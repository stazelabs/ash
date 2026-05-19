// Hook handling for `ash hook` — the only client-only verb in ash.
//
// Flow per invocation:
//
//  1. Read full Claude PreToolUse payload from stdin.
//  2. Decide in-process via hook.Decide (pure rule evaluation).
//  3. Write the Claude decision JSON to stdout, exit 0.
//  4. Best-effort: dial the daemon UDS with a 5ms timeout. If reachable,
//     fire a normal ash request (verb=hook, args=parsed payload) and close
//     immediately without reading the response. The daemon's existing
//     dispatch path writes the ledger row. If the daemon isn't running:
//     skip silently — hook latency is on the agent's critical path and
//     auto-starting ashd here would defeat the purpose.
//
// Errors are intentionally swallowed: the hook should steer agents,
// never break their tool calls. Any failure path falls through to
// "allow" (no Claude output, exit 0).
package main

import (
	"io"
	"net"
	"os"
	"time"

	"github.com/stazelabs/ash/internal/config"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/session"
	"github.com/stazelabs/ash/internal/verbs/hook"
)

const hookDaemonDialTimeout = 5 * time.Millisecond

func runHook() {
	// `--event stop` routes to the Stop-hook fast path
	// (cmd/ash/hook_stop.go). The default `ash hook` shape with no
	// flags remains the PreToolUse path. Both share the same
	// soft-fail discipline: any failure exits 0.
	if len(os.Args) >= 3 && stopArgvHasEvent(os.Args[2:]) {
		runHookStop()
		return
	}
	root, wireArgs, ok := runHookFromReader(os.Stdin, os.Stdout)
	if !ok {
		return
	}
	// Fire-and-forget ledger row. Skip when:
	//   - we can't resolve a project root (running outside a repo)
	//   - the daemon isn't already up (don't auto-start; latency cost too high)
	fireHookLedger(root, wireArgs)
}

// runHookFromReader is the deterministic, daemon-free part of runHook —
// extracted so benchmarks and tests can drive it without touching real
// stdio or the daemon socket. Returns the resolved project root (empty
// if unresolvable), the wire args (for the caller's ledger fire), and
// true if processing succeeded; on a malformed or empty payload it
// returns ("", nil, false) and the caller should skip the ledger.
//
// The root is resolved once here and threaded into fireHookLedger so
// the post-decision ledger fire does not redo os.Getwd + session.Root.
func runHookFromReader(r io.Reader, w io.Writer) (string, map[string]any, bool) {
	payload, err := io.ReadAll(r)
	if err != nil || len(payload) == 0 {
		return "", nil, false
	}
	wireArgs, args, err := hook.ExtractArgs(payload)
	if err != nil {
		return "", nil, false
	}
	// Resolve project root once; reused for config load (only on deny,
	// see below) and the ledger socket (in fireHookLedger). Soft-fail:
	// empty root disables both, leaving the hook to operate on rules only.
	var root string
	if cwd, err := os.Getwd(); err == nil {
		if r, err := session.Root(cwd); err == nil {
			root = r
		}
	}

	// Decide first, load config later. [hook].exclude_verbs only matters
	// when the rules engine would otherwise deny — so for the (common)
	// allow path we skip config.Load entirely. On deny, we load config
	// and post-hoc apply the exclusion list via hook.MaybeExclude, which
	// produces the same Result shape as the upfront path would.
	result := hook.Decide(args)
	if result.Decision == "deny" && root != "" {
		if cfg, _, err := config.Load(root); err == nil && len(cfg.Hook.ExcludeVerbs) > 0 {
			if excluded := hook.MaybeExclude(result, cfg.Hook.ExcludeVerbs); excluded != result {
				result = excluded
				wireArgs["exclude_verbs"] = cfg.Hook.ExcludeVerbs
			}
		}
	}

	// Emit Claude decision (deny) or nothing (allow).
	if out, err := hook.EncodeClaudeDecision(result); err == nil && out != nil {
		_, _ = w.Write(out)
	}
	return root, wireArgs, true
}

func fireHookLedger(root string, wireArgs map[string]any) {
	if root == "" {
		return // no project root resolved upstream; skip ledger row
	}
	sock := session.SocketPath(root)
	// Pre-stat the socket so the daemon-down case is ~1µs (one syscall)
	// instead of the full 5ms dial timeout. The dial below is still the
	// safety net for races (socket exists but daemon is mid-shutdown or
	// hung) — its timeout still applies.
	if _, err := os.Stat(sock); err != nil {
		return // socket file absent; daemon almost certainly not running
	}
	conn, err := net.DialTimeout("unix", sock, hookDaemonDialTimeout)
	if err != nil {
		return // daemon not running; skip ledger row
	}
	defer conn.Close()
	// Cap the total time we'd spend writing the request frame so a hung
	// daemon can't stall the hook.
	_ = conn.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
	req := &proto.Request{
		V:    proto.ProtocolVersion,
		ID:   newID(),
		Verb: "hook",
		Args: wireArgs,
	}
	encoded, err := proto.EncodeRequest(req)
	if err != nil {
		return
	}
	// Best-effort write; intentionally do not call ReadFrame.
	// The daemon's WriteFrame back to us will fail silently when we
	// close — that's fine; the ledger row is already recorded.
	_ = proto.WriteFrame(conn, encoded)
}
