# 2026-05-10 — ASH-69: hook redirects cat/echo/printf/tee + `>` to `ash write`

## Task

Resolve ASH-69. The PreToolUse hook misclassified `cat > FILE << EOF` as a read and built a malformed `ash read --path '>'` suggestion because (a) the redirected idiom is structurally a write, not a read, and (b) `positionalArgs` did not strip shell redirection operators before feeding the path into the suggestion builders.

## Verbs used

- `ash help --verb edit` — re-confirm patch-mode requires proper `@@ -a,b +c,d @@` headers; switched to `--range` mode for the bigger insertions.
- `ash read --range` — to navigate `internal/verbs/hook/hook.go` (~31 KB) without paying the full-file token tax.
- `ash grep` — to find function definition lines and ASH-69 references across the repo.
- `ash edit` (range / old_string modes) — every code mutation. Range mode for the big insertion after `positionalArgs`; old_string mode for the smaller inserts.
- `ash write` (heredoc) — staged the hook payload in `/tmp/ash-69-payload.json` for the smoke test, and wrote this note.
- `ash test` — confirmed all 33 packages still pass.
- `ash hook < payload.json` — end-to-end smoke test.

## Friction

1. **Patch-mode unified-diff parser is strict.** First attempt used a bare `@@` hunk header without the `-a,b +c,d` ranges. Got `patch_parse_error: not a hunk header: "@@"`. Help text says "Unified diff to apply" but doesn't call out the strictness. Range mode worked fine — but for an additive insertion that doesn't touch existing lines, patch mode would have been more natural if it accepted bare hunk headers (or auto-derived ranges). Worth a `patch_parse_error` improvement that hints at the missing range header.

2. **Hook self-trip on the smoke-test command.** First smoke-test invocation was `printf '%s' '{"tool_name":"Bash"...command":"cat > /tmp/x..."}}' | bin/ash hook`. The OUTER `printf` got intercepted by my own new rule because `tokenize` (whitespace-only) saw the inner `>` as a positional `>` token. The detection fired correctly per its rules — just on the wrong layer. This is the existing ASH-19 limitation (per-segment tokenizer is naïve about quotes). The fix here was to stage the payload as a file and pipe the file. **My layer-2 rule does increase the surface area where naïve tokenization bites** (`echo "use a > b"` with literal quoted `>` would now deny as Bash:redirect-write). I judged it acceptable: agents rarely echo strings containing literal `>`, and the deny-and-nudge philosophy means the worst case is a session note. ASH-19 is the right place to fix tokenize properly.

3. **Test loop already enforces nudge-tail and substring-suggested checks** — extending the loop with a "no `>` in suggestion path" regression assertion was a one-line add. Good ergonomics.

## Workarounds

- For multiline content embedded in the test cases (the new ASH-69 cases use Go raw strings `` `…` `` for cases with quoted `"hello"`), I leaned on the existing test-table style, which uses Go double-quoted strings with `\"` escapes plus raw strings where backticks aren't already used. No surprises.

## Suggestions

- **`ash edit --patch` should accept hunks without explicit ranges, or fail with a more pointed error.** "not a hunk header: `@@`" is technically accurate but doesn't tell the agent what's missing. A nudge like `patch_parse_error: hunk header missing line ranges; expected '@@ -a,b +c,d @@'` would save a doc lookup.
- **Quote-aware `tokenize`** (ASH-19 territory) — my new layer-2 rule extends the false-positive surface for `echo`/`printf` with literal `>` inside quoted strings. Doable as a small follow-up: track single/double quote state in a local fields-style scanner and skip operator detection inside quoted regions. Keeping it out of this PR to keep ASH-69 scope tight.
- **`ash test --packages` accepts a comma-joined list of packages**? Worth confirming — when iterating I ran `ash test --packages ./internal/verbs/hook/...` but several other plausible forms (space-joined, repeated flag) might also work. Help text could call out the canonical form.

## Instrumentation

After landing the fix and rebuilding the daemon, smoke-tested the original failure case from the issue:

```
$ bin/ash hook < /tmp/ash-69-payload.json   # cat > /tmp/new_readme.md << 'EOF'\nfoo\nEOF
{"hookSpecificOutput":{...,"permissionDecision":"deny",
 "permissionDecisionReason":"Use ash instead: `ash write --path /tmp/new_readme.md --content - << 'EOF'`
                            (bash `cat ... > FILE` is redirected to ash write in this repo). ..."}}
```

And the `2>&1` pollution case:

```
$ bin/ash hook < /tmp/ash-69-payload.json   # grep foo bar.txt 2>&1
{"hookSpecificOutput":{...,"permissionDecisionReason":
 "Use ash instead: `ash grep --pattern foo --path bar.txt` ..."}}
```

Both now produce non-malformed, useful suggestions. `ash report --verb hook` after a few sessions will show whether agents are following the new write suggestion or still reaching for `cat > FILE` (the recursive-development signal we want).

Test suite: 33/33 pkgs pass, including 12 new hook tests covering layer-1 (redirection stripping) and layer-2 (cat/echo/printf/tee + `>` → ash write) plus three unit-test functions for the new helpers (`classifyRedirectToken`, `stripRedirections`, `detectOutputRedirect`).
