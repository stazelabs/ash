# ASH-48: hook bash detection bypassed by quoted program names

**Task.** Fix the hook's `firstToken` so that `"grep"`, `'grep'`, and `\grep` all trigger the deny-list, matching the behavior the Python predecessor got for free from `shlex.split`.

**Verbs used.** `ash read`, `ash edit`, `ash write`, `ash grep`, `ash test`

**Changes.**
- Added `unquoteToken(s string) string` helper: strips surrounding `"..."` or `'...'`, or a leading `\`, before the deny-list lookup.
- `firstToken` now calls `unquoteToken` before `isPrefixWord` and `filepath.Base`, so quoted prefix words (`"env"`, `"command"`) are also unquoted before the check.
- 6 new test cases in `hook_test.go`: double-quoted grep/find/cat, single-quoted grep, backslash-escaped grep, quoted env prefix chained into grep.

**Friction (significant) — shell quoting vs. Go char literals.**
The first `ash edit` attempt passed the new function body via a single-quoted shell string containing `'"'` and `'\''` and `'\\'`. The shell stripped the embedded single quotes, corrupting the Go char literals. The broken file had `s[0] == ' && ...` instead of `s[0] == '"' && ...`.

Workaround: wrote a Python fixer script to `/tmp/fix_hook.py` using `ash write --content -` with a heredoc (heredoc with quoted delimiter is literal), then ran `python3 /tmp/fix_hook.py`. The Python script constructed the correct char literals at runtime via `sq = "'"`, `dq = '"'`, `bs = "\\"` concatenation, avoiding all shell/heredoc interpolation issues.

An additional off-by-one in the Python slicer left a double `}}` — fixed with `ash edit --range 539:539` to delete the extra line, then `ash edit --range 538:538 --new_content $'...\n}'` to restore the single closing brace.

**Suggestions.**
- `ash edit --new_string -` (stdin) would have avoided the shell quoting problem entirely if Go char literals were the argument. But `--old_string` has the same quoting problem on the input side, making it only half a solution.
- Longer term: an `ash edit --patch -` mode (accept a unified diff on stdin) would eliminate the shell quoting problem entirely for both sides of a replacement.
- The Python script approach is a reliable escape hatch for content with significant quoting requirements — worth documenting.

**Instrumentation.**
```
verb    n   ok%   p50_exec
hook    ~8  100%  ~10us
test    3   100%  ~220ms
```
