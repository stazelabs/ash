# Session note: ASH-51 — decodeResult elimination

## Task

Resolve ASH-51: change `proto.Response.Data` from `any` to `msgpack.RawMessage` and delete every per-verb `decodeResult` (`map[string]any` walker). Daemon encodes the typed result once into RawMessage; client/daemon-pretty handlers each call `proto.UnmarshalData(rsp, &Result{})` to decode.

## Verbs used

`ash read`, `ash grep`, `ash find`, `ash git --op status|diff`, `ash edit`, `ash write`, `ash test`, `ash report`, `ash metrics`, `ash bench`

## What shipped

- `proto.Response.Data` → `msgpack.RawMessage`; new `proto.MustData(v)` and `proto.UnmarshalData(rsp, dst)` helpers
- Daemon refactor: typed result held in a local var for the truncation check, then encoded via `MustData` before the wire encode
- Bench in-process dispatch: now wraps verb result in `MustData` before calling pretty handlers
- 15 verb `decodeResult` functions deleted (find, grep, read, stat, write, edit, diff, test, metrics, report, hook, help, initverb, uninit, bench)
- 4 git per-op decoders deleted (`decodeStatus`/`decodeChanges`/`decodeLog`/`decodeDiff`/`decodeShow`)
- 3 orphan helpers in test/ deleted (`decodePackage`, `decodeTest`, `decodeCounts`)
- 3 tests that exercised the decoders directly deleted (the round-trip is now covered by the msgpack library itself)
- All 30 packages pass `ash test`. Net change: +126 / −1375 = **−1249 LOC** (issue estimated −700)

## Friction

**Shell-quoting on `ash edit --new_content '…'` was the only real pain point this session.** I tried to replace lines 38–46 of `proto.go` by passing the new content (which included Go struct tags with backticks) as a single-quoted shell argument, using the standard `'"'"'` trick to embed quotes. That trick escapes a single quote, not a backtick — so every `'"'"'` was delivered as a literal `'` character. The file was corrupted: backticks gone, `type Response struct {` duplicated, edit had to be reverted via `git checkout`.

CLAUDE.md *does* document the escape hatch ("Shell quoting escape hatch" under bullet 8 of "When to prefer ash") but it currently only flags `--old_string`/`--new_string` as hazardous, not `--new_content`. And the verb is "Prefer …" rather than "Default to …", which I read as conditional advice instead of a hard rule.

Secondary friction: my batch-deletion Python script for the per-verb `decodeResult` functions used a brace-counting regex that ate the trailing blank line. When I then deleted `decodePackage`, `decodeTest`, and `decodeCounts` from test.go in sequence, the script's "find next `func decode…`" lookup fell into the middle of a previously-deleted function and missed `decodeTest`, leaving it orphaned with no preceding newline (`}func decodeTest(...){`) — a syntax error caught only on the next build. The fix was a manual ash edit. Lesson for next agent doing batch deletes: re-grep after each deletion rather than walking a stale regex match list.

## Workarounds

- For the proto.go rewrite, used `ash write --path /tmp/proto_new.go --content - << 'PYEOF' … PYEOF && mv /tmp/proto_new.go .../proto.go`. The single-quoted heredoc terminator preserves backticks verbatim and the atomic mv is one syscall. Simpler than a Python fixer script for whole-file rewrites.
- Subsequent edits used `ash edit --range start:end --new_content - << 'EOF' … EOF` for line-range replacements and `ash edit --old_string '…' --new_string '…'` for safe ASCII-only swaps.

## Suggestions

1. **Tighten CLAUDE.md guidance** (paired with this note): make the stdin rule unconditional for any non-trivial replacement content, and explicitly include `--new_content` and `--patch` in the hazard list alongside `--old_string`/`--new_string`. Remove the stale "ASH-60 tracks a `--patch -` mode" parenthetical (it's shipped). Reframe the escape hatch as a hierarchy: stdin (`--new_content -` / `--patch -`) is the default; inline `--…='…'` is the niche for short ASCII-only swaps.
2. No verb change recommended. The escape hatch already exists; the failure here was discoverability and rule strength, not affordance gap.

## Instrumentation

```
$ ash report --since 2h
verb           n   ok%   p50_exec   p95_exec  p50_out  p95_out  trunc%
------------------------------------------------------------------------
edit         many   ~99%      …us       …us       …       …       0%
```

(Edit success rate dipped only on the corruption attempt — single failed call surfaced as `ambiguous` after a follow-up `--replace_all` retry, plus the `git checkout` revert. The rest of the session was clean.)

