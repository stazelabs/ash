// Package initverb implements the `init` verb.
//
// Args:
//
//	path        string (optional) - target repo root; default "."
//	force       bool   (optional) - overwrite an existing different ash hook entry or guidance section
//	no_registry bool   (optional) - skip writing to the global installed-repos registry
//
// `ash init` bootstraps a target repo for use with ash:
//
//  1. Ensures a PreToolUse entry in <root>/.claude/settings.json that
//     runs `ash hook` (PATH form, so a single `make install` covers
//     every target repo at once).
//  2. Appends `.ash/` to <root>/.gitignore if a .gitignore exists.
//  3. Writes (or merges into) <root>/CLAUDE.md the embedded
//     agent-guidance section, bracketed by <!-- ash:begin --> /
//     <!-- ash:end --> markers so future updates are atomic. If the
//     target repo already uses AGENTS.md and lacks a CLAUDE.md, the
//     section is written there instead.
//  4. Records the absolute root in the global installed-repos registry
//     so `ash report --all-roots` can find it.
//
// Idempotent: re-running on an already-installed repo is a no-op and
// reports already_installed=true. A pre-existing settings.json entry
// that uses a different ash hook command, or a pre-existing guidance
// section whose content differs from the current template, is left in
// place with a warning unless --force is set.
package initverb

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/stazelabs/ash/internal/atomicwrite"
	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/registry"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

// HookCommand is the canonical command we install. PATH-based: the hook
// runs `ash hook` and relies on `make install` having put `ash` on PATH.
// The matcher mirrors the ash repo's own .claude/settings.json so a fresh
// target is gated on the same harness tools.
const (
	HookCommand = "ash hook"
	HookMatcher = "Grep|Glob|Bash|Edit|Write|Read"
)

// SettingsRelPath is the per-repo settings file we touch.
var SettingsRelPath = filepath.Join(".claude", "settings.json")

type Args struct {
	Path       string
	Force      bool
	NoRegistry bool
}

type Result struct {
	Path             string   `msgpack:"path"`
	SettingsWritten  bool     `msgpack:"settings_written"`
	GitignoreUpdated bool     `msgpack:"gitignore_updated"`
	GuidanceWritten  bool     `msgpack:"guidance_written,omitempty"`
	GuidancePath     string   `msgpack:"guidance_path,omitempty"`
	RegistryUpdated  bool     `msgpack:"registry_updated"`
	AlreadyInstalled bool     `msgpack:"already_installed,omitempty"`
	Warnings         []string `msgpack:"warnings,omitempty"`
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	var perr *proto.Error
	if a.Path, perr = argutil.OptionalString(in, "path", "."); perr != nil {
		return nil, perr
	}
	if a.Force, perr = argutil.OptionalBool(in, "force", false); perr != nil {
		return nil, perr
	}
	if a.NoRegistry, perr = argutil.OptionalBool(in, "no-registry", false); perr != nil {
		return nil, perr
	}
	if perr := jail.CheckPaths(map[string]string{
		"path": a.Path,
	}); perr != nil {
		return nil, perr
	}
	return a, nil
}

// Run is the verb entry point. tr is unused — init is pure file IO and
// has no instrumentable sub-phases worth tracing today.
func Run(a *Args, _ *proto.Tracer) (*Result, *proto.Error) {
	abs, err := filepath.Abs(a.Path)
	if err != nil {
		return nil, &proto.Error{Code: "args", Msg: "resolving --path: " + err.Error()}
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &proto.Error{Code: "not_found", Msg: abs + " does not exist"}
		}
		return nil, &proto.Error{Code: "stat", Msg: err.Error()}
	}
	if !info.IsDir() {
		return nil, &proto.Error{Code: "not_a_dir", Msg: abs + " is not a directory"}
	}

	res := &Result{Path: abs}

	settingsChanged, alreadyInstalled, warning, perr := updateSettings(abs, a.Force)
	if perr != nil {
		return nil, perr
	}
	res.SettingsWritten = settingsChanged
	res.AlreadyInstalled = alreadyInstalled
	if warning != "" {
		res.Warnings = append(res.Warnings, warning)
	}

	gitChanged, perr := updateGitignore(abs)
	if perr != nil {
		return nil, perr
	}
	res.GitignoreUpdated = gitChanged

	guidanceChanged, _, guidancePath, guidanceWarning, perr := updateGuidance(abs, a.Force)
	if perr != nil {
		return nil, perr
	}
	res.GuidanceWritten = guidanceChanged
	res.GuidancePath = guidancePath
	if guidanceWarning != "" {
		res.Warnings = append(res.Warnings, guidanceWarning)
	}

	if !a.NoRegistry {
		regChanged, err := registry.Add(abs)
		if err != nil {
			res.Warnings = append(res.Warnings, "registry: "+err.Error())
		} else {
			res.RegistryUpdated = regChanged
		}
	}

	// PATH probe: warn if the installed hook command ('ash hook') won't be
	// resolvable. The hook silently passes through all tool calls when the
	// binary is not found, which defeats the entire point of ash init.
	if _, lookErr := exec.LookPath("ash"); lookErr != nil {
		msg := "ash is not on your PATH — the PreToolUse hook ('ash hook') will not fire"
		if self, selfErr := os.Executable(); selfErr == nil {
			msg += fmt.Sprintf("; to fix: export PATH=%s:$PATH", filepath.Dir(self))
		}
		res.Warnings = append(res.Warnings, msg)
	}

	return res, nil
}

