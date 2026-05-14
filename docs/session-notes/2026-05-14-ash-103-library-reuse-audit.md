# 2026-05-14 — ASH-103: library-reuse audit (pure-Go deps + adoption candidates)

## Task

Resolve ASH-103: enumerate `go.mod`, audit each `internal/` package doing
general-purpose work, and decide whether a mature pure-Go library should
replace or back it. Output: decision table + a Linear ticket per non-trivial
adoption decision. Adoption itself is out of scope — this ticket produces
*decisions*, not refactors.

## Verbs used

- `ash read`, `ash find`, `ash grep`

## TL;DR

Posture is clean. Every library currently in `go.mod` is pure-Go (no CGO).
Every hand-rolled internal package is either (a) thin glue, (b) ash-specific
enough that a library would be overkill, or (c) already on the right library
with explicit prior reasoning for any path-not-taken. The audit produced
**zero new adoption tickets** — every replacement candidate scrutinised has
either already happened, has an explicit prior "keep hand-rolled" decision,
or is covered by an existing forward-looking ticket ([[ASH-104]], [[ASH-109]]).

## Direct deps in `go.mod`

| dep | version | purpose |
| -- | -- | -- |
| `github.com/bmatcuk/doublestar/v4` | v4.10.0 | glob matching with `**` semantics — used by walker `include`/`exclude` filters |
| `github.com/pkoukk/tiktoken-go` | v0.1.8 | cl100k_base BPE tokenizer for honest token counts on every response |
| `github.com/pkoukk/tiktoken-go-loader` | v0.0.2 | bundles the BPE table so tiktoken-go can load without a network fetch |
| `github.com/sabhiram/go-gitignore` | 2021-09-23 | pattern matcher for `.gitignore` semantics; wrapped by `internal/gitignore` |
| `github.com/vmihailenco/msgpack/v5` | v5.4.1 | request/response wire encoding between client and daemon |
| `modernc.org/sqlite` | v1.50.0 | transpiled pure-Go SQLite driver — backs `.ash/ledger.db` |

Notable indirect deps that show up in scrutiny:

- `github.com/BurntSushi/toml` — direct dep of `internal/config` (declared via `load.go`), reported indirect only because the build graph also reaches it via go-git.
- `github.com/sergi/go-diff` — pulled by go-git. `internal/diff` does **not** use it; see decision row below.
- `github.com/dlclark/regexp2` — pulled by tiktoken-go for byte-pair regex; not used directly by ash.
- All `github.com/go-git/*` + `modernc.org/*` chain — transitive support for the SQLite driver and the go-git backend.

No CGO anywhere; `make all` succeeds with `CGO_ENABLED=0` on a fresh tree.

## Decision table

