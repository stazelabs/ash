# Agent guidance — the doc surfaces that drive ash usage

> Status: design landed alongside the README rewrite (2026-05-09) and the CLAUDE.md cleanup that introduced the three-tier model. This doc captures the rationale so the next maintainer can judge edge cases without re-deriving the framework.

## Context

`ash` is a recursive-development experiment: the project's value depends on agents *actually using* `ash` for their daily operations so the ledger captures real friction data, which feeds the next ship's design. This means the docs that nudge agents into `ash` are not ornamental — they're load-bearing infrastructure for the experiment.

When the project shipped its first verbs, all agent guidance lived in a single [CLAUDE.md](../CLAUDE.md). As the surface grew (17 verbs, 5 config sections, ledger retention, jail policy, cross-repo report), CLAUDE.md accumulated three classes of content:

1. **Operational guidance for agents working in this repo** (what to use, when, with what gotchas) — its actual job.
2. **Verb-manual content** duplicating `ash help --verb <name>` — drift-prone bloat.
3. **Design-principle prose and configuration spec** duplicating the README — the wrong audience for that surface.

The cleanup that landed alongside this doc pulled CLAUDE.md back to (1) and split (2) and (3) onto their proper surfaces. This doc explains the model so the split stays clean.

## The three-tier model

Three surfaces share the agent-facing load. They have non-overlapping owners:

| Surface | Audience | Owns |
|---|---|---|
| [README.md](../README.md) | Humans evaluating ash | Pitch, install, verb surface index, design principles, configuration spec, examples, roadmap |
| [CLAUDE.md](../CLAUDE.md) | Agents working *in this repo* | Switch criteria, hard-won gotchas, session feedback ritual, memory hygiene, bash whitelist |
| `ash help [--verb <name>]` | Agents needing per-verb args | Authoritative arg schema (defaults, types, descriptions) — never duplicated in markdown |
| `docs/<topic>.md` | Designers and maintainers | Per-feature design depth (configuration, install, bench, PreToolUse, ledger-cleanup, this doc) |
| Hook denial messages | Agents in any `ash init`'d repo | The minimum self-contained nudge to use ash |

The split is not just organizational — each surface has a different *failure mode* if it bloats:

- **README bloat** turns away prospective users at the install gate.
- **CLAUDE.md bloat** buries the load-bearing wisdom under reference-material noise; agents skim and miss the gotcha they needed.
- **`ash help` bloat** is impossible by construction — it's generated from the verb schema.
- **`docs/<topic>.md` bloat** is fine; that's where depth belongs.

The non-obvious failure mode is **README/CLAUDE.md duplication**: when both copies say the same thing, the maintainer who edits one but forgets the other introduces silent drift. The agent reading CLAUDE.md sees a different reality than the human reading README, and neither knows.

## Why agent docs are load-bearing in ash specifically

For a normal CLI, "agent docs" are nice-to-have. For `ash`, they're the lever the experiment depends on:

1. **The recursive-dev premise depends on agents using ash.** No `ash` use → no ledger data → no friction signal → no next-ship design.
2. **The harness's defaults pull agents *off* `ash`.** Out of the box, Claude Code prefers its built-in `Grep`/`Glob`/`Read`/`Edit`/`Write` tools. `ash` only wins when the doc surface (and the hook) actively redirect.
3. **Friction is the product.** CLAUDE.md exists to make friction visible, not to hide it. Workarounds it documents should be transparent ("here's what to do until verb X ships") rather than smoothing over a bug.

This is why CLAUDE.md needs more care than e.g. a `CONTRIBUTING.md` — it's the entry point for the agent loop that drives everything else.

## Drift management

Today's most common drift modes, in order of frequency:

- **Verb count and ship number** — every ship updates the count somewhere; some places get missed (ASH-56 swept several mismatches at once).
- **Per-flag args** — a new flag ships in `internal/verbs/<verb>/<verb>.go` and updates the help schema; the CLAUDE.md example block doesn't mention it. The audit before this cleanup found four such gaps (`grep --no_text`, `stat --with_meta`, `test --short`, `report --top`).
- **Notation drift** — early `git diff <flag>` examples linger after the verb moved to `git --op diff <flag>`.
- **Stale references** — `ash kill` mentioned after the verb was renamed to `ash stop` (ASH-67).

The principle: **if a fact can be derived from `ash help` or `git log`, don't duplicate it; reference.** Concrete applications:

- CLAUDE.md's per-verb sections cite **one canonical example** + a pointer to `ash help --verb <name>`. The full arg list lives in help.
- README's verb table is **one row per verb**, no per-arg detail. Detail belongs in `ash help`.
- Configuration schema lives in [`ash.toml.example`](../ash.toml.example) and README. CLAUDE.md points to both.
- Constraints and design principles live in README. CLAUDE.md points there.

Future extension worth considering (not in scope for this round): a CI check that asserts CLAUDE.md's verb-count claim matches `ash help`, like ASH-42 did for `ash usage`. File a ticket if drift recurs after this cleanup.

## What counts as load-bearing wisdom

CLAUDE.md should document a fact only when *all three* are true:

1. **Born from observable session friction.** Born from a real session and documented in commit messages, Linear tickets, or a postmortem comment in the source. Not theoretical.
2. **Not derivable from source or `ash help`.** A note that "`grep` has a `--fixed_string` flag" is reference material; help has it. A note that "shell quoting silently corrupts `--old_string`" is wisdom; you can't infer it from the help schema.
3. **Actionable by an agent without reading additional docs.** "Use stdin via `--content -`" is actionable. "There's a class of issues with shell quoting" is not — it's punting.

