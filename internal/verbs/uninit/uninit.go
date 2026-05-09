// Package uninit implements the `uninit` verb — the reverse of `ash init`.
//
// Args:
//
//	path        string (optional) - target repo root; default "."
//	no_registry bool   (optional) - skip the registry write
//
// uninit:
//   1. Removes any PreToolUse entry whose hooks invoke `ash hook` from
//      <root>/.claude/settings.json.
//   2. Removes the `.ash/` line from <root>/.gitignore if present.
//   3. Drops the registry entry for the absolute root.
//
// Removing the only PreToolUse entry leaves the array empty (and we
// strip an empty `hooks.PreToolUse` so the file doesn't grow stale
// scaffolding). The .ash/ledger.db itself is left in place — a teardown
// shouldn't delete data the user might still want to inspect.
package uninit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/registry"
	"github.com/stazelabs/ash/internal/verbs/argutil"
	"github.com/stazelabs/ash/internal/verbs/initverb"
)

type Args struct {
	Path       string
	NoRegistry bool
}

type Result struct {
	Path             string   `msgpack:"path"`
	SettingsWritten  bool     `msgpack:"settings_written"`
	GitignoreUpdated bool     `msgpack:"gitignore_updated"`
	RegistryUpdated  bool     `msgpack:"registry_updated"`
	NotInstalled     bool     `msgpack:"not_installed,omitempty"`
	Warnings         []string `msgpack:"warnings,omitempty"`
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	var perr *proto.Error
	if a.Path, perr = argutil.OptionalString(in, "path", "."); perr != nil {
		return nil, perr
	}
	if a.NoRegistry, perr = argutil.OptionalBool(in, "no_registry", false); perr != nil {
		return nil, perr
	}
	return a, nil
}

func Run(a *Args, _ *proto.Tracer) (*Result, *proto.Error) {
	abs, err := filepath.Abs(a.Path)
	if err != nil {
		return nil, &proto.Error{Code: "args", Msg: "resolving --path: " + err.Error()}
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		// uninit is best-effort: a missing directory still cleans the registry row.
		if !a.NoRegistry {
			if removed, rerr := registry.Remove(abs); rerr == nil && removed {
				return &Result{Path: abs, RegistryUpdated: true, NotInstalled: true}, nil
			}
		}
		return &Result{Path: abs, NotInstalled: true}, nil
	}

	res := &Result{Path: abs}

	settingsChanged, perr := stripSettings(abs)
	if perr != nil {
		return nil, perr
	}
	res.SettingsWritten = settingsChanged

	gitChanged, perr := stripGitignore(abs)
	if perr != nil {
		return nil, perr
	}
	res.GitignoreUpdated = gitChanged

	if !a.NoRegistry {
		regChanged, err := registry.Remove(abs)
		if err != nil {
			res.Warnings = append(res.Warnings, "registry: "+err.Error())
		} else {
			res.RegistryUpdated = regChanged
		}
	}

	if !res.SettingsWritten && !res.GitignoreUpdated && !res.RegistryUpdated {
		res.NotInstalled = true
	}
	return res, nil
}

func stripSettings(root string) (bool, *proto.Error) {
	path := filepath.Join(root, initverb.SettingsRelPath)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, &proto.Error{Code: "settings_read", Msg: err.Error()}
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return false, &proto.Error{Code: "settings_parse", Msg: path + ": " + err.Error()}
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return false, nil
	}
	preToolUse, _ := hooks["PreToolUse"].([]any)
	if len(preToolUse) == 0 {
		return false, nil
	}
	kept := make([]any, 0, len(preToolUse))
	changed := false
	for _, e := range preToolUse {
		if entryInvokesAshHook(e) {
			changed = true
			continue
		}
		kept = append(kept, e)
	}
	if !changed {
		return false, nil
	}
	if len(kept) == 0 {
		delete(hooks, "PreToolUse")
	} else {
		hooks["PreToolUse"] = kept
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	} else {
		settings["hooks"] = hooks
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, &proto.Error{Code: "settings_marshal", Msg: err.Error()}
	}
	out = append(out, '\n')
	if err := atomicWrite(path, out); err != nil {
		return false, &proto.Error{Code: "settings_write", Msg: err.Error()}
	}
	return true, nil
}

// entryInvokesAshHook returns true if the PreToolUse entry has any hook
// command that ends in an `ash hook` invocation. Mirrors the loose match
// used by initverb so we recognize both PATH-form (`ash hook`) and
// per-repo-form (`$CLAUDE_PROJECT_DIR/bin/ash hook`) entries.
func entryInvokesAshHook(e any) bool {
	em, ok := e.(map[string]any)
	if !ok {
		return false
	}
	hooks, ok := em["hooks"].([]any)
	if !ok {
		return false
	}
	for _, h := range hooks {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		cmd, _ := hm["command"].(string)
		cmd = strings.TrimSpace(cmd)
		if cmd == initverb.HookCommand {
			return true
		}
		if !strings.Contains(cmd, "ash") {
			continue
		}
		if strings.HasSuffix(cmd, " hook") || strings.HasSuffix(cmd, "/ash hook") ||
			strings.HasSuffix(cmd, `"ash" hook`) || strings.HasSuffix(cmd, `" hook`) {
			return true
		}
	}
	return false
}

func stripGitignore(root string) (bool, *proto.Error) {
	path := filepath.Join(root, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, &proto.Error{Code: "gitignore_read", Msg: err.Error()}
	}
	lines := strings.Split(string(data), "\n")
	out := lines[:0]
	changed := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == ".ash/" || t == ".ash" {
			changed = true
			continue
		}
		out = append(out, l)
	}
	if !changed {
		return false, nil
	}
	if err := atomicWrite(path, []byte(strings.Join(out, "\n"))); err != nil {
		return false, &proto.Error{Code: "gitignore_write", Msg: err.Error()}
	}
	return true, nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ash-uninit-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func PrettyResponse(_ *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	r, ok := decodeResult(rsp.Data)
	if !ok {
		return "ok\n<unrecognized uninit result>"
	}
	var b strings.Builder
	header := fmt.Sprintf("=== ash uninit: %s ===", r.Path)
	if r.NotInstalled {
		header = fmt.Sprintf("=== ash uninit: %s — not installed ===", r.Path)
	}
	b.WriteString(header)
	b.WriteByte('\n')
	fmt.Fprintf(&b, "settings:  %s\n", yesNo(r.SettingsWritten))
	fmt.Fprintf(&b, "gitignore: %s\n", yesNo(r.GitignoreUpdated))
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

func decodeResult(data any) (*Result, bool) {
	if r, ok := data.(*Result); ok {
		return r, true
	}
	m, ok := data.(map[string]any)
	if !ok {
		return nil, false
	}
	r := &Result{}
	if v, ok := m["path"].(string); ok {
		r.Path = v
	}
	if v, ok := m["settings_written"].(bool); ok {
		r.SettingsWritten = v
	}
	if v, ok := m["gitignore_updated"].(bool); ok {
		r.GitignoreUpdated = v
	}
	if v, ok := m["registry_updated"].(bool); ok {
		r.RegistryUpdated = v
	}
	if v, ok := m["not_installed"].(bool); ok {
		r.NotInstalled = v
	}
	if raw, ok := m["warnings"].([]any); ok {
		for _, w := range raw {
			if s, ok := w.(string); ok {
				r.Warnings = append(r.Warnings, s)
			}
		}
	}
	return r, true
}
