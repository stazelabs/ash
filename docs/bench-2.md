# `ash bench` v2 — tracked, publishable benchmark harness (plan)

## Context

`ash bench` already exists ([../internal/verbs/bench/bench.go](../internal/verbs/bench/bench.go), 340 lines) and runs 16 canonical cases comparing ash vs bash on tokens, bytes, and latency. The output is in-memory only — there's no persistence, no provenance, no run-to-run comparison, no regression detection, and no coverage gate that fails when new verbs ship without bench cases. The v1 design doc at [bench.md](bench.md) explicitly lists `bench_runs` table for trend tracking as the #2 follow-up.

The project goal is to use `ash bench` as the substrate for the recursive-development experiment: every ash improvement should be visible as a δ in this measurement, and every regression should be visible the next time bench runs. v2 evolves it into a *consistent, reproducible, trackable, publishable* harness so improvements can be tracked over time and regressions caught.

This plan covers six phases: persistence, run stability, coverage, trends, publishable artifact, and Makefile glue. Each phase is independently shippable.

## Decisions already made

| Decision | Choice |
|---|---|
| Persistence DB | Same `.ash/ledger.db`, two new tables (`bench_runs`, `bench_case_results`). Existing `CREATE TABLE IF NOT EXISTS` startup pattern; no migration system. |
| Corpus strategy | Live repo. Record `repo_sha` per run for drift attribution. Δtok% is the apples-to-apples metric (both sides see the same content). |
| Publishable artifact | Checked-in `bench/baseline.json` + `bench/baseline.md`. Latency split into separate `bench/latency-snapshot.json` (machine-dependent, not contract). |
| `case_set_version` | SHA-256 hash of canonical case data (name + verb + sorted args), not file bytes — comments don't bump it. |
| `ash_commit_sha` source | Runtime `git rev-parse HEAD` via the existing in-process git backend. No `-ldflags` magic. |
| Trend & compare | Same `ash bench` verb, new flags (`--list`, `--compare`, `--baseline`). Avoids surface bloat. |
| `ash test` bench case | Exempt — no honest bash equivalent at the verb level. |
| Agent-session benchmarks | Out of scope; tracked as bench.md follow-up #5. |

---

## Phase 1 — Persistence + provenance

**Goal:** every `ash bench` invocation lands in the DB with enough metadata to reconstruct the run.

### 1.1 Schema (append to `internal/ledger/ledger.go` `schemaSQL`)

```sql
CREATE TABLE IF NOT EXISTS bench_runs (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    run_uuid            TEXT NOT NULL UNIQUE,
    ts                  INTEGER NOT NULL,             -- UnixNano
    ash_version         TEXT NOT NULL,
    ash_commit_sha      TEXT,
    case_set_version    TEXT NOT NULL,
    repo_sha            TEXT,
    repo_dirty          INTEGER NOT NULL DEFAULT 0,
    hostname            TEXT,
    cpu_count           INTEGER NOT NULL DEFAULT 0,
    daemon_uptime_us    INTEGER NOT NULL DEFAULT 0,
    repeat_n            INTEGER NOT NULL DEFAULT 1,
    warmup_n            INTEGER NOT NULL DEFAULT 0,
    notes               TEXT
);
CREATE INDEX IF NOT EXISTS idx_bench_runs_ts ON bench_runs(ts);

CREATE TABLE IF NOT EXISTS bench_case_results (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id                   INTEGER NOT NULL REFERENCES bench_runs(id) ON DELETE CASCADE,
    case_name                TEXT NOT NULL,
    verb                     TEXT NOT NULL,
    ash_tokens               INTEGER NOT NULL,
    bash_tokens              INTEGER NOT NULL,
    ash_bytes                INTEGER NOT NULL,
    bash_bytes               INTEGER NOT NULL,
    ash_latency_us_p50       INTEGER NOT NULL,
    ash_latency_us_min       INTEGER NOT NULL,
    bash_latency_us_p50      INTEGER NOT NULL,
    bash_latency_us_min      INTEGER NOT NULL,
    ash_ok                   INTEGER NOT NULL,
    ash_err                  TEXT,
    bash_exit                INTEGER NOT NULL DEFAULT 0,
    bash_run_err             TEXT,
    ash_truncated            INTEGER NOT NULL DEFAULT 0,
    bash_truncated           INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_bench_case_results_run  ON bench_case_results(run_id);
CREATE INDEX IF NOT EXISTS idx_bench_case_results_case ON bench_case_results(case_name);
```