Examples of load-bearing entries that earned their place:

- Shell quoting on `ash edit`/`ash write` (ASH-48, ASH-60, ASH-63)
- Daemon stickiness — config changes need explicit restart (ASH-49, ASH-61)
- Path-form semantics differ across verbs (ASH-33 sweep + ship-5-git-status note)
- `ash read --range` start vs. end clamping (ASH-57)
- Hook redirects `cat`/`echo`/`printf`/`tee` + `>` to `ash write` (ASH-69)

A fact that fails any of the three tests belongs somewhere else: source comments, design docs, or just the help schema.

## The target-repo gap

Hook denial messages tell agents "See CLAUDE.md 'When to prefer ash over bash'." But a repo that ran `ash init` does **not** receive a CLAUDE.md from `ash init` — only the hook config, the gitignore line, and the registry entry (see [internal/verbs/initverb/initverb.go](../internal/verbs/initverb/initverb.go)).

This is a real UX gap. An agent dropped into a target repo for the first time sees a stream of hook denials with embedded suggestions but no operational doc framing *why* they should switch or *what* the gotchas are. The hook's deny message is a nudge, not an onboarding.

The chosen direction (filed as a follow-up Linear ticket in the ash team): `ash init` embeds a generic agent-guidance template in the binary and writes (or merges into) the target repo's CLAUDE.md/AGENTS.md. The template covers:

- The switch criteria (verbs to prefer)
- Shell-quoting wisdom (load-bearing footgun)
- The session-feedback ritual location (or a target-repo equivalent)
- A pointer back to the ash repo's full CLAUDE.md / docs

The template is generic — no repo-specific paths or verbs that don't yet exist. Idempotency follows the hook-entry pattern: re-running `ash init` is a no-op; an existing CLAUDE.md gets append-merged with a conflict warning unless `--force`.

This is **not** in the scope of the current cleanup. The ticket parks the work with the chosen direction so it's not lost.

## Anti-patterns to avoid

When editing CLAUDE.md, watch for:

- **Verb-manual content.** `ash grep --pattern <re> --path <p> [--glob <pattern>] [--case smart|sensitive|insensitive] ...` — that's `ash help --verb grep`'s job, not CLAUDE.md's. Keep one canonical invocation per use-case and point to help.
- **Design-principle prose.** "Robot-first, human-second. Every design decision optimizes for agent token efficiency." That's a README pitch line. CLAUDE.md is operational, not aspirational.
- **Marketing tone.** CLAUDE.md is for an agent that's already committed to the project; it doesn't need to be sold on the recursive-dev premise. Tight, declarative, action-oriented.
- **Dead references.** `ash kill` (now `ash stop`), early `git diff <flag>` syntax (now `git --op diff <flag>`), Phase 1 ship numbers in a Phase 2 doc. Sweep these on every verb-shipping PR.
- **Configuration schema recap.** `ash.toml.example` + README §Configuration are canonical. CLAUDE.md references, doesn't recite.

## Maintenance ritual

When a new verb ships:

1. **Always:** update the verb's `Args` in `internal/verbs/<verb>/<verb>.go` so `ash help --verb <name>` reflects the new schema.
2. **Always:** add a one-row entry to README's verb table under the right domain (file system / VCS / build-test / observability / lifecycle).
3. **Sometimes:** add a CLAUDE.md switch-criteria entry only if there's a *new use case* the agent should reach for the verb in. Don't add a CLAUDE.md entry just because the verb exists.
4. **Sometimes:** add a CLAUDE.md Gotchas entry only if a session note surfaces a non-obvious failure mode. Don't pre-emptively document hypothetical gotchas.
5. Update README's "Status:" line and roadmap section when a phase boundary shifts.

When a verb's args change:

1. `ash help --verb <name>` updates automatically.
2. README's verb-table row probably doesn't need to change (it's one-line domain-level, not flag-level).
3. CLAUDE.md's switch-criteria example is updated only if a flag mentioned in the canonical invocation changed.

When a session note surfaces friction:

1. Decide: is this load-bearing wisdom (per the three-test criteria above)? If yes, add a Gotchas bullet. If no, the session note is the right home — don't pollute CLAUDE.md.
2. If it's a verb-level bug, file a Linear ticket and let the fix retire the gotcha.

The goal: **most ships should not require a CLAUDE.md edit**. The doc gets thinner over time, not heavier.

## Verification

When this model is working, the following should be true:

- A fresh agent at session start can read CLAUDE.md in under 5 minutes and answer "what should I use ash for?" without consulting README.
- A fresh maintainer can read this doc and answer "should I add this to CLAUDE.md or README?" without further guidance.
- `ash help --verb <any>` is sufficient to know how to invoke that verb; CLAUDE.md and README never claim to be the source of truth for arg defaults.
- A target-repo agent can drive ash from hook denials alone (until the embedded-template ticket lands; then they can drive it from a propagated CLAUDE.md).

When you find yourself adding the same content to two surfaces, stop and ask which one owns it. When in doubt, the more constrained surface (README → CLAUDE.md → docs) wins, since narrowing scope is reversible and broadening it costs everyone tokens.
