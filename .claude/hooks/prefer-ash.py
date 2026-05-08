#!/usr/bin/env python3
"""PreToolUse hook: deny built-in Grep/Glob and bash grep/find/cat/head/tail/
ls -R/git status/git log/stat in the ash repo, returning a suggested `ash <verb>`
invocation.

Read is intentionally NOT denied: the harness Edit/Write tools require a
prior native Read to satisfy their internal "file has been read" guard, so
denying Read created a catch-22 that forced agents into bash workarounds
(`tee` heredocs, `python3 -c`) just to edit a file. CLAUDE.md guidance and
ledger inspection drive `ash read` adoption instead. Bash `cat`/`head`/`tail`
remain denied so pure-exploration reads still flow through `ash`.

The hook is project-scoped (registered in .claude/settings.json). On any
unexpected error it allows the call through — the hook should steer, not
break, the agent.
"""

from __future__ import annotations

import json
import os
import re
import shlex
import sys
from pathlib import PurePath

NUDGE_TAIL = (
    'See CLAUDE.md "When to prefer ash over bash". If ash genuinely falls '
    "short, run it anyway and write a session note in docs/session-notes/."
)

GREP_LIKE = {"grep", "rg", "egrep", "fgrep"}
READ_LIKE = {"cat", "head", "tail"}
FIND_LIKE = {"find"}
STAT_LIKE = {"stat"}

GIT_REDIRECT = {"status", "log"}


def deny(reason: str) -> None:
    out = {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": f"{reason} {NUDGE_TAIL}",
        }
    }
    json.dump(out, sys.stdout)
    sys.exit(0)


def allow() -> None:
    sys.exit(0)


def shellquote(s: str) -> str:
    return shlex.quote(s) if s else "''"


def suggest_grep(pattern: str | None, path: str | None, glob: str | None = None) -> str:
    parts = ["ash grep"]
    if pattern is not None:
        parts.append(f"--pattern {shellquote(pattern)}")
    if path:
        parts.append(f"--path {shellquote(path)}")
    else:
        parts.append("--path .")
    if glob:
        parts.append(f"--glob {shellquote(glob)}")
    return " ".join(parts)


def suggest_find(path: str | None, glob: str | None = None, type_: str | None = None) -> str:
    parts = ["ash find"]
    parts.append(f"--path {shellquote(path)}" if path else "--path .")
    if glob:
        parts.append(f"--glob {shellquote(glob)}")
    if type_:
        parts.append(f"--type {type_}")
    return " ".join(parts)


def suggest_read(path: str | None) -> str:
    return f"ash read --path {shellquote(path)}" if path else "ash read --path <file>"


def suggest_stat(paths: list[str]) -> str:
    joined = ",".join(paths) if paths else "<path>"
    return f"ash stat --paths {shellquote(joined)}"


def handle_grep(tool_input: dict) -> None:
    pattern = tool_input.get("pattern")
    path = tool_input.get("path") or "."
    glob = tool_input.get("glob") or tool_input.get("include")
    deny(f"Use ash instead: `{suggest_grep(pattern, path, glob)}`.")


def handle_glob(tool_input: dict) -> None:
    pattern = tool_input.get("pattern") or "**/*"
    path = tool_input.get("path") or "."
    deny(f"Use ash instead: `{suggest_find(path, pattern, 'file')}`.")


_BASH_SPLIT_RE = re.compile(r"\|\||&&|;|\|")


def _segments(command: str) -> list[str]:
    return [s.strip() for s in _BASH_SPLIT_RE.split(command) if s.strip()]


