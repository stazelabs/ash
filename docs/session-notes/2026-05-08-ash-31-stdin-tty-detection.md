# Session: ASH-31 — tty-detection for --<arg> -

**Task.** Detect when stdin is a tty before blocking on io.ReadAll in resolveStdin; fail fast with a helpful error.

**Verbs used.** ash read, ash edit, ash diff (smoke test).

**Changes.**
- `cmd/ash/main.go` `resolveStdin`: added tty check using `os.Stdin.Stat()` and `fi.Mode()&os.ModeCharDevice != 0` before the `io.ReadAll` call. If stdin is a terminal, returns a typed `stdin_not_piped` error with two actionable hints: pipe content in, or pass the value directly.

**Implementation note.** Used stdlib `os.Stdin.Stat()` instead of `golang.org/x/term.IsTerminal` (which the issue suggested) — avoids adding a new dependency; `golang.org/x/sys` was already present as a transitive dep but `x/term` was not. The `ModeCharDevice` check is the standard stdlib pattern and works reliably on macOS/Linux.

**Friction.** None.

**Instrumentation.**
- `bin/ash diff --path f --content -` (no pipe) now exits 2 immediately with a clear message.
- `echo "..." | bin/ash diff --path f --content -` still works.
- All tests pass.
