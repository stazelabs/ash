// mcpbench drives bin/ashmcp over real stdio MCP transport and tokenizes
// the resulting CallToolResult envelopes, then compares to (a) direct
// CLI tokens from bench/baseline.json and (b) a simulated harness-native
// MCP equivalent that wraps the same payload in a CallToolResult of its
// own. Output goes into docs/value-assessment/06-mcp.md (ASH-182).
//
// What this measures
//
//	ashmcp_env_tok: the json-serialized CallToolResult that ashmcp emits
//	                to an MCP harness (StructuredContent + meta + the
//	                truncation TextContent sentinel where applicable).
//	                This is the production cost an MCP-aware harness
//	                pays per tool call.
//	cli_tok:        direct ash CLI cost from bench/baseline.json.
//	harness_env_tok: a CallToolResult of the same shape a hypothetical
//	                 harness-native MCP tool (Read/Grep/Glob in
//	                 another MCP server) would return: TextContent
//	                 holding the cat-n / file:line:content / glob-paths
//	                 payload, no StructuredContent. Built from the same
//	                 simulator used by cmd/harnessbench.
//
// Three-way comparison answers: "is ashmcp's envelope tax eating the
// per-call payload wins we have over CLI?" If ashmcp_env_tok is close
// to or below harness_env_tok, ashmcp is the right adoption surface.
//
// Not modeled: the *outer* JSON-RPC framing of stdio MCP (~30-60 bytes
// per request+response for jsonrpc/id/method wrapping). It applies
// uniformly to both ashmcp and harness-native, so the comparison is
// unaffected.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stazelabs/ash/internal/bench"
	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/mcpschema"
)

type baseline struct {
	Cases []baselineCase `json:"cases"`
}

type baselineCase struct {
	Name       string `json:"name"`
	Verb       string `json:"verb"`
	AshTokens  int    `json:"ash_tokens"`
	BashTokens int    `json:"bash_tokens"`
}

type row struct {
	name          string
	verb          string
	cliTok        int
	ashmcpTok     int
	harnessEnvTok int
	note          string
}

func main() {
	in := flag.String("in", "bench/baseline.json", "path to bench baseline json")
	out := flag.String("out", "-", "output markdown path; '-' for stdout")
	binPath := flag.String("ashmcp", "bin/ashmcp", "path to ashmcp binary")
	mcpFormat := flag.String("format", "json", "MCP emit format (json|pretty|compact) passed in tool args")
	flag.Parse()

	raw, err := os.ReadFile(*in)
	if err != nil {
		log.Fatalf("read %s: %v", *in, err)
	}
	var bl baseline
	if err := json.Unmarshal(raw, &bl); err != nil {
		log.Fatalf("parse %s: %v", *in, err)
	}
	counter, err := ledger.NewCounter()
	if err != nil {
		log.Fatalf("new counter: %v", err)
	}

	caseByName := make(map[string]bench.Case, len(bench.Cases))
	for _, c := range bench.Cases {
		caseByName[c.Name] = c
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sess, err := connectAshmcp(ctx, *binPath)
	if err != nil {
		log.Fatalf("connect ashmcp: %v", err)
	}
	defer sess.Close()

	rows := make([]row, 0, len(bl.Cases))
	for _, b := range bl.Cases {
		// Only verbs that have a clean harness-native MCP equivalent.
		if b.Verb != "read" && b.Verb != "grep" && b.Verb != "find" {
			continue
		}
		c, ok := caseByName[b.Name]
		if !ok {
			continue
		}
		// Skip absolute-path cases — they use {root} which only
		// expands inside the bench runner.
		if hasPlaceholder(c.AshArgs) {
			continue
		}

		ashmcpTok, err := callAshmcp(ctx, sess, c, counter, *mcpFormat)
		if err != nil {
			log.Printf("case %s ashmcp: %v", c.Name, err)
			continue
		}
		harnessTok, err := simulateHarnessEnvelope(c, counter)
		if err != nil {
			log.Printf("case %s harness sim: %v", c.Name, err)
			continue
		}
		rows = append(rows, row{
			name:          b.Name,
			verb:          b.Verb,
			cliTok:        b.AshTokens,
			ashmcpTok:     ashmcpTok,
			harnessEnvTok: harnessTok,
		})
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })

	var w bytes.Buffer
	emit(&w, rows)
	if *out == "-" {
		fmt.Print(w.String())
		return
	}
	if err := os.WriteFile(*out, w.Bytes(), 0o644); err != nil {
		log.Fatalf("write %s: %v", *out, err)
	}
}