The existing `Cleanup` does not touch `bench_*` tables; add a one-line carve-out comment so a future ledger-retention tweak does not accidentally include them.

### 1.2 New code

**[../internal/ledger/bench.go](../internal/ledger/bench.go) (new):**
- `type BenchRun struct { ... }` and `type BenchCaseResult struct { ... }` mirroring the schema.
- `func (l *Ledger) RecordBenchRun(run *BenchRun, results []BenchCaseResult) (int64, error)` — single transaction insert.
- `func (l *Ledger) QueryBenchRuns(limit int) ([]BenchRun, error)`
- `func (l *Ledger) QueryBenchRun(runUUID string) (*BenchRun, []BenchCaseResult, error)`
- `func (l *Ledger) QueryBenchBaseline(since time.Time) (map[string]BenchCaseResult, error)` — per-case median across runs since `since`. Used by Phase 4 `--baseline 7d`.

**[../internal/bench/version.go](../internal/bench/version.go) (new):**
- `func CaseSetVersion() string` — SHA-256 of canonical `Cases` data (name, verb, sorted (k,v) arg pairs), `sync.Once`-cached. Format: `cs-<first-8-hex>`.

**[../internal/bench/provenance.go](../internal/bench/provenance.go) (new):**
- `type Provenance struct { AshVersion, AshCommitSHA, CaseSetVersion, RepoSHA string; RepoDirty bool; Hostname string; CPUCount int; DaemonUptimeUs int64 }`
- `func CaptureProvenance(daemonStart time.Time, gitBackend git.Backend) Provenance` — uses the in-process git backend (already wired for `ash git`) for `RepoSHA` / `RepoDirty`. `os.Hostname`, `runtime.NumCPU` for the rest.

**[../internal/proto/version.go](../internal/proto/version.go) (new):**
- `const AshVersion = "0.1.0"` — manually bumped per ship for now. No build-time injection.

### 1.3 Wire-up

**[../internal/verbs/bench/bench.go](../internal/verbs/bench/bench.go) edits:**
- `Deps` gains: `Ledger *ledger.Ledger`, `DaemonStart time.Time`, `GitBackend git.Backend`.
- After the case loop in `RunWithDeps`, call `CaptureProvenance(...)` and `d.Ledger.RecordBenchRun(...)`. Best-effort — log on failure, do not fail the verb.
- Generate `run_uuid` once at the start with `crypto/rand` (16-byte hex, mirroring how sessions.id is generated elsewhere).
- Add `RunUUID` to the `Result` struct (msgpack-tagged) so callers see what was persisted.

**[../internal/verbs/verbs.go](../internal/verbs/verbs.go):**
- `Runners()` already takes `*ledger.Ledger`; thread it into the bench-runner closure.
- Add `daemonStart time.Time` and `gitBackend git.Backend` parameters to `Runners()` (these are needed by bench `Deps`).

**[../cmd/ashd/main.go](../cmd/ashd/main.go):**
- Capture `daemonStart := time.Now()` near the top of `main()`.
- Pass it (and the existing `gitBackend`) into the `verbs.Runners(...)` call.

### 1.4 Verification

```sh
make all
bin/ash bench --limit 2
sqlite3 .ash/ledger.db "SELECT id, run_uuid, ash_version, repo_sha, case_set_version FROM bench_runs ORDER BY id DESC LIMIT 1"
sqlite3 .ash/ledger.db "SELECT case_name, verb, ash_tokens, bash_tokens FROM bench_case_results WHERE run_id = (SELECT MAX(id) FROM bench_runs)"
bin/ash bench --format json | jq .   # existing fields present; add: run_uuid
bin/ash test --packages internal/verbs/bench    # existing test still passes
```

---

## Phase 2 — Run stability: `--repeat` and `--warmup`

**Goal:** smooth out wall-clock noise; persist p50 + min latency.

### 2.1 New args

In `Args`:
- `Repeat int` — measured iterations per case per side, default 1, max 50.
- `Warmup int` — unmeasured iterations per case per side, default `1` when `Repeat>1` else `0`, max 10.

