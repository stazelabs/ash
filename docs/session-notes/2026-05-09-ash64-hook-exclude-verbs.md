# 2026-05-09 — ASH-64: hook exclude_verbs + heredoc scanner finding

## Task

Implement `[hook].exclude_verbs` in `ash.toml` so users can opt individual verbs out of PreToolUse hook enforcement without removing the hook entirely.

## Verbs used

`ash read`, `ash edit`, `ash grep`, `ash write`, `ash test`, `ash stop`

## Friction

### Heredoc scanner false positive: `<< 'EOF'` with space

During planning I tried several ways to write the plan file to `~/.claude/plans/`. Every method that used a bash heredoc with a space between `<<` and the quoted delimiter (`<< 'PLANEOF'`, `tee ... << 'PLANEOF'`, `python3 - << 'PLANEOF'`) triggered the hook with a false positive, because the heredoc body contained words like "grep" and "find" as program names in the verb→rule mapping table.

`ash write --content - <<'PLANEOF'` (no space) worked correctly — the body was not scanned.

The hook's `scanHeredoc` function recognizes `<<'W'` but not `<< 'W'` (with a space). Bash accepts both forms. This is a real false positive: the hook scans the heredoc body as if it were commands.

## Workarounds

- Use `<<'EOF'` (no space) for all heredoc writes via `ash write`. This is already the CLAUDE.md recommendation but the space form is easy to accidentally write.
- As a last resort: Python fixer script (`ash write --path /tmp/fix.py --content - <<'PYEOF' ... PYEOF && python3 /tmp/fix.py`) for complex replacements with special characters.

## Suggestions

- `scanHeredoc` should also handle `<< 'W'` and `<< "W"` (with one or more spaces between `<<` and the delimiter). Bash allows whitespace there.
- Consider adding a test case to `TestDecide_bash` for `<< 'EOF'` (space form).

## Instrumentation

```
ash report --verb hook  # after the session
```

No denial anomalies beyond the expected false positives described above.
