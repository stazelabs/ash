package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestWatcher_NoFiles confirms NewWatcher works against a pristine
// project — no global, no project file, no ASH_CONFIG override. It
// must yield Defaults and a stable Refresh.
func TestWatcher_NoFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "no-such-config"))
	t.Setenv("ASH_CONFIG", "")

	w, cfg, source, err := NewWatcher(root)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if cfg == nil {
		t.Fatal("cfg should not be nil")
	}
	if source != "defaults" {
		t.Errorf("source = %q, want \"defaults\"", source)
	}
	if cfg.Jail.Enabled {
		t.Errorf("default jail should be disabled")
	}

	// Refresh on an unchanged tree returns changed=false.
	_, _, changed, err := w.Refresh()
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if changed {
		t.Errorf("Refresh on unchanged tree reported changed=true")
	}
}

// TestWatcher_DetectsProjectFileAppearance is the headline case: agent
// edits ash.toml mid-session and the daemon picks it up on the next
// request. The Watcher must report changed=true exactly once on the
// transition, and the new Config must reflect the file's contents.
func TestWatcher_DetectsProjectFileAppearance(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "no-such-config"))
	t.Setenv("ASH_CONFIG", "")

	w, cfg, _, err := NewWatcher(root)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if cfg.Jail.Enabled {
		t.Errorf("starting state should have jail disabled")
	}

	// Create the project file with jail enabled.
	if err := writeBytes(ProjectPath(root), []byte("[jail]\nenabled = true\n")); err != nil {
		t.Fatalf("writeBytes: %v", err)
	}

	cfg2, source2, changed, err := w.Refresh()
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !changed {
		t.Fatal("Refresh after file creation must report changed=true")
	}
	if !cfg2.Jail.Enabled {
		t.Errorf("Refresh did not pick up [jail].enabled = true; cfg=%+v", cfg2)
	}
	if source2 != ProjectPath(root) {
		t.Errorf("source = %q, want project path %q", source2, ProjectPath(root))
	}

	// Second refresh with no change must report changed=false (so the
	// daemon's enforcement re-apply isn't fired on every request after
	// a one-time edit).
	_, _, changed2, err := w.Refresh()
	if err != nil {
		t.Fatalf("Refresh #2: %v", err)
	}
	if changed2 {
		t.Errorf("Refresh after no change reported changed=true (would fire enforcement every request)")
	}
}

// TestWatcher_DetectsProjectFileChange covers the mid-life edit: file
// existed at NewWatcher time, then its contents change. Verified by
// size delta (the (mtime, size) signature catches same-second edits
// the bare-mtime check would miss).
func TestWatcher_DetectsProjectFileChange(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "no-such-config"))
	t.Setenv("ASH_CONFIG", "")

	if err := writeBytes(ProjectPath(root), []byte("[jail]\nenabled = false\n")); err != nil {
		t.Fatalf("writeBytes initial: %v", err)
	}
	w, cfg, _, err := NewWatcher(root)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if cfg.Jail.Enabled {
		t.Errorf("initial cfg should have jail disabled")
	}

	// Different size (line length) so the (mtime, size) signature
	// changes even if mtime resolution is coarse on this FS.
	if err := writeBytes(ProjectPath(root), []byte("[jail]\nenabled = true\nallow_paths = [\"/tmp\"]\n")); err != nil {
		t.Fatalf("writeBytes update: %v", err)
	}
	cfg2, _, changed, err := w.Refresh()
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !changed {
		t.Fatal("Refresh after file change must report changed=true")
	}
	if !cfg2.Jail.Enabled {
		t.Errorf("Refresh did not pick up jail enabled change")
	}
	if len(cfg2.Jail.AllowPaths) != 1 || cfg2.Jail.AllowPaths[0] != "/tmp" {
		t.Errorf("Refresh did not pick up allow_paths: %+v", cfg2.Jail.AllowPaths)
	}
}