def _first_token(segment: str) -> tuple[str, list[str]]:
    """Return (program, args) for a bash segment, stripping leading
    VAR=value assignments and `env`/`command` prefixes."""
    try:
        toks = shlex.split(segment, posix=True)
    except ValueError:
        toks = segment.split()
    while toks and "=" in toks[0] and toks[0].split("=", 1)[0].isidentifier():
        toks = toks[1:]
    while toks and toks[0] in {"env", "command", "exec", "time", "nice"}:
        # `env FOO=bar prog` — skip env and any further VAR=val
        toks = toks[1:]
        while toks and "=" in toks[0] and toks[0].split("=", 1)[0].isidentifier():
            toks = toks[1:]
    if not toks:
        return "", []
    prog = PurePath(toks[0]).name  # /usr/bin/grep -> grep
    return prog, toks[1:]


def _ls_is_recursive(args: list[str]) -> bool:
    for a in args:
        if a.startswith("-") and not a.startswith("--") and "R" in a[1:]:
            return True
        if a == "--recursive":
            return True
    return False


def _git_subcommand(args: list[str]) -> str | None:
    for a in args:
        if a.startswith("-"):
            continue
        return a
    return None


def handle_bash(tool_input: dict) -> None:
    command = tool_input.get("command") or ""
    if not command.strip():
        allow()

    for seg in _segments(command):
        prog, args = _first_token(seg)
        if not prog:
            continue

        if prog in GREP_LIKE:
            # Best-effort pattern + path extraction: last positional after flags.
            positional = [a for a in args if not a.startswith("-")]
            pattern = positional[0] if positional else None
            path = positional[1] if len(positional) >= 2 else "."
            deny(
                f"Use ash instead: `{suggest_grep(pattern, path)}` (bash `{prog}` is "
                "redirected to ash grep in this repo)."
            )

        if prog in FIND_LIKE:
            positional = [a for a in args if not a.startswith("-")]
            path = positional[0] if positional else "."
            # Try to pull a -name pattern for the suggestion.
            glob = None
            for i, a in enumerate(args):
                if a in {"-name", "-iname"} and i + 1 < len(args):
                    glob = args[i + 1]
                    break
            deny(
                f"Use ash instead: `{suggest_find(path, glob)}` (bash `find` is "
                "redirected to ash find in this repo)."
            )

        if prog in READ_LIKE:
            positional = [a for a in args if not a.startswith("-")]
            path = positional[0] if positional else None
            deny(
                f"Use ash instead: `{suggest_read(path)}` (bash `{prog}` is "
                "redirected to ash read in this repo)."
            )

        if prog == "ls" and _ls_is_recursive(args):
            positional = [a for a in args if not a.startswith("-")]
            path = positional[0] if positional else "."
            deny(
                f"Use ash instead: `{suggest_find(path)}` (recursive `ls -R` is "
                "redirected to ash find in this repo)."
            )

        if prog in STAT_LIKE:
            positional = [a for a in args if not a.startswith("-")]
            deny(
                f"Use ash instead: `{suggest_stat(positional)}` (bash `stat` is "
                "redirected to ash stat in this repo)."
            )

        if prog == "git":
            sub = _git_subcommand(args)
            if sub in GIT_REDIRECT:
                deny(
                    f"Use ash instead: `ash git --op {sub}` (bash `git {sub}` "
                    "is redirected to the ash git verb in this repo)."
                )
            # Other git ops (diff, blame, show, commit, push, ...) pass through.

    allow()


def main() -> None:
    try:
        payload = json.load(sys.stdin)
    except Exception as e:
        print(f"prefer-ash hook: bad payload: {e}", file=sys.stderr)
        allow()

    tool_name = payload.get("tool_name")
    tool_input = payload.get("tool_input") or {}

    try:
        if tool_name == "Grep":
            handle_grep(tool_input)
        elif tool_name == "Glob":
            handle_glob(tool_input)
        elif tool_name == "Bash":
            handle_bash(tool_input)
        else:
            allow()
    except SystemExit:
        raise
    except Exception as e:
        print(f"prefer-ash hook: error during decision: {e}", file=sys.stderr)
        allow()


if __name__ == "__main__":
    main()
