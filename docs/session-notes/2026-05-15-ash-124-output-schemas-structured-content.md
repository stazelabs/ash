# ASH-124 — outputSchema + StructuredContent

**Task.** Drop the per-call JSON wrapper tax measured in ASH-123, give MCP harnesses an `outputSchema` so they can validate structured output, and route the metrics envelope into MCP `_meta` so it doesn't count against the model.

**Verbs used.** `ash read`, `ash grep`, `ash find`, `ash edit`, `ash write`, `ash test`, `ash help`, `ash git`. One bash escape: `make schema` / `make vocab-check` / `make validate-check` (Makefile orchestration is still bash).

## What shipped

**AST-driven outputSchema generation ([`internal/mcpschema/output.go`](../../internal/mcpschema/output.go)).** New file that parses `internal/verbs/<pkg>/` with `go/parser`, collects every `type X struct` declaration in the package, and recursively turns the `Result` struct (plus the types it references) into a JSON Schema draft 2020-12 node tree. Handles primitives (string/int/float/bool), slices, pointers, maps, same-package struct references, and one hand-baked external reference (`proto.TruncInfo`). `omitempty` on a msgpack tag drops the field from `required[]`; `msgpack:"-"` skips the field entirely. Field descriptions come from the trailing `// comment` (preferred) or the doc comment above. Same approach as `cmd/ashvocab` per the ticket.

**Generator wiring.** `mcpschema.Generate` grew a `repoRoot` parameter that the AST walker uses to find verb sources. `cmd/ashschema/gen.go` passes `os.Getwd()` (Makefile invokes from repo root). Tests resolve the root via `runtime.Caller(0)` so they work regardless of `go test`'s CWD. `Tool` gained an optional `OutputSchema *OutputSchema` field; the JSON omits it when nil (no drift for the no-source-access test path).

**Envelope shape ([`internal/proto/mcpenv.go`](../../internal/proto/mcpenv.go)).** `proto.MCPEnvelope` used to return `{"ok":true,"data":...,"metrics":...}`. Now it returns just `rsp.Data` re-marshaled as JSON on success, or `"<code>: <msg>"` on failure — no wrapper. New sibling helper `proto.MCPStructuredData` decodes `rsp.Data` into a generic `any` for use as `CallToolResult.StructuredContent`. Both ashmcp (for emit) and ashd (for ledger accounting) call `MCPEnvelope` so the byte count stays single-sourced.

**ashmcp dispatch ([`cmd/ashmcp/dispatch.go`](../../cmd/ashmcp/dispatch.go)).** `toolResult` now dual-emits: `StructuredContent = decoded data map` (when the data marshals to a top-level object — every verb's `Result` does), plus a `TextContent` carrying the same data JSON (so harnesses that ignore `structuredContent` still get the payload, not a thin sentinel). Metrics move to `Meta = {"ash": {"metrics": ...}}` so they ride as MCP-reserved metadata rather than tokens. Errors keep `IsError=true` + `TextContent("<code>: <msg>")`.

**ashmcp registration ([`cmd/ashmcp/main.go`](../../cmd/ashmcp/main.go)).** `embeddedTool` gained an `outputSchema` field (raw JSON, omitempty); when present the server forwards it via `mcp.Tool.OutputSchema`. The eight v1 verbs all have schemas; `tools/list` advertises them.

## Verification

- `make schema` regenerates `docs/mcp/tools.json` (+ the embed mirror) with `outputSchema` for **all 17 verbs**, well past the "at least 8 v1 verbs" bar.
- `make schema-check`: ok (drift gate still passes).
- `make vocab-check`: ok (no new agent-facing strings — envelope removal *drops* the `{"ok":...,"metrics":...}` chrome, but those literals were never in the vocab inventory since they're JSON keys, not pretty headers).
- `make validate-check`: ok (no substitution-rule sign-flips).
- `bin/ash test --timeout 300s`: 40 packages, 40 pass. `cmd/ashschema` (schema-check generator), `internal/mcpschema` (new AST tests via the live registry), `cmd/ashmcp` (`TestEmbedMatchesCanonical`, `TestServerRegistersTools`), `internal/proto` (envelope helper) all green.
- `mcp__ash__ash_git --op status`: clean before commit.

## Friction / Suggestions

- **The hook redirected harness `Edit` → `ash edit` and harness `Read` → `ash read`** while editing existing files. `ash edit` with `--old`/`--new` worked fine for surgical changes (one-shot string replace), but the bash shell needs `\`` escapes for backticked struct tags, which got noisy. Not a verb gap — just a reminder that complex multi-line Go struct edits benefit from `--old -` / `--new -` stdin mode rather than command-line args.
- **Generator schema for `additionalProperties: false` at every level** could surprise harnesses that pass extra fields back (none should, since the daemon controls the payload shape). Verified against the live registry; no verb's `Result` adds dynamic keys outside `map[string]V` fields, which we model as `additionalProperties: true`.
- **One pre-baked external reference (`proto.TruncInfo`).** When future verbs reference more external types we'll either expand `externalSchemas` or fold a tiny cross-package resolver in. Current state is intentional — the universe is small and the resolver would cost more lines than it saves.
- **`make validate` ran clean.** `.env.local` carries the key (gitignored; `.env.local` wins over `.env` per the dotenv convention documented in CLAUDE.md, which I overlooked at first). 80 rows in `testdata/validate_results.md`, no `✗` markers — every substitution rule's cl100k Δ agrees in sign with the Claude Δ across every fixture. The post-ASH-124 envelope shape doesn't perturb any rule's cross-tokenizer agreement, which is the load-bearing invariant. **Note for future agents: this repo HAS an API key configured. Don't assume `make validate` is gated out — try it.**

## Token impact (predicted)

Per the ASH-123 wire-cost measurement, the old envelope wrapper was ~30-50 cl100k tokens per call. ASH-124 drops it cleanly: success is now `rsp.Data` JSON only, and metrics move to `_meta` (out of band, not counted by cooperating harnesses).

For terse responses (find/grep/stat with <50 tokens of payload), the wrapper was the majority of the cost — these should see the biggest relative wins. The structured-content path additionally lets harnesses skip the `JSON.parse(text)` round-trip, which doesn't show in tokens but matters for harness latency.

Re-running `cmd/wirecmp` after this change will quantify the delta against the ASH-123 baseline; not done in this session because the harness target was the ticket's primary deliverable.

## Instrumentation

No ledger queries this session — schema work doesn't surface in `ash report`. The test suite's 40-package pass + the byte-equal `make schema-check` are the load-bearing checks.
