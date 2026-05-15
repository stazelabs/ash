# ASH-143 — make `ash edit --old/--new @PATH` form discoverable

## Task

ASH-117 and ASH-121 both filed `--old @file` / `--new @file` as a *feature ask* — independent of each other, in two consecutive sessions, by the same agent — without realizing `@PATH` was already shipped and documented in `ash help --verb edit`. That's a discoverability problem, not a feature gap. ASH-143 was filed to fix it.

## Scope shift from the original ticket

The ticket suggested adding an example to the `Long` description of `--old` / `--new` (referencing the `--bytes` Long for precedent). When I picked it up I found two things that pointed at a different fix:

1. **The `Long` field never reaches the client.** [help.go:26](../../internal/verbs/help/help.go#L26) declares `Long string \`msgpack:"-"\`` — Long is deliberately stripped from the wire shape. The daemon has it, the client renderer doesn't. So `ash help --verb edit --verbose true` shows the *Description* (the short form), not the Long. Updating Long would be invisible to the agent. **This is itself a real bug** — `--verbose` on `help` is effectively a no-op today — but it's out of scope for ASH-143. Flagged as a follow-up below.
2. **The `PH` schema field already documents the form options** (`<text|@file|->`) but `writeArg` only renders type, not PH. So agents see `--old:string — Exact text to find; pass '-' for stdin or '@PATH' for a file; …` — the `@PATH` cue gets buried mid-sentence and is easy to skip. The structural fix is to surface PH in the signature line itself.

Decision: do the structural fix instead of the Long-text tweak.

## What changed

**[internal/verbs/help/help.go](../../internal/verbs/help/help.go)** — three things:

1. `writeArg` now inlines `a.PH` after `--name:type[!|=default]` when PH is set. The per-arg help signature changes from
   `--old:string — Exact text…`
   to
   `--old:string <text|@file|-> — Exact text…`
   — and the `@file` placeholder is now impossible to miss.
2. `--old:string` (string mode) — Description trimmed: dropped `pass '-' for stdin or '@PATH' for a file;` since the PH placeholder now carries that information visually. Keeps the semantic clauses (`must appear once unless all=true`).
3. `--new:string` (both string and range modes) — same trim.

The change affects all 11 PH-bearing args across verbs (`edit --old`, `edit --new` ×2, `edit --patch`, `find --paths`, `git --ref`, `git --range`, `bench --compare`, `bench --baseline`, `bench --micro_packages`, `test --packages`, `test --timeout`, `stat --paths`) — they all now show their placeholder inline. That's an incidental UX win on top of the targeted fix.

**Before / after** for the verb most affected:

```
# Before
  --old:string — Exact text to find; pass '-' for stdin or '@PATH' for a file; must appear once unless all=true.
  --new:string — Replacement text; pass '-' for stdin or '@PATH' for a file; empty string deletes the match.

# After
  --old:string <text|@file|-> — Exact text to find; must appear once unless all=true.
  --new:string <text|@file|-> — Replacement text; empty string deletes the match.
```

The PH placeholder is now structurally visible and the Description shed redundant prose.

**[docs/mcp/tools.json](../../docs/mcp/tools.json)** + **[cmd/ashmcp/tools.json](../../cmd/ashmcp/tools.json)** — regenerated via `make schema`. MCP tool descriptions for `edit` (`old`, `new`) reflect the trimmed Description. PH does not flow to JSON Schema (the schema gets its info from `type` and `description`); MCP clients are unaffected structurally.

**[docs/vocab/inventory.json](../../docs/vocab/inventory.json)** — regenerated via `make vocab`. Inventory.md was byte-identical (vocab tracks atom strings, not Description prose); inventory.json had a trivial reformat that the drift gate caught.

## Verbs used

`ash read`, `ash grep`, `ash find`, `ash edit` (`--old`/`--new` inline `$'…'`), `ash write` (probe + session note), `ash test`, `ash help` (with and without `--verbose`), `ash git` (`status`, `diff`). `make all`, `make vocab-check`, `make vocab`, `make schema-check`, `make schema`, `make validate-check` — all green after regen.

Also exercised the [[ash-edit-atpath-form]] memory pattern that was saved end-of-last-session: when validating the `@PATH` form actually works, I wrote `/tmp/ash121-edit-old.txt` and `/tmp/ash121-edit-new.txt` via `ash write` and passed them as `--old @/tmp/old.txt --new @/tmp/new.txt`. Worked first try; no `$'…'` quoting friction.

## Friction

- **`ash help --verbose true` is a no-op for the client renderer.** Long descriptions are excluded from the wire shape ([help.go:26](../../internal/verbs/help/help.go#L26): `Long string \`msgpack:"-"\``), so the client never sees them. The daemon-side code path that would render Long (in `writeArg` when `verbose` is true) only fires daemon-side, but the daemon doesn't render pretty — the client does. Two paths to fix: (a) drop `msgpack:"-"` on Long and ship it over the wire (cheap; ~1-2 KiB per help response for verbose users), or (b) move pretty rendering to the daemon for help and serialize the final string. Worth a ticket — agents passing `--verbose true` today silently get nothing.
- **`ash git --op diff` patch is byte-capped at 256 KiB by default**, and `--bytes` (the cap flag) isn't intuitively named — I tried `--limit_bytes` first based on the verb's internal error message. Looking at help: it *is* `--bytes`. The flag-name reuse with `read --bytes` is fine; the friction was that the diff truncation hint (post-ASH-121) says `--limit_bytes` is the raise-flag, but the *actual* flag name on the CLI is `--bytes`. Wait — let me re-check. Actually I conflated `--bytes` with `--limit_bytes`. The diff truncation hint says `or raise --limit_bytes` but the help shows `--bytes:int=262144`. Looking at [internal/verbs/git/diff.go:264](../../internal/verbs/git/diff.go#L264), the hint message uses `--limit_bytes` which doesn't match the flag name `--bytes`. **That's a stale-flag-name bug introduced by the ASH-80 rename pass.** Filing as ASH-followup.
- **`ash help --verb git | ash grep --path - --pattern diff`** errored with `file name too long` (stat tried to use the piped content as a path). The `--path -` form of `ash grep` doesn't accept stdin pipe input — but it doesn't error cleanly either. Worth a session note observation; not a ticket.

## Verification

- `make all` — clean build of `bin/ash`, `bin/ashd`, `bin/ashmcp`, plus `bin/ashvocab`, `bin/ashschema` on demand.
- `bin/ash test --timeout 300s` — 43/43 packages pass.
- `bin/ash help --verb edit` — confirms the new shape:
  ```
  --old:string <text|@file|-> — Exact text to find; must appear once unless all=true.
  --new:string <text|@file|-> — Replacement text; empty string deletes the match.
  ```
- `make vocab-check` — `vocab check: ok` after regeneration.
- `make schema-check` — `schema check: ok` after regeneration.
- `make validate-check` — `validate-check: ok` (no token-shape impact since hint strings unchanged).
- Functional confirmation: `bin/ash edit --path FILE --old @/tmp/old.txt --new @/tmp/new.txt` succeeds (re-tested mid-session to validate the `@PATH` form still works after my changes — it does).

## Follow-up tickets to consider

1. **`help --verbose` is a no-op for the client renderer** (see Friction #1 above). Two-shape fix; pick (a) wire-side Long or (b) daemon-side render. The Long descriptions today contain useful context the agent never sees.
2. **Stale `--limit_bytes` flag name in `git diff` truncation hint** (see Friction #2). One-line code fix; rename the user-visible flag in the hint to match the actual flag name `--bytes`. Probably 5-minute fix.

## Suggestions

- The PH-inline-in-signature pattern is the right shape for any future `placeholder` fields. If new verbs land with multi-form args (`--x <a|b|->`), set PH and let the renderer surface it. The Description should explain *semantics* (what the arg does, constraints), not *form options* (what shapes the value can take) — PH owns the latter.
- Two of today's friction items were *flag-name drift* and *Long-text invisible*. Both are signs that the help system is doing more work than its tests catch. A future "help golden" test (snapshot the rendered help output for each verb) would catch shape regressions cheaply. Out of scope for ASH-143.
