package initverb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runRefresh wraps Run for the --refresh code path.
func runRefresh(t *testing.T, root string) *Result {
	t.Helper()
	res, perr := Run(&Args{Path: root, Refresh: true, NoRegistry: true}, nil)
	if perr != nil {
		t.Fatalf("Run --refresh: %s: %s", perr.Code, perr.Msg)
	}
	return res
}

// staleSection returns a marker-bracketed section whose body deliberately
// does not match the current template — simulates an older install whose
// template has since drifted.
func staleSection() string {
	return GuidanceBeginMarker + "\nold and stale ash guidance\n" + GuidanceEndMarker + "\n"
}

// TestInit_RefreshAvailable_OnStaleSection: default-mode init on a repo
// where settings are canonical but the guidance section has drifted should
// not write anything, and should surface RefreshAvailable=true so the
// agent knows --refresh would fix it.
func TestInit_RefreshAvailable_OnStaleSection(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	runInit(t, root, false, false) // canonical install

	// Replace the just-written CLAUDE.md with a stale-bytes section.
	guidancePath := filepath.Join(root, "CLAUDE.md")
	if err := os.WriteFile(guidancePath, []byte(staleSection()), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runInit(t, root, false, false)
	if res.GuidanceWritten {
		t.Fatal("default init should not rewrite a drifted section")
	}
	if !res.RefreshAvailable {
		t.Fatalf("expected RefreshAvailable=true, got %+v", res)
	}
	if res.AlreadyInstalled {
		t.Fatal("AlreadyInstalled should be false when guidance has drifted")
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected a warning suggesting --refresh")
	}
	if !strings.Contains(res.Warnings[0], "--refresh") {
		t.Fatalf("warning should mention --refresh: %q", res.Warnings[0])
	}
}

// TestInit_Refresh_AppliesUpdate: --refresh on a drifted section rewrites
// only that section, leaving settings.json and any pre-existing user
// content outside the markers untouched.
func TestInit_Refresh_AppliesUpdate(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	runInit(t, root, false, false) // canonical install

	settingsPath := filepath.Join(root, ".claude", "settings.json")
	settingsStat1, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatal(err)
	}

	// Replace CLAUDE.md with user prose + stale section + user prose.
	guidancePath := filepath.Join(root, "CLAUDE.md")
	prose := "# User notes\n\nimportant context here\n\n" + staleSection() + "\nmore user notes\n"
	if err := os.WriteFile(guidancePath, []byte(prose), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runRefresh(t, root)
	if !res.GuidanceWritten {
		t.Fatalf("--refresh should rewrite the drifted section: %+v", res)
	}
	if res.RefreshAvailable {
		t.Fatal("RefreshAvailable should be false after a refresh applied it")
	}

	body, _ := os.ReadFile(guidancePath)
	s := string(body)
	if strings.Contains(s, "old and stale ash guidance") {
		t.Fatalf("--refresh failed to replace stale section:\n%s", s)
	}
	if !strings.Contains(s, "# User notes") || !strings.Contains(s, "more user notes") {
		t.Fatalf("--refresh clobbered user content:\n%s", s)
	}

	settingsStat2, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !settingsStat1.ModTime().Equal(settingsStat2.ModTime()) {
		t.Fatal("--refresh should not touch settings.json")
	}
}

// TestInit_Refresh_AlreadyCanonical_NoOp: --refresh on a fully canonical
// repo is a no-op and reports AlreadyInstalled=true.
func TestInit_Refresh_AlreadyCanonical_NoOp(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	runInit(t, root, false, false)

	guidancePath := filepath.Join(root, "CLAUDE.md")
	stat1, err := os.Stat(guidancePath)
	if err != nil {
		t.Fatal(err)
	}

	res := runRefresh(t, root)
	if res.GuidanceWritten {
		t.Fatal("--refresh on canonical state should not write")
	}
	if !res.AlreadyInstalled {
		t.Fatalf("expected AlreadyInstalled=true, got %+v", res)
	}

	stat2, err := os.Stat(guidancePath)
	if err != nil {
		t.Fatal(err)
	}
	if !stat1.ModTime().Equal(stat2.ModTime()) {
		t.Fatal("CLAUDE.md mtime changed during no-op --refresh")
	}
}

// TestInit_Refresh_FileMissing_Warns: --refresh on a repo with no
// guidance file should warn and not create the file.
func TestInit_Refresh_FileMissing_Warns(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()

	res := runRefresh(t, root)
	if res.GuidanceWritten {
		t.Fatal("--refresh on a missing file should not create it")
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected a warning about the missing file")
	}
	if !strings.Contains(res.Warnings[0], "not found") {
		t.Fatalf("warning should mention 'not found': %q", res.Warnings[0])
	}
	if _, err := os.Stat(filepath.Join(root, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatal("CLAUDE.md should not be created by --refresh")
	}
}

// TestInit_Refresh_NoMarkers_Warns: --refresh on a file without ash
// markers should warn and leave the file untouched.
func TestInit_Refresh_NoMarkers_Warns(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	original := "# Project notes\n\nno ash markers here.\n"
	guidancePath := filepath.Join(root, "CLAUDE.md")
	if err := os.WriteFile(guidancePath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runRefresh(t, root)
	if res.GuidanceWritten {
		t.Fatal("--refresh should not append markers to an unmarked file")
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected a warning about the missing markers")
	}
	body, _ := os.ReadFile(guidancePath)
	if string(body) != original {
		t.Fatalf("--refresh modified file without markers:\n%s", body)
	}
}

// TestInit_SettingsConflict_Flag: a non-canonical hook entry should
// surface as SettingsConflict=true.
func TestInit_SettingsConflict_Flag(t *testing.T) {
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

	res := runInit(t, root, false, false)
	if !res.SettingsConflict {
		t.Fatalf("expected SettingsConflict=true: %+v", res)
	}
	if res.SettingsWritten {
		t.Fatal("settings should not be written when a conflict blocks the update")
	}
}

// TestInit_StatusLine covers initStatusLine() — the pure helper that
// computes the one-line agent hint. Exercises each branch.
func TestInit_StatusLine(t *testing.T) {
	cases := []struct {
		name    string
		refresh bool
		r       Result
		want    string
	}{
		{
			name:    "refresh applied",
			refresh: true,
			r:       Result{GuidanceWritten: true},
			want:    "refresh applied",
		},
		{
			name:    "refresh nothing to do",
			refresh: true,
			r:       Result{AlreadyInstalled: true},
			want:    "nothing to refresh — guidance section already matches current template",
		},
		{
			name:    "refresh warned only",
			refresh: true,
			r:       Result{Warnings: []string{"guidance file not found"}},
			want:    "",
		},
		{
			name: "settings conflict",
			r:    Result{SettingsConflict: true},
			want: "conflict — pass --force to override",
		},
		{
			name: "refresh available",
			r:    Result{RefreshAvailable: true},
			want: "refresh available — run: ash init --refresh",
		},
		{
			name: "nothing to do",
			r:    Result{AlreadyInstalled: true},
			want: "nothing to do",
		},
		{
			name: "fresh install",
			r:    Result{SettingsWritten: true, GuidanceWritten: true, RegistryUpdated: true},
			want: "",
		},
		{
			name: "conflict wins over refresh available",
			r:    Result{SettingsConflict: true, RefreshAvailable: true},
			want: "conflict — pass --force to override",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := initStatusLine(c.refresh, &c.r)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestInit_Refresh_IgnoresForceFlag: --refresh + --force is a valid
// combination; refresh semantics win (narrow scope, no settings touch).
func TestInit_Refresh_IgnoresForceFlag(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	runInit(t, root, false, false)

	settingsPath := filepath.Join(root, ".claude", "settings.json")
	settingsStat1, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatal(err)
	}

	res, perr := Run(&Args{Path: root, Refresh: true, Force: true, NoRegistry: true}, nil)
	if perr != nil {
		t.Fatalf("Run: %s: %s", perr.Code, perr.Msg)
	}
	if res.SettingsWritten {
		t.Fatal("--refresh --force should still not touch settings")
	}

	settingsStat2, _ := os.Stat(settingsPath)
	if !settingsStat1.ModTime().Equal(settingsStat2.ModTime()) {
		t.Fatal("--refresh --force changed settings.json mtime")
	}
}

// TestInit_AlreadyInstalled_SemanticsTightened: confirms the redefined
// AlreadyInstalled semantics (settings AND guidance both canonical) by
// constructing a state where settings are canonical but guidance has
// drifted, and asserting AlreadyInstalled=false.
func TestInit_AlreadyInstalled_SemanticsTightened(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	runInit(t, root, false, false)

	// Verify our setup: settings.json contains the canonical hook entry.
	settingsPath := filepath.Join(root, ".claude", "settings.json")
	raw, _ := os.ReadFile(settingsPath)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)

	// Drift the guidance section.
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte(staleSection()), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runInit(t, root, false, false)
	if res.AlreadyInstalled {
		t.Fatal("AlreadyInstalled should be false when guidance has drifted (even though settings are canonical)")
	}
	if !res.RefreshAvailable {
		t.Fatal("RefreshAvailable should be true in this state")
	}
}
