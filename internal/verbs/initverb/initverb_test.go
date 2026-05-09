package initverb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempXDG redirects XDG_CONFIG_HOME so registry writes don't pollute
// the developer's real ~/.config/ash file when tests run locally.
func withTempXDG(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

// runInit is a small wrapper to keep tests readable.
func runInit(t *testing.T, root string, force, noReg bool) *Result {
	t.Helper()
	res, perr := Run(&Args{Path: root, Force: force, NoRegistry: noReg}, nil)
	if perr != nil {
		t.Fatalf("Run: %s: %s", perr.Code, perr.Msg)
	}
	return res
}

// readJSON reads and parses settings.json into a generic map. Tests use
// this rather than asserting on raw bytes so JSON key ordering doesn't
// brittle the assertions.
func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, data)
	}
	return m
}

func TestInit_FreshRepo(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("bin/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runInit(t, root, false, false)
	if !res.SettingsWritten || !res.GitignoreUpdated || !res.RegistryUpdated {
		t.Fatalf("expected all three updated, got %+v", res)
	}
	if res.AlreadyInstalled {
		t.Fatal("fresh repo should not report already_installed")
	}

	// settings.json contains a PreToolUse entry whose hook command is "ash hook".
	m := readJSON(t, filepath.Join(root, ".claude", "settings.json"))
	hooks := m["hooks"].(map[string]any)
	pre := hooks["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Fatalf("expected 1 PreToolUse entry, got %d", len(pre))
	}
	entry := pre[0].(map[string]any)
	if entry["matcher"] != HookMatcher {
		t.Fatalf("matcher: got %q", entry["matcher"])
	}
	cmds := entry["hooks"].([]any)
	cmd := cmds[0].(map[string]any)["command"].(string)
	if cmd != HookCommand {
		t.Fatalf("command: got %q want %q", cmd, HookCommand)
	}

	// gitignore now ends with `.ash/`.
	gi, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if !strings.Contains(string(gi), ".ash/") {
		t.Fatalf("gitignore missing .ash/ entry:\n%s", gi)
	}
}

func TestInit_AlreadyInstalled(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	runInit(t, root, false, false) // first install

	// Capture settings mtime so we can confirm the second call doesn't rewrite the file.
	settingsPath := filepath.Join(root, ".claude", "settings.json")
	stat1, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatal(err)
	}

	res := runInit(t, root, false, false) // second install: no-op
	if !res.AlreadyInstalled {
		t.Fatalf("expected already_installed, got %+v", res)
	}
	if res.SettingsWritten {
		t.Fatal("re-init should not rewrite settings")
	}

	stat2, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !stat1.ModTime().Equal(stat2.ModTime()) {
		t.Fatal("settings.json mtime changed during no-op re-init")
	}
}

func TestInit_ConflictingHookEntry_NoForce(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	clauseDir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(clauseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-existing settings with a per-repo $CLAUDE_PROJECT_DIR ash hook —
	// the form the ash repo itself uses.
	existing := `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Grep|Glob|Bash|Edit|Write|Read",
        "hooks": [
          {"type": "command", "command": "\"$CLAUDE_PROJECT_DIR/bin/ash\" hook"}
        ]
      }
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(clauseDir, "settings.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runInit(t, root, false, false)
	if res.SettingsWritten {
		t.Fatal("conflicting entry without --force should leave settings alone")
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected a warning for conflicting hook entry")
	}
}

func TestInit_ConflictingHookEntry_Force(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	clauseDir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(clauseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Grep|Glob|Bash|Edit|Write|Read",
        "hooks": [
          {"type": "command", "command": "\"$CLAUDE_PROJECT_DIR/bin/ash\" hook"}
        ]
      }
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(clauseDir, "settings.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runInit(t, root, true, false)
	if !res.SettingsWritten {
		t.Fatal("--force should replace the conflicting entry")
	}

	// Only the new PATH-form entry remains.
	m := readJSON(t, filepath.Join(clauseDir, "settings.json"))
	pre := m["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Fatalf("expected 1 entry after --force, got %d", len(pre))
	}
	cmd := pre[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	if cmd != HookCommand {
		t.Fatalf("command after --force: got %q", cmd)
	}
}

func TestInit_NoGitignore(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	res := runInit(t, root, false, false)
	if res.GitignoreUpdated {
		t.Fatal("a repo without .gitignore should not report gitignore_updated")
	}
	if !res.SettingsWritten {
		t.Fatal("settings should still be written")
	}
}

func TestInit_PreservesOtherSettings(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	clauseDir := filepath.Join(root, ".claude")
	if err := os.MkdirAll(clauseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{
  "theme": "dark",
  "hooks": {
    "PostToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "echo done"}]}
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(clauseDir, "settings.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	runInit(t, root, false, false)

	m := readJSON(t, filepath.Join(clauseDir, "settings.json"))
	if m["theme"] != "dark" {
		t.Fatal("init clobbered top-level theme key")
	}
	hooks := m["hooks"].(map[string]any)
	if _, ok := hooks["PostToolUse"]; !ok {
		t.Fatal("init clobbered PostToolUse")
	}
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Fatal("init failed to add PreToolUse")
	}
}

func TestInit_RegistryRecordsAbsolutePath(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	root := t.TempDir()

	res := runInit(t, root, false, false)
	if !res.RegistryUpdated {
		t.Fatal("expected registry update")
	}
	data, err := os.ReadFile(filepath.Join(xdg, "ash", "installed-repos.txt"))
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(root)
	if !strings.Contains(string(data), abs) {
		t.Fatalf("registry file does not contain absolute root %q:\n%s", abs, data)
	}
}

func TestInit_NoRegistry(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	root := t.TempDir()

	res := runInit(t, root, false, true) // noRegistry=true
	if res.RegistryUpdated {
		t.Fatal("--no_registry should suppress registry write")
	}
	if _, err := os.Stat(filepath.Join(xdg, "ash", "installed-repos.txt")); !os.IsNotExist(err) {
		t.Fatal("registry file should not exist with --no_registry")
	}
}