// connectAshmcp spawns the ashmcp binary and completes the MCP handshake.
func connectAshmcp(ctx context.Context, binPath string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpbench", Version: "0.1.0"}, nil)
	transport := &mcp.CommandTransport{Command: exec.Command(binPath)}
	return client.Connect(ctx, transport, nil)
}

// callAshmcp invokes one tool call on the ashmcp session, captures the
// full CallToolResult envelope, and returns its serialized token count.
//
// We tokenize the json.Marshal of the entire result struct (Content,
// StructuredContent, IsError, Meta), which mirrors what an MCP harness
// reads off the wire. This is the production-shape cost an
// MCP-aware harness pays per ashmcp tool call.
func callAshmcp(ctx context.Context, sess *mcp.ClientSession, c bench.Case, counter *ledger.Counter, format string) (int, error) {
	toolName := mcpschema.ToolNamePrefix + c.Verb
	args := make(map[string]any, len(c.AshArgs)+1)
	for k, v := range c.AshArgs {
		args[k] = v
	}
	if format != "" && format != "json" {
		args["format"] = format
	}
	result, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: args})
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(result)
	if err != nil {
		return 0, err
	}
	return counter.Count(string(body)), nil
}

// simulateHarnessEnvelope constructs the CallToolResult a hypothetical
// harness-native MCP server (Read/Grep/Glob from another MCP server)
// would return for the same case. Format: TextContent only, holding
// the cat-n / file:line:content / paths payload — no StructuredContent.
// This matches Claude Code's harness-native tool wire shape (per docs:
// raw text content blocks, no structured payload).
func simulateHarnessEnvelope(c bench.Case, counter *ledger.Counter) (int, error) {
	argv, err := bench.BashFor(c)
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	stdout, err := cmd.Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			return 0, err
		}
	}
	var payload string
	switch c.Verb {
	case "read":
		payload = string(catNFormat(stdout))
	default:
		// grep/find: harness format ≡ bash format
		payload = string(stdout)
	}
	result := &mcp.CallToolResult{
		IsError: false,
		Content: []mcp.Content{&mcp.TextContent{Text: payload}},
	}
	body, err := json.Marshal(result)
	if err != nil {
		return 0, err
	}
	return counter.Count(string(body)), nil
}

// catNFormat prepends the cat -n line prefix (6-char right-padded line
// number, tab, content, newline) to each line of input. Mirrors the
// helper in cmd/harnessbench/main.go; kept inlined here to avoid
// cross-cmd import.
func catNFormat(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	lines := bytes.Split(b, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	var out bytes.Buffer
	out.Grow(len(b) + len(lines)*8)
	for i, line := range lines {
		fmt.Fprintf(&out, "%6d\t%s\n", i+1, line)
	}
	return out.Bytes()
}

func hasPlaceholder(args map[string]any) bool {
	for _, v := range args {
		if s, ok := v.(string); ok {
			if bytes.Contains([]byte(s), []byte("{root}")) {
				return true
			}
		}
	}
	return false
}

func emit(w *bytes.Buffer, rows []row) {
	var sumCLI, sumAshmcp, sumHarness int
	fmt.Fprintln(w, "| case | verb | cli_tok | ashmcp_env_tok | harness_env_tok | Δashmcp-vs-cli | Δashmcp-vs-harness |")
	fmt.Fprintln(w, "|---|---|---:|---:|---:|---:|---:|")
	for _, r := range rows {
		sumCLI += r.cliTok
		sumAshmcp += r.ashmcpTok
		sumHarness += r.harnessEnvTok
		fmt.Fprintf(w, "| `%s` | %s | %d | %d | %d | %+d%% | %+d%% |\n",
			r.name, r.verb, r.cliTok, r.ashmcpTok, r.harnessEnvTok,
			pctInt(r.ashmcpTok, r.cliTok), pctInt(r.ashmcpTok, r.harnessEnvTok))
	}
	fmt.Fprintf(w, "\n**Subset totals (%d cases):** cli %d tok, ashmcp_env %d tok, harness_env %d tok.\n",
		len(rows), sumCLI, sumAshmcp, sumHarness)
	if sumCLI != 0 {
		fmt.Fprintf(w, "* ashmcp vs cli:     **%+d%%** (envelope tax)\n", pctInt(sumAshmcp, sumCLI))
	}
	if sumHarness != 0 {
		fmt.Fprintf(w, "* ashmcp vs harness: **%+d%%**\n", pctInt(sumAshmcp, sumHarness))
	}
}

func pctInt(a, b int) int {
	if b == 0 {
		return 0
	}
	return int(float64(a-b) / float64(b) * 100)
}
