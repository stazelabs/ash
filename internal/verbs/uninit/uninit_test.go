package uninit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stazelabs/ash/internal/verbs/initverb"
)

func withTempXDG(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", d)
	return d
}

func runInit(t *testing.T, root string) {
	t.Helper()
	_, perr := initverb.Run(&initverb.Args{Path: root}, nil)
	if perr != nil {
		t.Fatalf("init: %s: %s", perr.Code, perr.Msg)
	}
}

func runUninit(t *testing.T, root string) *Result {
	t.Helper()
	res, perr := Run(&Args{Path: root}, nil)
	if perr != nil {
		t.Fatalf("uninit: %s: %s", perr.Code, perr.Msg)
	}
	return res
}

func TestUninit_RoundTrip(t *testing.T) {
	xdg := withTempXDG(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("bin/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runInit(t, root)
	res := runUninit(t, root)
	if !res.SettingsWritten || !res.GitignoreUpdated || !res.RegistryUpdated {
		t.Fatalf("uninit after init should reverse all three: %+v", res)
	}

	// settings.json: PreToolUse entry gone (and the hooks key cleaned up
	// since it was the only one).
	data, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if hooks, ok := m["hooks"]; ok {
		hm := hooks.(map[string]any)
		if pre, ok := hm["PreToolUse"]; ok {
			t.Fatalf("PreToolUse should be gone: %v", pre)
		}
	}

	// gitignore: .ash/ removed.
	gi, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if strings.Contains(string(gi), ".ash") {
		t.Fatalf("gitignore still contains .ash:\n%s", gi)
	}

	// Registry: empty file or no entry for our root.
	regPath := filepath.Join(xdg, "ash", "installed-repos.txt")
	if data, err := os.ReadFile(regPath); err == nil {
		abs, _ := filepath.Abs(root)
		if strings.Contains(string(data), abs) {
			t.Fatalf("registry still contains %q:\n%s", abs, data)
		}
	}
}

func TestUninit_NotInstalled(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	res := runUninit(t, root)
	if !res.NotInstalled {
		t.Fatalf("expected not_installed=true on a virgin repo: %+v", res)
	}
}

func TestUninit_PreservesOtherHooks(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	clauseDir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(clauseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Two PreToolUse entries: one is the ash hook (PATH form), one is unrelated.
	existing := `{
  "hooks": {
    "PreToolUse": [
      {"matcher": "Grep", "hooks": [{"type": "command", "command": "ash hook"}]},
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "logger pre-bash"}]}
    ],
    "PostToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "echo done"}]}
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(clauseDir, "settings.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runUninit(t, root)
	if !res.SettingsWritten {
		t.Fatal("expected settings rewrite")
	}
	data, _ := os.ReadFile(filepath.Join(clauseDir, "settings.json"))
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	hooks := m["hooks"].(map[string]any)
	pre := hooks["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Fatalf("expected 1 PreToolUse entry remaining, got %d", len(pre))
	}
	if matcher := pre[0].(map[string]any)["matcher"]; matcher != "Bash" {
		t.Fatalf("wrong entry survived: matcher=%v", matcher)
	}
	if _, ok := hooks["PostToolUse"]; !ok {
		t.Fatal("PostToolUse should be untouched")
	}
}