Register in `ParseArgs` via `argutil.OptionalNonNegInt`. Add to the help schema in [../internal/verbs/help/help.go](../internal/verbs/help/help.go) bench entry.

### 2.2 Loop change

In `runCase`, refactor the single-shot ash and bash calls into helper functions `runAshOnce(d, c)` and `runBashOnce(c)` returning `sample{tokens, bytes, latencyUs int64}`. Then warmup-discard `a.Warmup` iterations, then collect `a.Repeat` measured samples. Tokens are deterministic given the same response — pin to `samples[0].tokens` for the persisted record. Compute p50 via `sort.Slice` + `math.Floor((n-1)*p)` index.

### 2.3 Asymmetry decision (documented, not normalized)

The bash subprocess pays fork+exec on every iteration; ash's in-process dispatch is hot. **Keep the asymmetry** — it mirrors what an agent actually pays in a real session. Warmup runs both sides to prime filesystem cache.

### 2.4 Backwards-compat

`Result.Cases` already has `AshLatencyUs` and `BashLatencyUs`. Add `AshLatencyUsP50`, `AshLatencyUsMin`, `BashLatencyUsP50`, `BashLatencyUsMin`. When `--repeat 1`, the new fields equal the existing ones. Old JSON consumers see no change.

### 2.5 Verification

```sh
bin/ash bench --case grep_heavy_func_internal --repeat 5 --warmup 2
sqlite3 .ash/ledger.db "SELECT ash_latency_us_p50, ash_latency_us_min FROM bench_case_results WHERE run_id = (SELECT MAX(id) FROM bench_runs)"
```

---

## Phase 3 — Coverage gate + new bench cases

**Goal:** the case list grows with the verb surface; the test suite fails when a measurable verb has no case.

### 3.1 Verb classification

Add to [../internal/bench/cases.go](../internal/bench/cases.go):

```go
var MeasuredVerbs = []string{"read", "write", "edit", "diff", "find", "grep", "git", "stat"}
var ExemptVerbs = map[string]string{
    "metrics": "reads ledger; size depends on session, no honest bash equivalent",
    "report":  "reads ledger; size depends on session",
    "help":    "static schema render; trivially small",
    "init":    "one-shot setup; mutates files",
    "uninit":  "one-shot teardown; mutates files",
    "stop":    "kills daemon",
    "hook":    "the redirector under test; circular",
    "bench":   "recursive",
    "test":    "no honest bash equivalent at the verb level",
}
```

### 3.2 Coverage tests

**[../internal/verbs/bench/coverage_test.go](../internal/verbs/bench/coverage_test.go) (new)** — placed here, not in `internal/bench`, to avoid the cycle (`internal/verbs` already imports `internal/verbs/bench` which imports `internal/bench`):

- `TestEveryMeasuredVerbHasACase` — counts cases by verb; fails if any `MeasuredVerb` is missing.
- `TestEveryRegisteredVerbIsClassified` — every verb in `verbs.PrettyHandlers()` must appear in either `MeasuredVerbs` or `ExemptVerbs`. Forces explicit acknowledgement when a new verb ships.
- `TestNoStaleExemptions` — every key in `ExemptVerbs` must be a real registered verb.

### 3.3 New cases (append to [../internal/bench/cases.go](../internal/bench/cases.go))

`Case` struct gains `Setup func() error // optional` for cases that mutate the working tree.

- `write_small` — write `.ash/bench-tmp/write_small.txt`. Bash: `sh -c "cat > FILE << 'EOF'…"`.
- `edit_string_replace` — replace `FOO` → `BAR` in a setup-prepared `.ash/bench-tmp/edit_target.txt`. Bash: `sed -i.bak`.
- `diff_two_files` — `diff README.md CLAUDE.md` (both sides).
- `diff_stat_only` — `--stat true`; bash equivalent stays as the unified diff (no clean stat-only mapping). Win is structural.

`.ash/bench-tmp/` is created by `ensureBenchTmpDir` and torn down after all cases finish.

### 3.4 Bash translation extensions

Extend [../internal/bench/translate.go](../internal/bench/translate.go) for `write`/`edit`/`diff`. Extend [../internal/bench/runner.go](../internal/bench/runner.go) to accept `["sh", "-c", "<script>"]` argv (the honest agent form for redirect-write idioms — mirrors what the hook denies under `Bash:redirect-write`).

### 3.5 Verification

