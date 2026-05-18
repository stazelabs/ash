---
description: Synthesize the past week's ash session friction into Linear-ready ticket drafts (ASH-168)
argument-hint: "[since-duration, default 1w]"
---

# /friction — session-friction synthesis

You are reading the ash project's ledger to draft Linear-ready ticket bullets
from real session friction. The output is a synthesis you present to me for
review; you do **not** file the tickets automatically.

This is the process side of the ASH-160 recalibration. The Go side (ASH-162)
shipped the "hook denials by rule" grouping in `ash report` that feeds the
pattern-matching step below. The recalibration explicitly rejected a Go-side
`friction` verb in favor of this prompt — if you find yourself wanting Go
heuristics for pattern-matching, that's the wrong answer.

## Step 1 — gather signals (run in parallel)

Let `WINDOW` be `$ARGUMENTS` if non-empty, otherwise `7d`.

Note: `ash report` / `recap`'s `--since` accepts Go duration syntax plus
`d` for days only (`7d`, not `1w` — ASH-171 will add `w`/`mo`). Use
`7d`, `14d`, `30d` for typical windows.

Run all three in one tool batch:

- `ash report --since $WINDOW --top 20`
- `ash recap --since $WINDOW`
- `ash metrics --verb hook --last 50`

The `report` output includes a "hook denials by rule" section (rule code →
count → top suggested ash verb) when there are deny rows in the window.
That section is the headline friction signal.

## Step 2 — look for patterns

Scan each signal for these failure modes. Each item below is a real shape;
don't invent categories that aren't in the data.

- **Truncation hotspots** (`report` truncation section) — verbs hitting
  their cap. A higher `--max` would have saved tokens, or the verb's
  default is too tight for typical use.
- **Hook denials by rule** (the ASH-162 section) — a rule firing repeatedly
  suggests either a missing verb (high count → "we should ship this") or a
  flow the agent is fighting (the rule fires and the agent works around).
- **Repeated near-identical calls** (`recap` patterns + `report` arg
  distributions) — cache opportunity, or a missing batch flag.
- **Slow sub-phase outliers** (`report` sub-phase breakdown) — exec time
  dominated by walk/io/regex/regex_compile; could be cached, gitignored
  better, or the algorithm itself needs review.
- **High tokens/KiB** (`report` token efficiency) — verbs producing
  encoding-inefficient output; structural pretty-form work might help.
- **Recurring error codes** (`report` error histogram) — a bug, missing
  arg validation, or bad UX surface.
- **MCP emit cost** (`report` mcp emit line) — if `tokens_out_emit`
  disproportionately exceeds `tokens_out`, the MCP envelope is bloating
  beyond the daemon-pretty render; revisit per-verb shape.

## Step 3 — draft tickets

For each pattern worth filing, draft a bullet in this exact shape:

```
**Title:** <short; verb-prefixed when verb-specific>
**Why:** <one sentence quoting a concrete signal from the report>
**Proposed:** <one paragraph; the actual change>
**Out of scope:** <bound the work; what the ticket will NOT touch>
**Effort:** S | M | L
**Parent:** <ASH-160 if recalibration-adjacent, else blank>
```

Cap at 5 drafts per run. Quality over quantity — if nothing surfaced worth
filing, say so explicitly and stop. A "no drafts this week" output is more
honest than five low-conviction drafts.

## Step 4 — summarize

End with:

- **Drafts:** N
- **Top 3 by impact** (your call, briefly justified)
- **One surprise** from the data (a finding you didn't predict)

Then ask the user whether to file any drafts via the Linear MCP tool. Do
not file without explicit approval. The human-in-the-loop step is the
whole point of doing this as a prompt rather than a `friction` verb — see
ASH-160 §"Seam 2 reframed" and the ASH-168 ticket.

## Tone

- Terse. Each draft fits on a screen.
- Concrete. Quote the exact line of output that motivated the draft.
- Honest about uncertainty. If a pattern shows up once, it's noise, not
  signal — note it for the next week's run, don't file it.