// TestWatcher_DetectsProjectFileDisappearance covers the agent
// removing ash.toml: the daemon should revert to the next-lower
// config layer (defaults, in this test) and report changed=true.
func TestWatcher_DetectsProjectFileDisappearance(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "no-such-config"))
	t.Setenv("ASH_CONFIG", "")

	if err := writeBytes(ProjectPath(root), []byte("[jail]\nenabled = true\n")); err != nil {
		t.Fatalf("writeBytes: %v", err)
	}
	w, cfg, _, err := NewWatcher(root)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if !cfg.Jail.Enabled {
		t.Fatalf("initial cfg should have jail enabled, got %+v", cfg.Jail)
	}

	if err := os.Remove(ProjectPath(root)); err != nil {
		t.Fatalf("remove: %v", err)
	}

	cfg2, _, changed, err := w.Refresh()
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !changed {
		t.Fatal("Refresh after file removal must report changed=true")
	}
	if cfg2.Jail.Enabled {
		t.Errorf("Refresh after file removal should revert to defaults (jail disabled)")
	}
}

// TestWatcher_KeepsConfigOnLoadError covers the half-edit case: a
// user saves an ash.toml in mid-edit with a syntax error. The Watcher
// must keep the previous good config rather than swap garbage in.
func TestWatcher_KeepsConfigOnLoadError(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "no-such-config"))
	t.Setenv("ASH_CONFIG", "")

	if err := writeBytes(ProjectPath(root), []byte("[jail]\nenabled = true\n")); err != nil {
		t.Fatalf("writeBytes initial: %v", err)
	}
	w, cfgInit, _, err := NewWatcher(root)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if !cfgInit.Jail.Enabled {
		t.Fatalf("initial cfg should have jail enabled")
	}

	// Save a syntactically-broken TOML — different size so the
	// signature comparison triggers a reload attempt.
	if err := writeBytes(ProjectPath(root), []byte("[jail]\nthis is = not valid toml at all !!!!!!!\n")); err != nil {
		t.Fatalf("writeBytes broken: %v", err)
	}

	cfgAfter, _, changed, err := w.Refresh()
	if err == nil {
		t.Fatal("Refresh on malformed file should return an error")
	}
	if changed {
		t.Errorf("Refresh on malformed file should report changed=false (no swap occurred)")
	}
	if !cfgAfter.Jail.Enabled {
		t.Errorf("Refresh on error should preserve previous cfg; got %+v", cfgAfter.Jail)
	}
}

// TestWatcher_ConcurrentRefresh exercises the goroutine-safety claim
// in the Watcher's doc. The daemon's per-request handler can race
// many goroutines through Refresh simultaneously after one file edit;
// at most one should observe changed=true (the winning swap), and no
// goroutine should panic on the shared map.
func TestWatcher_ConcurrentRefresh(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "no-such-config"))
	t.Setenv("ASH_CONFIG", "")

	w, _, _, err := NewWatcher(root)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if err := writeBytes(ProjectPath(root), []byte("[jail]\nenabled = true\n")); err != nil {
		t.Fatalf("writeBytes: %v", err)
	}

	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)
	changedSeen := make(chan bool, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _, ch, err := w.Refresh()
			if err != nil {
				t.Errorf("Refresh: %v", err)
			}
			changedSeen <- ch
		}()
	}
	wg.Wait()
	close(changedSeen)

	changes := 0
	for ch := range changedSeen {
		if ch {
			changes++
		}
	}
	if changes != 1 {
		t.Errorf("expected exactly one Refresh to report changed=true (the swap winner); got %d", changes)
	}
}

// TestWatcher_LoadErrorOnInitialBadFile covers the early-fail case:
// NewWatcher on a malformed file should surface the error to the
// caller (the daemon will log.Fatal at startup; better than running
// with surprise defaults).
func TestWatcher_LoadErrorOnInitialBadFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "no-such-config"))
	t.Setenv("ASH_CONFIG", "")

	if err := writeBytes(ProjectPath(root), []byte("not valid toml ===\n")); err != nil {
		t.Fatalf("writeBytes: %v", err)
	}
	_, _, _, err := NewWatcher(root)
	if err == nil {
		t.Fatal("NewWatcher on malformed file should return an error")
	}
	if !strings.Contains(err.Error(), "load project config") {
		t.Errorf("error message missing context: %v", err)
	}
}
