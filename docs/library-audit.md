# Library reuse audit (ASH-103)

Outcome of the periodic audit: enumerate `go.mod`, check each
`internal/` package doing general-purpose work, and decide whether a
mature pure-Go library should replace or back it.

**Result: zero adoption tickets filed.** Every direct dep is load-bearing
and pure Go; every hand-rolled internal package is either thin glue,
ash-specific enough that a library would be overkill, or already on the
right library with explicit prior reasoning for the path not taken.

Documenting the "no change" calls here so the audit doesn't need to be
re-run from scratch the next time a verb ships. Re-audit when a verb
ships a new `internal/` package.

## Direct deps (all pure Go)

| dep | purpose |
|---|---|
| `bmatcuk/doublestar/v4` | glob with `**` semantics — walker `include`/`exclude` filters |
| `pkoukk/tiktoken-go` + `tiktoken-go-loader` | cl100k_base BPE tokenizer; bundled BPE table |
| `sabhiram/go-gitignore` | `.gitignore` pattern matching; wrapped by `internal/gitignore` |
| `vmihailenco/msgpack/v5` | request/response wire encoding |
| `modernc.org/sqlite` | transpiled pure-Go SQLite driver (`.ash/ledger.db`) |
| `BurntSushi/toml` | `ash.toml` decoding via `internal/config/load.go` |

Indirect deps worth knowing:

- `sergi/go-diff` — pulled by go-git. `internal/diff` does **not** use it.
- `dlclark/regexp2` — pulled by tiktoken-go; not used directly.
- All `go-git/*` + `modernc.org/*` chain — transitive support for the
  SQLite driver and the go-git backend.

No CGO anywhere. `make all` succeeds with `CGO_ENABLED=0` on a fresh tree.

## Decision table

| area | current | candidate considered | decision |
|---|---|---|---|
| gitignore parsing | `sabhiram/go-gitignore` + per-`.gitignore` cache (ASH-38) | `denormal/go-gitignore` | **no change** — cache is the perf-critical work, lives above the library |
| diff (LCS) | hand-rolled O(n·m) LCS, cap 4000 lines/side (ASH-32) | `sergi/go-diff` (Myers) | **no change** — cap bump covers ~2× more real-world files at ~30 MiB/~40 ms; Myers swap not worth the dep for the long tail today |
| SQLite driver | `modernc.org/sqlite` via stdlib `database/sql` | `mattn/go-sqlite3` (CGO) | **no change** — CGO forbidden by repo constraints |
| tokenizer | `pkoukk/tiktoken-go` | (none viable pure-Go) | **no change** — universal choice; confirmed in encoding measurement |
| TOML loader | `BurntSushi/toml` | `pelletier/go-toml/v2` | **no change** — de-facto standard, already wired with layered decoding |
| msgpack wire | `vmihailenco/msgpack/v5` | `tinylib/msgp` (codegen) | **no change** — reflection cost invisible on hot path; codegen buys compile-time pain for zero win |
| glob matching | `bmatcuk/doublestar/v4` | stdlib `filepath.Match` | **no change** — stdlib lacks `**` |
| directory walk | own filter pipeline atop `filepath.WalkDir` + doublestar + gitignore | bare stdlib | **no change** — pipeline composition is ash-specific (hidden prune → max-depth → gitignore → exclude → glob) |
| atomic file write | `internal/atomicwrite/` (temp + same-dir rename) | `google/renameio` | **no change** — 5 funcs; library would obscure deterministic naming `ash write` tests depend on |
| path jailing | `internal/jail/` (allow/deny by prefix, symlink-aware) | (none mature pure-Go) | **no change** — security-critical, ash-specific; rolling our own keeps the policy seam visible |
| Go AST | stdlib `go/ast`+`go/parser`+`go/token` | tree-sitter, ast-grep | **no change** — stdlib is exact, fast, avoids CGO/tree-sitter wall |
| ledger SQL | stdlib `database/sql` + modernc driver | an ORM | **no change** — one INSERT per call, two queries for `report`; ORM would be pure overhead |
| process running | `internal/runner/` over `os/exec` with truncation + timeout | shell-out library | **no change** — truncation + exit-code accounting is the reason it exists |
| MCP server | `modelcontextprotocol/go-sdk` (ASH-104, shipped) | (none other viable) | **adopted** — Anthropic-maintained, pure Go, JSON Schema 2020-12 dialect required by MCP |
| LSP client | `go.lsp.dev/protocol` (ASH-109, future) | (none other viable) | **adopt when ASH-109 ships** — mature protocol types; ash is a broker, not an implementer |
| bench orchestration | `internal/bench/` (hand-rolled timing + capture) | `golang.org/x/perf` | **no change** — `bench/` compares bash equivalent to ash verb; x/perf solves a different problem (Go benchmark series) |

## Findings worth keeping

1. **No CGO debt anywhere.** Every direct dep and every transitive dep
   in the binary is pure Go. The no-CGO constraint is binding without
   being painful — the ecosystem matured to the point where it's the
   default path.
2. **Hand-rolled work that *looked* replaceable is intentional.**
   `internal/diff` and `internal/atomicwrite` are both small and
   load-bearing; library swaps would either trade a known cost for
   unknown (sergi-go-diff in diff) or obscure ash-specific behavior
   (renameio in atomicwrite).
3. **The two "incoming" adoption decisions** (MCP server, LSP broker)
   pre-name their libraries on the ticket — this audit confirms those
   choices and adds nothing new.

## Conditions for re-evaluation

- **`internal/diff` exceeds 4000 lines** in real workloads (the ASH-32
  follow-up about a `--stat` cap-free fast path). Re-evaluate
  `sergi/go-diff` with fresh profile data — Myers-on-similar-input is
  meaningfully cheaper at scale and the dep is already indirect.
- **New `internal/` package ships.** Run a one-package audit using the
  decision-table shape above. The current list (15 dirs) is stable
  enough that incremental audits stay cheap.
