# ASH-147 — trim the `help` MCP envelope (`Long` gated on `--verbose`)

**Task.** [ASH-129](https://linear.app/stazelabs/issue/ASH-129) showed `help` over MCP was the dominant outlier in [docs/mcp/wire-cost.md](../mcp/wire-cost.md) (+1613% Δ claude, 17× CLI). The root cause: [ASH-144](https://linear.app/stazelabs/issue/ASH-144) changed the `Long` msgpack tag from `-` to `long,omitempty` so the CLI `--verbose true` flag could finally surface per-arg long descriptions — correct fix for the CLI, but it also added `Long` (often several sentences per arg) to the MCP envelope, where harnesses pay tokens for it regardless of whether they ever render it. ASH-147 gates `Long` on `Args.Verbose` at `help.Run` so the wire shape matches the consumer's stated intent.

## Verbs used

`ash read`, `ash find`, `ash grep`, `ash git --op log/status/diff`, `ash edit` (with `@PATH` for the multi-line replacements), `ash write`, `ash test --packages …`, then `bin/wirecmp -claude -out`. `make vocab` + `make vocab-check` + `make schema-check` + `make validate-check` + `make all`.

## Design choice — gate at Run, not at the transport

Three candidate seams were on the table when the ticket was filed:

1. **Strip in the daemon's MCP dispatch path.** Branch on `Request.Transport == "mcp"`. Rejected: leaks MCP awareness into the daemon. Tax 2 (sibling [ASH-146](https://linear.app/stazelabs/issue/ASH-146)) is the right place to design a generic transport-aware projection seam; doing it inline for `help` would build a one-off branch we'd rip out later.
2. **Add a generic `MCPProject` step to `proto.MCPEnvelope`.** Rejected for the same reason — premature design that overlaps with ASH-146. Also doesn't compose well: `MCPEnvelope` operates on the encoded `rsp.Data` and would need verb-specific knowledge to know what to drop.
3. **Gate `Long` at `help.Run` on `Args.Verbose`.** Picked. The semantics are already there: `Long` exists *for* verbose consumers. If the request didn't ask for verbose, the verb shouldn't put `Long` on the wire — for *any* transport. CLI default, MCP default, and any future transport all collapse to the same shape. No transport-specific branches.

(3) is also strictly cleaner than the pre-ASH-144 state: instead of "Long is always present locally but never on the wire" (the silent bug ASH-144 fixed), we now have "Long is on the wire only when the caller asked for it" (matches the user-visible knob).

The implementation is small — two helpers (`verbWithoutLong`, `registryWithoutLong`) that shallow-copy the Args slice with `Long` cleared per `ArgSchema`. Package-level `registry` is untouched (anything that reaches into `help.Registry()` directly — `internal/mcpschema`, `internal/vocab` — still sees the full data, which is what they want).

## Numbers

`bin/wirecmp -claude -repeat 5`. Only the `help` row changes; the others are unaffected (no transport-level work in this ticket).

| metric | post-ASH-124 | post-ASH-147 | Δ |
|---|---:|---:|---:|
| `help` MCP bytes | 29680 | 17505 | -41% |
| `help` MCP cl100k | 6939 | 4076 | -41% |
| `help` MCP claude | 7933 | 4696 | -41% |
| `help` Δ vs CLI (claude) | +1613% | +914% | 17× → 10× |
| aggregate MCP claude | 9366 | 6173 | -34% |
| aggregate Δ vs CLI (claude) | +429% | +243% | |

The "halve it" target in the ticket was beat at 41% off. The aggregate also drops dramatically because `help` so dominates it — but ASH-147 is a targeted projection, not a fix for the underlying tax 2 (named-field cost), which is still there for every other arg field (`name`, `type`, `default`, `description`, `PH`, `mode`, `op`, `values`) and for every list-of-records verb. ASH-146 owns that closure.

## Test surface

`TestVerboseSurfacesLong` (the [ASH-144](https://linear.app/stazelabs/issue/ASH-144) regression guard) was strengthened to assert the *wire-shape* gate, not just the pretty-render gate. Pre-ASH-147 the test called `Run` once with `Verbose=false` and then re-rendered the same Result with two different `req.Args` — which worked because both Long and Description were always on the wire. Post-ASH-147 that pattern is wrong: the wire shape now depends on `Args.Verbose`, not just the request's verbose flag at render time. The new test runs `Run` twice (once with `Verbose=false`, once with `Verbose=true`), asserts the Result Long fields are empty/non-empty respectively, and then verifies the pretty rendering on each.

This catches the right class of bug: if a future change re-attaches `Long` to the wire unconditionally, the wire-shape assertion fires immediately rather than waiting for someone to notice the help row inflated in [docs/mcp/wire-cost.md](../mcp/wire-cost.md) again.

## Friction

- **Hook + harness `Edit` interaction was rough.** The harness's `Edit` tool requires a prior `Read`, but `Read` is denied (correctly) by the PreToolUse hook. Net effect: `ash edit @PATH` was the only path, which meant writing the before/after blocks to `/tmp/*.txt` (via `ash write`) and then invoking `ash edit --old @/tmp/x --new @/tmp/y`. Works, but four invocations for one multi-line replacement. The memory entry [ash-edit-atpath-form](../../../.claude/projects/-Users-cstaszak-Stazelabs-projects-ash/memory/ash-edit-atpath-form.md) flagged this pattern; the friction is real and is the natural shape until something like an `ash patch` verb lands.
- **`ash git --op diff` byte cap on inventory.json.** A 10-line diff that's actually ~10 line-number shifts in a one-line JSON file blew past the default 256 KiB cap even with `--bytes 1048576`. Workaround: `git stash push <path>` + `git stash show -p` to inspect the patch. ash should probably special-case JSON files (or any file where the post-context exceeds the cap) by truncating context rather than dropping the whole patch — not in scope here, but worth a session note.

## Surprises

- The non-`help` rows in the wirecmp run drifted slightly (`git status` row: 36% → 37% Δ vs CLI). The drift is the `git status` workload itself (this run captured the ASH-147 in-flight edits on the working tree). Footnoted in [docs/mcp/wire-cost.md](../mcp/wire-cost.md).
- Vocab inventory.json regenerated with 10/10 changes — all of them `"line"` field shifts in `sites` references because the help.go change added comment lines. Token-shape (literals, counts) unchanged. `make vocab-check` failed on the byte-diff, `make vocab` regenerated cleanly. Expected — vocab artifacts pin source-site lines and will shift on any file edit in tracked files. Not a real drift.

## Files changed

- `internal/verbs/help/help.go` — `Run` strips `Long` when `Verbose=false`; new helpers `verbWithoutLong` and `registryWithoutLong`.
- `internal/verbs/help/help_test.go` — `TestVerboseSurfacesLong` rewritten to assert the wire shape, not just the render.
- `docs/vocab/inventory.json` — site-line-number shifts (auto-regenerated by `make vocab`).
- `docs/mcp/wire-cost.md` — new "Latest snapshot (post-ASH-147)" section, "Pre-vs-post ASH-147 deltas (`help`)" sub-table, refreshed narrative pointing at sibling tickets [ASH-146](https://linear.app/stazelabs/issue/ASH-146) and [ASH-148](https://linear.app/stazelabs/issue/ASH-148).
- `docs/session-notes/2026-05-15-ash-147-help-mcp-long-gate.md` — this note.

`docs/mcp/tools.json` and `cmd/ashmcp/tools.json` unchanged (the MCP tool *schema* artifact never used `Long` — that's `Description`-only — so the schema is unaffected, confirmed via `make schema-check`).

43/43 packages pass; `vocab-check`, `schema-check`, `validate-check` all green.