```sh
bin/ash test --packages internal/verbs/bench
bin/ash bench --verb write
bin/ash bench --verb edit
bin/ash bench --verb diff
ls .ash/bench-tmp/   # cleaned up
```

---

## Phase 4 — Trend tooling

**Goal:** answer "did this change make ash better or worse?" without leaving the CLI.

### 4.1 New flags on `ash bench`

In `Args`: `List bool`, `ListLimit int` (default 20), `CompareA string`, `CompareB string`, `Baseline string`, `RegressTok float64` (default 0.10), `RegressLat float64` (default 0.20). Special tokens for compare: `latest` = most recent run, `baseline` = read from `bench/baseline.json`.

Dispatch in `RunWithDeps`:
```go
switch {
case a.List:           return runList(d, a)
case a.CompareA != "": return runCompare(d, a)
case a.Baseline != "": return runWithBaseline(d, a)
default:               return runStandard(d, a)
}
```

### 4.2 Result types

`RunSummary` (run_uuid + ash_version + ash_commit_sha + case_set_version + repo_sha + ts + cases + ash_total + bash_total + delta_pct), `ListResult{Runs []RunSummary}`, `CompareResult` (A, B, PerCase, Regressions, Improvements, CaseSetMatch), `CaseDelta` (per-case A/B numbers + Δ% + flags).

### 4.3 Regression rule

A regression on either tokens or latency counts. Improvement requires *both* (the Hippocratic rule — token wins that cost latency are still suspect). `OnlyInA` / `OnlyInB` are neither.

### 4.4 Pretty rendering

Borrow the layout from existing `PrettyResponse`. Side-by-side per-case table with Δtok% and Δlat% columns, regression/improvement count footer, threshold banner.

### 4.5 Verification

```sh
bin/ash bench
bin/ash bench
bin/ash bench --list --list-limit 5
bin/ash bench --compare <uuid_A>,latest
bin/ash bench --baseline 7d
bin/ash bench --baseline 7d --regress-tokens 0.05
```

---

## Phase 5 — Publishable artifact

**Goal:** a checked-in baseline that shows up in PR diffs.

### 5.1 Files

```
bench/
  baseline.json              # tokens-only — the regression contract
  baseline.md                # human-readable form
  latency-snapshot.json      # latency, machine-tagged — informational only
```

`baseline.json` schema: `{schema, ts, ash_version, ash_commit_sha, case_set_version, repo_sha, cases:[{name, verb, ash_tokens, bash_tokens, ash_truncated, bash_truncated}], summary:{n_cases, ash_tokens_total, bash_tokens_total, delta_tok_pct}}`.

`latency-snapshot.json` adds `hostname`, `cpu_count`, and per-case `ash_us_p50/min`, `bash_us_p50/min`. *Not* the source of truth for regression CI.

### 5.2 New flags

- `RecordBaseline bool` — `--record-baseline`. After a normal bench, write `bench/baseline.json` and `bench/latency-snapshot.json`.
- `ExportMd bool` — `--export-md`. Emit Markdown to stdout (intended to be redirected to `bench/baseline.md`).

### 5.3 Code

- [../internal/verbs/bench/baseline.go](../internal/verbs/bench/baseline.go) (new) — JSON schema structs; `writeBaseline(res, prov, projectRoot)`.
- [../internal/verbs/bench/markdown.go](../internal/verbs/bench/markdown.go) (new) — `exportMarkdown(res, prov)`.

`projectRoot` comes from the daemon's existing project-root discovery.

### 5.4 Compare against the checked-in baseline

`--compare baseline,latest` — when `CompareA == "baseline"`, read `bench/baseline.json` instead of querying the DB. Lets the user diff their working state against the contract.

### 5.5 Verification

```sh
bin/ash bench --record-baseline
ls bench/                     # baseline.json, latency-snapshot.json
bin/ash bench --export-md > bench/baseline.md
git diff bench/
bin/ash bench --compare baseline,latest
```

---

## Phase 6 — Makefile glue

Add to [../Makefile](../Makefile):

```makefile
.PHONY: bench bench-baseline

bench: bin/ash
	@mkdir -p bench
	./bin/ash bench --repeat 5 --warmup 2 --format json > bench/latest.json
	@echo "wrote bench/latest.json"

bench-baseline: bin/ash
	./bin/ash bench --repeat 5 --warmup 2 --record-baseline
	./bin/ash bench --export-md > bench/baseline.md
	@echo "regenerated bench/baseline.json and bench/baseline.md"
	@echo "review the diff: git diff bench/"
```

