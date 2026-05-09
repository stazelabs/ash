# ASH-63: hook bash segmenter over-matched on heredoc body content

**Task.** Plan ASH-61 (ashd configuration file). The plan write-up itself was non-trivial — a docs page with a markdown table listing every verb's path-arg site. Writing it via `ash write --content - <<'DOC_EOF' … DOC_EOF` triggered the hook with `Use ash instead: ash find --path .`. There was no `find` invocation in the bash command; the hook was over-matching.

**Verbs used.** `ash read`, `ash grep`, `ash find`, `ash edit`, `ash write`, `ash test`, `ash git --op {status,log,diff}`, `ash stat`

**Root cause.** [internal/verbs/hook/hook.go](../../internal/verbs/hook/hook.go) `segments()` is shell-operator-aware (single quotes, double quotes, parens, backticks, escapes) but had an explicit comment opting out of heredoc handling at top level — assuming heredocs always appear inside `$(...)` and are protected by paren tracking. That assumption is false for `ash write --content - <<EOF…EOF`, which is the canonical pattern for any large content the agent writes (docs, generated code, commit messages with bodies).

In my failing case the heredoc body had markdown table rows like `| read | foo/read.go | path |`. The `|` characters at unquoted top level were treated as pipe operators; each cell flushed a segment. The cell containing just `find` became a standalone segment; `firstToken` returned `prog="find"`, `args=[]`; the bash-find rule fired with the no-positional default and produced `ash find --path .` — exactly the deny message I got.

**Fix.** Added `scanHeredoc(s, start) (delimEnd, bodyEnd, ok)` and a new case in `segments()` for top-level `<<DELIM`. Operator + delimiter stay in the segment text (so `firstToken` still sees the host program correctly), but body bytes never enter `cur` and so cannot produce false segments. Recognises `<<W`, `<<-W`, `<<'W'`, `<<"W"`, `<<\W`. Unterminated heredocs are consumed to EOF. Forces `flush()` after the body so post-terminator content starts a fresh segment.

7 new test cases in `hook_test.go` covering: markdown table in body, unquoted body with bash operators, double-quoted delim, strip-tabs delim, command-after-terminator-still-parsed, unterminated heredoc, plus the existing `$(...)` case still allowed.

**Friction (significant) — recurrence of the ASH-48 quoting problem.**
First attempt to add `scanHeredoc` via `ash edit --new_string '…'` died on shell quoting: the function body has Go char literals `'\''`, `'"'`, `'\\'`, `'\t'`, `'\n'`, plus the operator `<` characters. The shell ate quote pairs and the build failed with `empty rune literal or unescaped '`.

Workaround: same Python-writer pattern noted in `2026-05-09-ash-48-quoted-prog-names.md`. Wrote `/tmp/hook_patch2.py` via `ash write --content - << 'PYEOF' … PYEOF`, the script constructed Go char literals from byte-by-byte string concatenation (`GO_SQ_RUNE = "'" + BS + SQ + "'"`), then `re.sub`'d the function body in place. Worked first try after that. Same path the ASH-48 note recommended; this is now the second time it's been the right call.

**Suggestions.**
- ASH-60's `ash edit --patch -` (already shipped) is the structural answer to this. A unified diff on stdin sidesteps the shell-quoting problem on both sides of a replacement. Should default to it for any edit whose `old_string`/`new_string` contains rune literals or quote characters.
- The Python-writer escape hatch is reliable but boils down to "give me an editor with a clean text channel". A short doc for the `ash edit --patch -` mode in CLAUDE.md would help future agents reach for it instead of fighting the shell.
- Long-tail: the hook segmenter still doesn't treat `\n` as a top-level command separator. With heredocs out of the way, that gap is more visible — `cmd1\ncmd2` parses as one segment today. Worth a follow-up if the agent starts emitting multi-line bash commands routinely.

**Instrumentation.**
```
verb    n    ok%    p50_exec
write   ~8   100%   ~330us
edit    ~3   100%   ~500us
test    ~3   100%   ~470ms (full repo, 30 pkgs)
hook    n/a  —      not exercised in this session beyond test runs
```

End-to-end verification: feeding the original failing command to `bin/ash hook` returns allow (empty output, exit 0). The same `ash write` shape that triggered the deny while writing `docs/configuration.md` now succeeds against the rebuilt `bin/ash`.
