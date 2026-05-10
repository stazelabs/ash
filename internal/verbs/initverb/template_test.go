package initverb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInit_FreshRepo_WritesGuidance covers the no-CLAUDE.md case: init
// should create CLAUDE.md and the bracketed section should be present.
func TestInit_FreshRepo_WritesGuidance(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()

	res := runInit(t, root, false, false)
	if !res.GuidanceWritten {
		t.Fatalf("expected guidance_written=true on fresh repo: %+v", res)
	}
	want := filepath.Join(root, "CLAUDE.md")
	if res.GuidancePath != want {
		t.Fatalf("guidance_path: got %q, want %q", res.GuidancePath, want)
	}

	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, GuidanceBeginMarker) || !strings.Contains(body, GuidanceEndMarker) {
		t.Fatalf("CLAUDE.md missing markers:\n%s", body)
	}
	if !strings.Contains(body, "ash help") {
		t.Fatalf("CLAUDE.md missing template body:\n%s", body)
	}
}

// TestInit_ExistingClaudeMd_AppendsSection covers the merge case: an
// existing CLAUDE.md is preserved and the section is appended.
func TestInit_ExistingClaudeMd_AppendsSection(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	original := "# My project\n\nSome existing notes.\n"
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runInit(t, root, false, false)
	if !res.GuidanceWritten {
		t.Fatalf("expected guidance_written=true: %+v", res)
	}

	body, _ := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if !strings.HasPrefix(string(body), original) {
		t.Fatalf("original CLAUDE.md content was clobbered:\n%s", body)
	}
	if !strings.Contains(string(body), GuidanceBeginMarker) {
		t.Fatalf("section not appended:\n%s", body)
	}
}

// TestInit_AlreadyInstalled_GuidanceUnchanged: re-running init on a repo
// where the section is already present is a no-op for the guidance file.
func TestInit_AlreadyInstalled_GuidanceUnchanged(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	runInit(t, root, false, false)

	guidancePath := filepath.Join(root, "CLAUDE.md")
	stat1, err := os.Stat(guidancePath)
	if err != nil {
		t.Fatal(err)
	}

	res := runInit(t, root, false, false)
	if res.GuidanceWritten {
		t.Fatalf("re-init should not rewrite guidance: %+v", res)
	}
	if !res.AlreadyInstalled {
		t.Fatalf("expected already_installed=true: %+v", res)
	}

	stat2, err := os.Stat(guidancePath)
	if err != nil {
		t.Fatal(err)
	}
	if !stat1.ModTime().Equal(stat2.ModTime()) {
		t.Fatal("CLAUDE.md mtime changed during no-op re-init")
	}
}

// TestInit_ConflictingGuidance_NoForce: an older version of the section
// is left alone with a warning unless --force.
func TestInit_ConflictingGuidance_NoForce(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	older := GuidanceBeginMarker + "\nold and stale ash guidance\n" + GuidanceEndMarker + "\n"
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte(older), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runInit(t, root, false, false)
	if res.GuidanceWritten {
		t.Fatal("conflict without --force should leave CLAUDE.md alone")
	}
	if len(res.Warnings) == 0 {
		t.Fatal("expected a warning for conflicting guidance section")
	}
	body, _ := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if string(body) != older {
		t.Fatalf("CLAUDE.md content changed without --force:\n%s", body)
	}
}

// TestInit_ConflictingGuidance_Force: --force replaces the older section.
func TestInit_ConflictingGuidance_Force(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	older := "# Notes\n\n" + GuidanceBeginMarker + "\nold and stale ash guidance\n" + GuidanceEndMarker + "\n\nuser content after section\n"
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte(older), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runInit(t, root, true, false)
	if !res.GuidanceWritten {
		t.Fatal("--force should rewrite the section")
	}
	body, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if strings.Contains(s, "old and stale ash guidance") {
		t.Fatalf("--force did not replace old section:\n%s", s)
	}
	if !strings.HasPrefix(s, "# Notes\n\n") {
		t.Fatalf("--force clobbered content before section:\n%s", s)
	}
	if !strings.Contains(s, "user content after section") {
		t.Fatalf("--force clobbered content after section:\n%s", s)
	}
}

// TestInit_AgentsMd: when only AGENTS.md exists, init merges into it
// instead of creating CLAUDE.md.
func TestInit_AgentsMd(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Agents\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runInit(t, root, false, false)
	if filepath.Base(res.GuidancePath) != "AGENTS.md" {
		t.Fatalf("expected AGENTS.md target, got %q", res.GuidancePath)
	}
	if _, err := os.Stat(filepath.Join(root, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("CLAUDE.md should not be created when AGENTS.md exists: err=%v", err)
	}
	body, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if !strings.Contains(string(body), GuidanceBeginMarker) {
		t.Fatalf("AGENTS.md missing markers:\n%s", body)
	}
}

// TestInit_BothFiles_PrefersClaudeMd: if both CLAUDE.md and AGENTS.md
// exist, the section is written to CLAUDE.md; AGENTS.md is untouched.
func TestInit_BothFiles_PrefersClaudeMd(t *testing.T) {
	withTempXDG(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("# Claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agentsBefore := "# Agents\n\nunchanged\n"
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(agentsBefore), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runInit(t, root, false, false)
	if filepath.Base(res.GuidancePath) != "CLAUDE.md" {
		t.Fatalf("expected CLAUDE.md target, got %q", res.GuidancePath)
	}
	agentsAfter, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if string(agentsAfter) != agentsBefore {
		t.Fatalf("AGENTS.md was modified when CLAUDE.md was the target:\n%s", agentsAfter)
	}
}

// TestRenderSection_RoundTrip: verify the marker regex reliably extracts
// the rendered section. Guards against future template tweaks breaking
// the idempotency check.
func TestRenderSection_RoundTrip(t *testing.T) {
	section := renderSection()
	if !strings.HasPrefix(section, GuidanceBeginMarker+"\n") {
		t.Fatalf("rendered section does not start with begin marker:\n%s", section)
	}
	if !strings.HasSuffix(section, GuidanceEndMarker+"\n") {
		t.Fatalf("rendered section does not end with end marker + newline:\n%s", section)
	}
	loc := guidanceSectionRE.FindStringIndex(section)
	if loc == nil {
		t.Fatal("regex failed to match rendered section")
	}
	if section[loc[0]:loc[1]] != section {
		t.Fatalf("regex match did not cover full section: got [%d:%d] of len %d", loc[0], loc[1], len(section))
	}
}
