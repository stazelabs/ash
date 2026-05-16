// Command wirecmp measures the wire cost of running the same verb intent
// over the CLI vs MCP transports (ASH-123).
//
// For each fixture, wirecmp opens a UDS connection to the local ashd,
// dispatches the request once, and renders the response both ways:
//
//   - CLI shape: verbs.PrettyHandlers()[verb](req, rsp) — the daemon-
//     pretty-rendered text bin/ash prints to the user.
//   - MCP shape: proto.MCPEnvelope(rsp) — the JSON envelope ashmcp emits
//     as TextContent to the harness.
//
// Both renders happen from the same Response so the comparison isolates
// transport overhead from verb behavior. The tool reports bytes,
// cl100k_base tokens, and median latency over -repeat trials. With
// ANTHROPIC_API_KEY set and -claude, it also calls count_tokens for the
// same payloads so the comparison reflects what Claude actually charges
// rather than cl100k_base approximations.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/session"
	"github.com/stazelabs/ash/internal/verbs"
)

type fixture struct {
	Name string
	Verb string
	Args map[string]any
}

// Canonical read-side intents. Stay deliberately small so the
// comparison reflects the protocol overhead, not the verb workload.
//
// Numeric / bool args use natural Go types — the daemon's argutil
// layer accepts every integer flavor msgpack can decode (ASH-149), so
// this fixture set mirrors a caller that hands the daemon int / bool
// values directly (programmatic agents, internal tooling, replay) and
// the find/grep rows here would have measured an `args` error envelope
// under the pre-ASH-149 coercer.
var fixtures = []fixture{
	{"read README:1-60", "read", map[string]any{"path": "README.md", "range": "1:60"}},
	{"find **/*.go (20)", "find", map[string]any{"path": ".", "glob": "**/*.go", "limit": 20}},
	{"find **/*.go --meta (20)", "find", map[string]any{"path": ".", "glob": "**/*.go", "limit": 20, "meta": true}},
	{"grep ^func Run", "grep", map[string]any{"path": ".", "pattern": "^func Run", "max": 20}},
	{"stat README.md", "stat", map[string]any{"path": "README.md"}},
	{"git status", "git", map[string]any{"op": "status"}},
	{"help", "help", map[string]any{}},
}

