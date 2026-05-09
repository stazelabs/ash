# ASH-35: go-git as default git backend

**Task.** Wire `[git].backend = "go-git"` from the config substrate that ASH-61 landed, switch the default to `"go-git"`, and keep `"shellout"` as opt-in. Goal: zero dep on system git for the common workflows (an agent in a fresh container shouldn't need apt-get install git).

**Verbs used.** `ash read`, `ash grep`, `ash find`, `ash write`, `ash edit`, `ash test`, `ash git --op {status,log,diff,show}`, `ash stat`, `ash report`.

**Implementation.** Backend selector lives in [internal/verbs/git/backend.go](../../internal/verbs/git/backend.go). One package-level atomic variable, set once at daemon startup via `git.SetBackend(cfg.Git.Backend)`. Per-op dispatchers (`runStatus`, `runLog`, `runDiff`, `runShow`) check the active backend and call the matching shellout/gogit implementation. Existing `runStatus` etc. were renamed to `runStatusShellout` etc. (one-line edits in each file); the gogit halves live in `gogit_status.go`, `gogit_log.go`, `gogit_diff.go`, `gogit_show.go`.

**Coverage matrix shipped:**

| op            | range A..B | --staged    | default unstaged | --stat |
|---------------|------------|-------------|------------------|--------|
| status        | n/a        | n/a         | full             | n/a    |
| log           | full       | n/a         | n/a              | n/a    |
| diff          | full patch | counts only | counts only      | full   |
| show          | full patch | n/a         | n/a              | full   |

Working-tree patch text (`--staged` or default unstaged with patches) is the one place gogit is sub-shellout today. Producing unified-diff text for arbitrary content requires constructing custom go-git `FilePatch`/`Chunk` types and feeding `UnifiedEncoder` — non-trivial enough to warrant its own ticket. For now: counts only, document the divergence, send users to `[git].backend = "shellout"` if they need patch text on those modes.

Also-divergent (and documented):
- Log `--since "1 week ago"` (relative dates) — gogit accepts only Go-parseable absolute forms (RFC3339 / `"2006-01-02 15:04:05"` / `"2006-01-02"`). Implementing git's date parser is out of scope.
- Log `--author` — gogit does case-insensitive substring on `"Name <email>"` rather than git's regex. Sufficient for "filter by my name" use; non-trivial regexes will diverge.
- Status `--ignored true` — gogit's worktree status doesn't enumerate ignored files; returns empty list.

**Performance.** Smoke run on this repo (`bin/ash git ...`):

```
verb         backend    p50_exec
status       gogit      ~16ms
log -n 3     gogit      ~1ms
diff --stat  gogit      ~11ms
show --stat  gogit      ~8ms
```

ASH-35 originally cited shellout's p50 status at ~32ms (dominated by fork+exec); gogit halves that for status and is an order of magnitude faster for log on this repo size. Big-repo numbers will differ — go-git lacks git's index optimizations (untracked cache, fsmonitor, split index), so the comparison flips at some repo size we haven't measured. If users encounter a slow gogit on a large repo, `[git].backend = "shellout"` is the escape valve.

**Friction.**

- Shipped scope vs ideal scope. Implementing full unified-diff text from arbitrary content (sergi/go-diff line preprocessor + custom FilePatch + UnifiedEncoder) was tractable but ~200 LOC of careful hunk-formatting code in addition to what's already here. Decided to ship counts-only for working-tree modes and file a follow-up rather than rabbit-hole. Users who need patch text on unstaged opt to shellout.
- Index API import. go-git's per-entry index type lives at `plumbing/format/index.Entry`. I initially wrote local view types (`indexEntryView`, `idxLike`) thinking I'd avoid the import; turned out cleaner to just import `index` and use the real types.
- The `diff.FilePatch` interface I tried to use lives in `plumbing/format/diff` (formatter), not `utils/diff` (line-level differ). Cost a build cycle to discover.

**Suggestions.**

- ASH-66 (will file): full unified-diff text for gogit on `--staged` and unstaged modes. Implementation: per-file blob reads → custom FilePatch → UnifiedEncoder. ~200 LOC, well-bounded once attempted.
- Bench gogit vs shellout in [docs/bench.md](../bench.md) — answer "at what repo size does gogit become slower?" with hard numbers rather than the README hand-wave.
- An `ash kill` verb to restart the daemon cleanly. Three sessions in a row I've used `pkill -TERM ashd` to reload config or pick up a fresh build. The ergonomics suggest a verb is overdue.

**Instrumentation (this session).**

```
verb     n    ok%   p50_exec   p95_exec
write    ~30  100%  ~330us     ~600us
edit     ~12  100%  ~500us     ~800us
test     ~12  100%  ~480ms     ~3.2s   (full repo, 32 pkgs; 3.2s outlier was the gogit integration test that seeds a real git fixture)
git      ~6   100%  ~10ms      ~16ms   (gogit backend smoke)
```

End state: 32 packages pass, gogit is the default for the four live ops, shellout remains wired for the documented opt-out cases.