| area | current | candidate | decision | reason |
| -- | -- | -- | -- | -- |
| gitignore parsing | `internal/gitignore/` wraps `sabhiram/go-gitignore` with a per-`.gitignore` cache (see [[ASH-38]]) | `denormal/go-gitignore` | **no change** | already on `sabhiram`; the cache layer is the perf-critical work and lives above the library |
| diff (LCS) | `internal/diff/diff.go` — hand-rolled O(n·m) LCS + unified-diff formatter, cap 4000 lines/side | `sergi/go-diff` (Myers) | **no change** | explicit prior decision in [[ASH-32]] after profiling: cap bump 2000→4000 covers ~2× more files at ~30 MiB / ~40 ms, Myers swap not worth the dep for the long tail today |
| SQLite driver | `modernc.org/sqlite` via stdlib `database/sql` | `mattn/go-sqlite3` (CGO) | **no change** | CGO-bound drivers are forbidden by repo constraints; modernc is the pure-Go answer and already in use |
| tokenizer | `pkoukk/tiktoken-go` + `tiktoken-go-loader` | (none — only viable pure-Go cl100k impl) | **no change** | universal choice; encoding-substitution work in [[2026-05-13-encoding-substitution-measurement]] reaffirmed cl100k as the right counting target |
| TOML loader | `BurntSushi/toml` via `internal/config/load.go` | `pelletier/go-toml/v2` | **no change** | BurntSushi is the de-facto standard and already wired with layered decoding (global → project → env override); no perf or feature gap |
| msgpack wire | `vmihailenco/msgpack/v5` in `internal/proto/` | `tinylib/msgp` (codegen) | **no change** | reflection cost is invisible on the hot path; codegen would buy compile-time pain for no measurable win |
| glob matching | `bmatcuk/doublestar/v4` inside `internal/walker/` | stdlib `path/filepath.Match` | **no change** | stdlib lacks `**`; we need it for `**/*.go`-style includes — doublestar is the standard |
| directory walk | `internal/walker/` — own filter pipeline atop `filepath.WalkDir` + doublestar + gitignore wrapper | bare stdlib | **no change** | the pipeline (hidden prune → max-depth → gitignore → exclude → glob) is ash-specific; libraries can't compose this |
| atomic file write | `internal/atomicwrite/` — temp-file + same-dir rename | `google/renameio` | **no change** | trivial (5 funcs, ash-specific tmp-name convention); a library would obscure the deterministic naming we depend on for `ash write` tests |
| path jailing | `internal/jail/` — allow/deny by prefix, symlink-aware | (none mature pure-Go) | **no change** | security-critical and ash-specific; rolling our own keeps the policy seam visible |
| Go AST walking | stdlib `go/ast` + `go/parser` + `go/token` in `internal/vocab/` and `cmd/ashvocab/` | tree-sitter, ast-grep | **no change** | stdlib is exact, fast, and avoids the CGO / tree-sitter wall — confirmed correct in ticket scope |
| ledger SQLite usage | stdlib `database/sql` + modernc driver in `internal/ledger/` | an ORM (e.g. `gorm`) | **no change** | one `INSERT` per call, two queries for `report`; an ORM would be overhead with no leverage |
| process running | `internal/runner/` — wraps `os/exec` with truncation tracking + timeout | shell-out library | **no change** | the truncation + exit-code accounting is the reason it exists; libraries can't bolt this on |
| MCP server (incoming) | not yet built | `modelcontextprotocol/go-sdk` | **adopt when [[ASH-104]] ships** | Anthropic-maintained, pure Go, JSON-Schema 2020-12 dialect required by MCP — no daylight between "right library" and "what's in tree" |
| LSP client (incoming) | not yet built | `go.lsp.dev/protocol` | **adopt when [[ASH-109]] ships** | mature protocol types only; we are a *broker* over gopls/rust-analyzer, not an LSP implementer |
| Bench / runner orchestration | `internal/bench/` — hand-rolled timing + capture | `golang.org/x/perf` | **no change** | bench/ is a side-by-side harness for `bash equivalent` vs `ash verb`, not Go benchmarks; x/perf solves a different problem |

## Findings

1. **No CGO debt anywhere.** Every direct dep and every transitive dep that lands in the binary is pure Go. The no-CGO constraint is binding without being painful — the ecosystem has matured to the point where it is the default path.
2. **Hand-rolled work that *looked* replaceable is intentional.** `internal/diff` (cap-bounded LCS) and `internal/atomicwrite` (deterministic temp-file naming) are both small and load-bearing; library swaps would either trade a known cost for an unknown one (sergi/go-diff in `diff`) or obscure ash-specific behaviour (`renameio` in `atomicwrite`).
3. **The two "incoming" adoption decisions are already on the docket.** [[ASH-104]] (MCP server, `modelcontextprotocol/go-sdk`) and [[ASH-109]] (LSP broker, `go.lsp.dev/protocol`) already pre-name the libraries — this audit confirms those choices and adds nothing new.
4. **The audit found no non-trivial replacement decision worth filing.** Per the ticket's "verification" criterion, that means **zero follow-up tickets**. Documenting the "no change" calls here so the audit doesn't need to be re-run.

## Suggestions / follow-ups

- **Re-run this audit whenever a verb shipping introduces a new internal package.** The current package list (15 dirs under `internal/`) is stable enough that a re-audit is cheap; the value is keeping the table evergreen so future ships don't accumulate library-tax debt.
- **If `internal/diff` ever needs to handle files > 4000 lines** (e.g. the [[ASH-32]] follow-up about a `--stat` cap-free fast path), revisit the sergi/go-diff decision with fresh profile data — the Myers-on-similar-input case is meaningfully cheaper at scale and the dep is already an indirect.
- **No action on `go.mod` cleanup.** Every direct dep is load-bearing; every indirect is honest (real transitive use).

## Files touched

- `docs/session-notes/2026-05-14-ash-103-library-reuse-audit.md` (this file)