func main() {
	repeat := flag.Int("repeat", 5, "roundtrips per transport for latency median")
	outPath := flag.String("out", "", "markdown output path (default stdout)")
	claudeFlag := flag.Bool("claude", false, "also call Anthropic count_tokens (needs ANTHROPIC_API_KEY)")
	model := flag.String("model", "claude-sonnet-4-5", "model for count_tokens when -claude")
	prettyFlag := flag.Bool("pretty", false, "measure MCP under format=pretty (ASH-146) instead of the default JSON envelope")
	flag.Parse()

	root, err := session.Root(".")
	if err != nil {
		die("project root: %v", err)
	}
	jail.SetPolicy(jail.FromConfig(false, root, nil, nil))
	sock := session.SocketPath(root)

	counter, err := ledger.NewCounter()
	if err != nil {
		die("counter: %v", err)
	}
	pretty := verbs.PrettyHandlers()

	var apiKey string
	if *claudeFlag {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			die("-claude requires ANTHROPIC_API_KEY")
		}
	}

	type row struct {
		Name                                                 string
		CLIBytes, CLITokens, MCPBytes, MCPTokens             int
		CLITokensClaude, MCPTokensClaude                     int
		CLILatencyUs, MCPLatencyUs                           int64
	}
	var rows []row

	for _, f := range fixtures {
		// One canonical roundtrip with Transport=mcp so we get a Response
		// that's structurally identical to what ashmcp would receive.
		req := &proto.Request{
			V: proto.ProtocolVersion, ID: newID(),
			Verb: f.Verb, Args: f.Args,
			Transport: proto.TransportMCP,
		}
		rsp, err := roundtripOnce(sock, req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "wirecmp: %s: %v\n", f.Name, err)
			continue
		}

		// CLI shape: daemon's pretty render. PrettyResponseHeader is the
		// fallback for verbs that don't register a custom renderer.
		var cliText string
		if p, ok := pretty[f.Verb]; ok {
			cliText = p(req, rsp)
		} else {
			cliText = proto.PrettyResponseHeader(rsp)
		}
		cliBytes := len(cliText)
		cliTokens := counter.Count(cliText)

		// MCP shape: what ashmcp would actually emit as TextContent. In
		// the default JSON envelope mode (the pre-ASH-146 surface) this
		// is proto.MCPEnvelope. Under -pretty, ashmcp ships the
		// daemon-pretty render instead — same text the CLI prints — so
		// the harness pays CLI-equivalent token cost. Truncated
		// responses gain a prepended sentinel TextContent block
		// (ASH-127) in either mode; we mirror its cost here so wirecmp
		// stays byte-identical to the daemon's tokens_out_emit
		// accounting.
		var mcpText string
		if *prettyFlag {
			mcpText = cliText
		} else {
			env, err := proto.MCPEnvelope(rsp)
			if err != nil {
				fmt.Fprintf(os.Stderr, "wirecmp: %s envelope: %v\n", f.Name, err)
				continue
			}
			mcpText = string(env)
		}
		mcpBytes := len(mcpText)
		mcpTokens := counter.Count(mcpText)
		if sentinel := proto.MCPTruncationSentinel(rsp); sentinel != "" {
			mcpBytes += len(sentinel)
			mcpTokens += counter.Count(sentinel)
			mcpText = sentinel + "\n" + mcpText
		}

		// Latency: median of N roundtrips per transport.
		cliLat := medianRoundtripUs(sock, f.Verb, f.Args, "", *repeat)
		mcpLat := medianRoundtripUs(sock, f.Verb, f.Args, proto.TransportMCP, *repeat)

		r := row{
			Name: f.Name,
			CLIBytes: cliBytes, CLITokens: cliTokens,
			MCPBytes: mcpBytes, MCPTokens: mcpTokens,
			CLILatencyUs: cliLat, MCPLatencyUs: mcpLat,
		}
		if *claudeFlag {
			n1, err := countTokensAnthropic(apiKey, *model, cliText)
			if err != nil {
				fmt.Fprintf(os.Stderr, "wirecmp: claude (%s cli): %v\n", f.Name, err)
			}
			n2, err := countTokensAnthropic(apiKey, *model, mcpText)
			if err != nil {
				fmt.Fprintf(os.Stderr, "wirecmp: claude (%s mcp): %v\n", f.Name, err)
			}
			r.CLITokensClaude = n1
			r.MCPTokensClaude = n2
		}
		rows = append(rows, r)
		fmt.Fprintf(os.Stderr, "wirecmp: %-24s cli=%4dB/%4dt mcp=%4dB/%4dt (Δ %+5dB / %+4dt)\n",
			f.Name, r.CLIBytes, r.CLITokens, r.MCPBytes, r.MCPTokens,
			r.MCPBytes-r.CLIBytes, r.MCPTokens-r.CLITokens)
	}

	out := os.Stdout
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			die("create %s: %v", *outPath, err)
		}
		defer f.Close()
		out = f
	}
	fmt.Fprintln(out, "# wirecmp: CLI vs MCP wire cost")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Same intent, two transports. CLI = daemon-pretty render; MCP = JSON envelope ashmcp emits as TextContent. Both renders are computed from a single daemon roundtrip per fixture; latency is the median of `-repeat` trials per transport.")
	fmt.Fprintln(out)
	if *claudeFlag {
		fmt.Fprintln(out, "| fixture | CLI bytes | CLI cl100k | CLI claude | MCP bytes | MCP cl100k | MCP claude | Δ bytes | Δ cl100k | Δ claude | CLI p50 | MCP p50 |")
		fmt.Fprintln(out, "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	} else {
		fmt.Fprintln(out, "| fixture | CLI bytes | CLI cl100k | MCP bytes | MCP cl100k | Δ bytes | Δ cl100k | CLI p50 | MCP p50 |")
		fmt.Fprintln(out, "|---|---:|---:|---:|---:|---:|---:|---:|---:|")
	}
	for _, r := range rows {
		if *claudeFlag {
			fmt.Fprintf(out, "| %s | %d | %d | %d | %d | %d | %d | %+d (%+.0f%%) | %+d (%+.0f%%) | %+d (%+.0f%%) | %s | %s |\n",
				r.Name, r.CLIBytes, r.CLITokens, r.CLITokensClaude,
				r.MCPBytes, r.MCPTokens, r.MCPTokensClaude,
				r.MCPBytes-r.CLIBytes, pct(r.MCPBytes-r.CLIBytes, r.CLIBytes),
				r.MCPTokens-r.CLITokens, pct(r.MCPTokens-r.CLITokens, r.CLITokens),
				r.MCPTokensClaude-r.CLITokensClaude, pct(r.MCPTokensClaude-r.CLITokensClaude, r.CLITokensClaude),
				fmtUs(r.CLILatencyUs), fmtUs(r.MCPLatencyUs))
		} else {
			fmt.Fprintf(out, "| %s | %d | %d | %d | %d | %+d (%+.0f%%) | %+d (%+.0f%%) | %s | %s |\n",
				r.Name, r.CLIBytes, r.CLITokens, r.MCPBytes, r.MCPTokens,
				r.MCPBytes-r.CLIBytes, pct(r.MCPBytes-r.CLIBytes, r.CLIBytes),
				r.MCPTokens-r.CLITokens, pct(r.MCPTokens-r.CLITokens, r.CLITokens),
				fmtUs(r.CLILatencyUs), fmtUs(r.MCPLatencyUs))
		}
	}

	// Summary totals so a reader can see the net direction without
	// eyeballing every row.
	var sumCLIb, sumCLIt, sumMCPb, sumMCPt int
	var sumCLIcl, sumMCPcl int
	for _, r := range rows {
		sumCLIb += r.CLIBytes
		sumCLIt += r.CLITokens
		sumMCPb += r.MCPBytes
		sumMCPt += r.MCPTokens
		sumCLIcl += r.CLITokensClaude
		sumMCPcl += r.MCPTokensClaude
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "**Totals** — CLI %dB / %d cl100k, MCP %dB / %d cl100k. Δ %+dB (%+.1f%%) / %+d cl100k tokens (%+.1f%%).\n",
		sumCLIb, sumCLIt, sumMCPb, sumMCPt,
		sumMCPb-sumCLIb, pct(sumMCPb-sumCLIb, sumCLIb),
		sumMCPt-sumCLIt, pct(sumMCPt-sumCLIt, sumCLIt))
	if *claudeFlag {
		fmt.Fprintf(out, "Claude: CLI %d, MCP %d, Δ %+d (%+.1f%%).\n",
			sumCLIcl, sumMCPcl, sumMCPcl-sumCLIcl, pct(sumMCPcl-sumCLIcl, sumCLIcl))
	}
}

