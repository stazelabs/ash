# ASH-47: hook redirect sync — git diff/show + go test

**Task.** Extend the PreToolUse hook to redirect `git diff`, `git show`, and `go test` to their ash equivalents, which had shipped but were missing from the deny list.

**Verbs used.** `ash read`, `ash edit`, `ash grep`, `ash find`, `ash test`, `ash report`

**Changes.**
- `gitRedirect` extended: `{"status", "log", "diff", "show"}`
- `suggestTest` helper added; `decideBash` now intercepts `prog == "go" && args[0] == "test"` and suggests `ash test --packages <pkgs>`
- Tests: flipped `git diff allows` and `go test allows` to deny expectations; added `git diff staged`, `git show`, `go test no args`, `go vet allows`; fixed the `all-allowed chain` test which previously relied on `go test` passing through

**Friction.** None — the change was mechanical. The hook structure made adding a new redirect straightforward.

**Suggestions.**
- The issue description flags a good long-term fix: derive the redirect list from a single source of truth (e.g., a live-verbs registry) so adding a verb auto-updates the hook. Right now it requires a manual two-file edit (hook.go + hook_test.go) every time a verb ships.

**Instrumentation.**
```
verb    n  ok%   p50_exec
hook    2  100%  6us
test    2  100%  330ms
```
