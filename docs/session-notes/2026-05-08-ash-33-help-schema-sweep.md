# Session: ASH-33 — help schema doc pass

**Task.** Sweep every verb schema in help.go and tighten descriptions that leave non-obvious semantic questions unanswered.

**Verbs used.** ash grep, ash read, ash edit, ash help (smoke test).

**Changes to internal/verbs/help/help.go:**

- **read --range**: added "1-based, inclusive on both ends. End is clamped to file length."
- **read --limit_bytes**: corrected hard cap from "256 KiB" to "8 MiB" (was factually wrong; MaxLimitBytes = 8 MiB in read.go).
- **find --max_depth**: added "1 = direct children of --path only."
- **find --exclude**: added "Matched against path relative to --path (same as --glob)."
- **find --respect_gitignore**: clarified "walk root" → "walk root (--path)" (both find and grep).
- **grep --path**: added path-form note: "Returned match paths mirror the input form."
- **grep --glob**: added "Matched against the path relative to --path (the walk root)." (primary issue callout)
- **grep --exclude**: added "Matched against path relative to --path (the walk root)."
- **grep --context_before/after**: added dedup note: "Context lines are deduplicated across overlapping matches."
- **git --path**: added repo-root-relative note and explicit departure from find/grep convention. (primary issue callout)
- **git --pathspec**: added "Interpreted relative to the repo root, not relative to --path."
- **write --content**: added "Pass '-' to read from stdin."

**Friction.** The read --limit_bytes bug (256 KiB stated, 8 MiB actual) was only caught by reading read.go — not obvious from the help output alone. Worth noting that schema descriptions can silently diverge from code.

**Instrumentation.** All tests pass. ash help --verb grep/git confirms new text.
