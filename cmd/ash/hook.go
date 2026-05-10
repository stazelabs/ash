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
	payload, err := io.ReadAll(os.Stdin)
	if err != nil || len(payload) == 0 {
		return // allow
	}
	wireArgs, args, err := hook.ExtractArgs(payload)
	if err != nil {
		return // malformed payload — allow
	}
	// Load [hook].exclude_verbs from ash.toml. Soft-fail: any error
	// (outside a repo, missing file, parse error) leaves ExcludeVerbs
	// empty so the hook operates normally.
	if cwd, err := os.Getwd(); err == nil {
		if root, err := session.Root(cwd); err == nil {
			if cfg, _, err := config.Load(root); err == nil && len(cfg.Hook.ExcludeVerbs) > 0 {
				args.ExcludeVerbs = cfg.Hook.ExcludeVerbs
				wireArgs["exclude_verbs"] = cfg.Hook.ExcludeVerbs
			}
		}
	}

	result := hook.Decide(args)

	// Emit Claude decision (deny) or nothing (allow).
	if out, err := hook.EncodeClaudeDecision(result); err == nil && out != nil {
		_, _ = os.Stdout.Write(out)
	}

	// Fire-and-forget ledger row. Skip when:
	//   - we can't resolve a project root (running outside a repo)
	//   - the daemon isn't already up (don't auto-start; latency cost too high)
	fireHookLedger(wireArgs)
}

func fireHookLedger(wireArgs map[string]any) {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	root, err := session.Root(cwd)
	if err != nil {
		return
	}
	sock := session.SocketPath(root)
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