// BashAshAllowRule is the permissions.allow entry that pre-approves all ash
// Bash invocations. It is exported so uninit can strip it symmetrically.
// Without this entry, subagents (which don't inherit the parent session's
// MCP servers and have a fresh permission profile) cannot freely call ash
// commands — every call blocks on per-call human approval.
const BashAshAllowRule = "Bash(ash *)"

// updateSettings reads <root>/.claude/settings.json (if any), merges in a
// PreToolUse entry that runs `ash hook` and ensures "Bash(ash *)" is in
// permissions.allow, then writes it back. Returns (changed, alreadyInstalled,
// warning, error). alreadyInstalled is set when both entries are already
// present. A pre-existing hook entry that uses a different ash command
// produces a warning unless force is true, in which case it is replaced.
func updateSettings(root string, force bool) (bool, bool, string, *proto.Error) {
	path := filepath.Join(root, SettingsRelPath)

	var settings map[string]any
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return false, false, "", &proto.Error{Code: "settings_parse", Msg: path + ": " + err.Error()}
		}
	} else if !os.IsNotExist(err) {
		return false, false, "", &proto.Error{Code: "settings_read", Msg: err.Error()}
	}
	if settings == nil {
		settings = map[string]any{}
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	preToolUse, _ := hooks["PreToolUse"].([]any)

	hookAlready, conflictIdx := scanForAshHook(preToolUse)
	permAlready := hasBashAshAllow(settings)

	if hookAlready && permAlready {
		return false, true, "", nil
	}

	warning := ""
	changed := false
	blocked := false // hook conflict blocked by !force

	if !hookAlready {
		if conflictIdx >= 0 {
			if !force {
				warning = fmt.Sprintf("existing PreToolUse entry invokes ash with a different command (%s); pass --force to replace", path)
				blocked = true
			} else {
				// Replace the conflicting entry in place.
				preToolUse = append(preToolUse[:conflictIdx], preToolUse[conflictIdx+1:]...)
				preToolUse = append(preToolUse, ashHookEntry())
				hooks["PreToolUse"] = preToolUse
				settings["hooks"] = hooks
				changed = true
			}
		} else {
			preToolUse = append(preToolUse, ashHookEntry())
			hooks["PreToolUse"] = preToolUse
			settings["hooks"] = hooks
			changed = true
		}
	}

	// Only add the permissions entry if the hook is installed (or was already
	// installed). If a conflict blocked the hook update, leave settings alone.
	if !permAlready && !blocked {
		addBashAshAllow(settings)
		changed = true
	}

	if !changed {
		return false, false, warning, nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, false, warning, &proto.Error{Code: "settings_write", Msg: err.Error()}
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, false, warning, &proto.Error{Code: "settings_marshal", Msg: err.Error()}
	}
	out = append(out, '\n')
	if err := atomicwrite.Write(path, out, atomicwrite.Options{TmpPrefix: ".ash-init-"}); err != nil {
		return false, false, warning, &proto.Error{Code: "settings_write", Msg: err.Error()}
	}
	return true, false, warning, nil
}