Add `/bench/latest.json` to [../.gitignore](../.gitignore).

---

## Documentation updates

**[bench.md](bench.md)** — append (don't rewrite) date-stamped sections matching the existing "First optimization round" / "Heavy-tree case" style:
1. *Persistence (date)* — new tables, `case_set_version`, runtime provenance capture.
2. *Run stability (date)* — `--repeat`/`--warmup`, the bash fork+exec asymmetry decision, noise sources.
3. *Coverage gate (date)* — `MeasuredVerbs`/`ExemptVerbs`, the rule.
4. *Trend tooling (date)* — `--list`, `--compare`, `--baseline`, regression thresholds.
5. *Publishable baseline (date)* — `bench/baseline.json` contract, latency split.

**[../CLAUDE.md](../CLAUDE.md)** — extend the "When to prefer ash over bash" checklist: `make bench` is the canonical publishable run; `ash bench --baseline 7d` is the canonical "is anything regressing?" check.

**[../README.md](../README.md)** — add a brief "Benchmarks" section pointing at `bench/baseline.md`.

---

## Critical files (full inventory)

**New:**
- [../internal/ledger/bench.go](../internal/ledger/bench.go) — `BenchRun`/`BenchCaseResult` + query helpers
- [../internal/bench/version.go](../internal/bench/version.go) — `CaseSetVersion()`
- [../internal/bench/provenance.go](../internal/bench/provenance.go) — `CaptureProvenance()`
- [../internal/proto/version.go](../internal/proto/version.go) — `AshVersion` const
- [../internal/verbs/bench/baseline.go](../internal/verbs/bench/baseline.go) — `--record-baseline` writer
- [../internal/verbs/bench/markdown.go](../internal/verbs/bench/markdown.go) — `--export-md` renderer
- [../internal/verbs/bench/coverage_test.go](../internal/verbs/bench/coverage_test.go) — three coverage tests
- bench/baseline.json, bench/baseline.md, bench/latency-snapshot.json (generated, checked in)

**Edited:**
- [../internal/ledger/ledger.go](../internal/ledger/ledger.go) — schema additions
- [../internal/bench/cases.go](../internal/bench/cases.go) — `MeasuredVerbs`, `ExemptVerbs`, new cases for write/edit/diff, `Setup` field on `Case`
- [../internal/bench/translate.go](../internal/bench/translate.go) — bash mappings for write/edit/diff
- [../internal/bench/runner.go](../internal/bench/runner.go) — accept `["sh", "-c", "…"]` argv form
- [../internal/verbs/bench/bench.go](../internal/verbs/bench/bench.go) — `Repeat`/`Warmup`/`List`/`Compare*`/`Baseline`/`RecordBaseline`/`ExportMd` args, persistence call, dispatch switch, new pretty renderers
- [../internal/verbs/verbs.go](../internal/verbs/verbs.go) — thread `*ledger.Ledger`, `daemonStart`, `gitBackend` into bench Deps
- [../internal/verbs/help/help.go](../internal/verbs/help/help.go) — new bench arg schemas
- [../cmd/ashd/main.go](../cmd/ashd/main.go) — capture `daemonStart`
- [../Makefile](../Makefile) — `bench` and `bench-baseline` targets
- [../.gitignore](../.gitignore) — `/bench/latest.json`
- [bench.md](bench.md), [../CLAUDE.md](../CLAUDE.md), [../README.md](../README.md)

---

## End-to-end verification (after all phases)

```sh
make all
bin/ash test --packages ./...                # all tests pass, coverage gate green
make bench-baseline
git diff bench/                              # baseline.json + baseline.md + latency-snapshot.json land cleanly
bin/ash bench --list --list-limit 5
bin/ash bench --compare baseline,latest      # zero regressions on a no-op change

# Now mutate something measurable, e.g. add a token to find pretty form,
# rebuild, re-run bench, and confirm find_* cases are flagged as regressions.
# Revert the change.

bin/ash bench --baseline 7d                  # rolling baseline; reads from QueryBenchBaseline
sqlite3 .ash/ledger.db "SELECT count(*) FROM bench_runs"
```