func roundtripOnce(sock string, req *proto.Request) (*proto.Response, error) {
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial: %w (is ashd running?)", err)
	}
	defer conn.Close()
	encoded, err := proto.EncodeRequest(req)
	if err != nil {
		return nil, err
	}
	if err := proto.WriteFrame(conn, encoded); err != nil {
		return nil, err
	}
	buf, err := proto.ReadFrame(conn)
	if err != nil {
		return nil, err
	}
	rsp, err := proto.DecodeResponse(buf)
	if err != nil {
		return nil, err
	}
	// Mirror what ashmcp's roundtrip does so the envelope-token count
	// matches what the harness would actually see.
	if rsp.Metrics != nil {
		rsp.Metrics.BytesOut = len(buf)
	}
	return rsp, nil
}

func medianRoundtripUs(sock, verb string, args map[string]any, transport string, n int) int64 {
	if n <= 0 {
		n = 1
	}
	lats := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		req := &proto.Request{
			V: proto.ProtocolVersion, ID: newID(),
			Verb: verb, Args: args, Transport: transport,
		}
		t0 := time.Now()
		if _, err := roundtripOnce(sock, req); err != nil {
			continue
		}
		lats = append(lats, time.Since(t0).Microseconds())
	}
	if len(lats) == 0 {
		return 0
	}
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	return lats[len(lats)/2]
}

func newID() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint64(time.Now().UnixNano())
	}
	return binary.BigEndian.Uint64(b[:])
}

func pct(num, denom int) float64 {
	if denom == 0 {
		return 0
	}
	return float64(num) / float64(denom) * 100
}

func fmtUs(us int64) string {
	switch {
	case us < 1000:
		return fmt.Sprintf("%dµs", us)
	case us < 1_000_000:
		return fmt.Sprintf("%.1fms", float64(us)/1000)
	default:
		return fmt.Sprintf("%.2fs", float64(us)/1_000_000)
	}
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "wirecmp: "+format+"\n", a...)
	os.Exit(1)
}

// countTokensAnthropic mirrors cmd/encexplore/validate.go's helper so
// wirecmp stays self-contained — extracting it to internal/ would be a
// premature shared utility for two callers.
type countTokensReq struct {
	Model    string           `json:"model"`
	Messages []map[string]any `json:"messages"`
}

type countTokensResp struct {
	InputTokens int `json:"input_tokens"`
}

func countTokensAnthropic(key, model, content string) (int, error) {
	r := countTokensReq{Model: model, Messages: []map[string]any{
		{"role": "user", "content": content},
	}}
	body, err := json.Marshal(r)
	if err != nil {
		return 0, err
	}
	httpReq, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages/count_tokens", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", key)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	client := &http.Client{Timeout: 30 * time.Second}
	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		resp, err = client.Do(httpReq)
		if err == nil && resp.StatusCode < 500 {
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(time.Duration(1<<attempt) * time.Second)
	}
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	var ctr countTokensResp
	if err := json.NewDecoder(resp.Body).Decode(&ctr); err != nil {
		return 0, err
	}
	return ctr.InputTokens, nil
}