// hasBashAshAllow reports whether BashAshAllowRule is already present in
// settings["permissions"]["allow"].
func hasBashAshAllow(settings map[string]any) bool {
	perms, _ := settings["permissions"].(map[string]any)
	if perms == nil {
		return false
	}
	allow, _ := perms["allow"].([]any)
	for _, v := range allow {
		if s, ok := v.(string); ok && s == BashAshAllowRule {
			return true
		}
	}
	return false
}

// addBashAshAllow appends BashAshAllowRule to settings["permissions"]["allow"].
func addBashAshAllow(settings map[string]any) {
	perms, _ := settings["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	allow, _ := perms["allow"].([]any)
	perms["allow"] = append(allow, BashAshAllowRule)
	settings["permissions"] = perms
}

func ashHookEntry() map[string]any {
	return map[string]any{
		"matcher": HookMatcher,
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": HookCommand,
			},
		},
	}
}

// scanForAshHook walks the PreToolUse entries looking for one whose
// hooks invoke ash. Returns (alreadyInstalled, conflictIndex).
//
// alreadyInstalled is true when the matched entry's command is exactly
// "ash hook" (the canonical PATH form we install).
//
// conflictIndex is set when a different ash hook command is found
// (e.g. the per-repo "$CLAUDE_PROJECT_DIR/bin/ash hook" form). The
// caller decides whether to replace it (with --force) or warn.
//
// At most one of the two states is reported. If both states match
// different entries, alreadyInstalled wins — the canonical entry is
// already there and the other can be left alone.
func scanForAshHook(entries []any) (bool, int) {
	conflictIdx := -1
	for i, e := range entries {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		hooks, ok := em["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range hooks {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			cmd, _ := hm["command"].(string)
			if cmd == "" {
				continue
			}
			trimmed := strings.TrimSpace(cmd)
			if trimmed == HookCommand {
				return true, -1
			}
			if mentionsAshHook(trimmed) && conflictIdx < 0 {
				conflictIdx = i
			}
		}
	}
	return false, conflictIdx
}

// mentionsAshHook returns true for any command string that ends in an
// `ash hook` invocation. The leading part may be a quoted absolute path
// or a $CLAUDE_PROJECT_DIR-style template; we don't try to parse the
// shell, just detect the hook signature.
func mentionsAshHook(cmd string) bool {
	if !strings.Contains(cmd, "ash") {
		return false
	}
	if !strings.HasSuffix(cmd, " hook") && !strings.HasSuffix(cmd, "/ash hook") &&
		!strings.HasSuffix(cmd, `"ash" hook`) && !strings.HasSuffix(cmd, `" hook`) {
		return false
	}
	return true
}

// updateGitignore appends `.ash/` to <root>/.gitignore if the file exists
// and the entry isn't already there. Returns true if a write occurred.
// A missing .gitignore is not an error — many target repos lack one.
func updateGitignore(root string) (bool, *proto.Error) {
	path := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, &proto.Error{Code: "gitignore_read", Msg: err.Error()}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == ".ash/" || strings.TrimSpace(line) == ".ash" {
			return false, nil
		}
	}
	out := string(data)
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += ".ash/\n"
	if err := atomicwrite.Write(path, []byte(out), atomicwrite.Options{TmpPrefix: ".ash-init-"}); err != nil {
		return false, &proto.Error{Code: "gitignore_write", Msg: err.Error()}
	}
	return true, nil
}

func PrettyResponse(_ *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized init result>"
	}
	var b strings.Builder
	if r.AlreadyInstalled {
		fmt.Fprintf(&b, "§init: %s — already installed\n", r.Path)
	} else {
		fmt.Fprintf(&b, "§init: %s\n", r.Path)
	}
	fmt.Fprintf(&b, "settings:  %s\n", yesNo(r.SettingsWritten))
	fmt.Fprintf(&b, "gitignore: %s\n", yesNo(r.GitignoreUpdated))
	if r.GuidancePath != "" {
		fmt.Fprintf(&b, "guidance:  %s (%s)\n", yesNo(r.GuidanceWritten), filepath.Base(r.GuidancePath))
	} else {
		fmt.Fprintf(&b, "guidance:  %s\n", yesNo(r.GuidanceWritten))
	}
	fmt.Fprintf(&b, "registry:  %s\n", yesNo(r.RegistryUpdated))
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	return strings.TrimRight(b.String(), "\n")
}

func yesNo(b bool) string {
	if b {
		return "updated"
	}
	return "unchanged"
}
