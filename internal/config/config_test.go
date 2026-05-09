package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	c := Defaults()
	if c.Jail.Enabled {
		t.Errorf("jail must default to disabled")
	}
	if c.Git.Backend != GitBackendShellout {
		t.Errorf("git backend default: want %q, got %q", GitBackendShellout, c.Git.Backend)
	}
	if c.Daemon.MaxConcurrentHandlers != 0 {
		t.Errorf("max_concurrent_handlers default: want 0 (unlimited), got %d", c.Daemon.MaxConcurrentHandlers)
	}
}

func TestDuration_UnmarshalText(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"30s", 30 * time.Second, false},
		{"1m", time.Minute, false},
		{"500ms", 500 * time.Millisecond, false},
		{"", 0, false},
		{"banana", 0, true},
	}
	for _, c := range cases {
		var d Duration
		err := d.UnmarshalText([]byte(c.in))
		if c.err {
			if err == nil {
				t.Errorf("%q: want error, got %v", c.in, time.Duration(d))
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.in, err)
		}
		if time.Duration(d) != c.want {
			t.Errorf("%q: want %v, got %v", c.in, c.want, time.Duration(d))
		}
	}
}

func TestDuration_RoundTrip(t *testing.T) {
	d := Duration(45 * time.Second)
	out, err := d.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	var back Duration
	if err := back.UnmarshalText(out); err != nil {
		t.Fatal(err)
	}
	if back != d {
		t.Errorf("round-trip: %v -> %s -> %v", time.Duration(d), out, time.Duration(back))
	}
}

func TestLoad_MissingFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "no-such-config"))
	t.Setenv("ASH_CONFIG", "")
	cfg, source, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if source != "defaults" {
		t.Errorf("source: want %q, got %q", "defaults", source)
	}
	if cfg.Git.Backend != GitBackendShellout {
		t.Errorf("default git backend not preserved: %q", cfg.Git.Backend)
	}
}

func TestLoad_ProjectOverridesGlobal(t *testing.T) {
	root := t.TempDir()
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("ASH_CONFIG", "")

	globalDir := filepath.Join(cfgHome, "ash")
	if err := writeFile(t, filepath.Join(globalDir, "config.toml"),
		`[git]
backend = "go-git"

[daemon]
max_concurrent_handlers = 16
read_deadline = "30s"
`); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(t, filepath.Join(root, "ash.toml"),
		`[git]
backend = "shellout"

[jail]
enabled = true
allow_paths = ["/tmp/scratch"]
`); err != nil {
		t.Fatal(err)
	}

	cfg, source, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if source != filepath.Join(root, "ash.toml") {
		t.Errorf("source: want project ash.toml, got %q", source)
	}
	// Project overrides global for git.backend.
	if cfg.Git.Backend != GitBackendShellout {
		t.Errorf("project git.backend not applied: %q", cfg.Git.Backend)
	}
	// Global daemon settings survive (project file didn't set them).
	if cfg.Daemon.MaxConcurrentHandlers != 16 {
		t.Errorf("global max_concurrent_handlers lost: %d", cfg.Daemon.MaxConcurrentHandlers)
	}
	if cfg.Daemon.ReadDeadline.AsDuration() != 30*time.Second {
		t.Errorf("global read_deadline lost: %v", cfg.Daemon.ReadDeadline.AsDuration())
	}
	// Project-only jail config applies.
	if !cfg.Jail.Enabled {
		t.Errorf("jail enabled flag not applied")
	}
	if len(cfg.Jail.AllowPaths) != 1 || cfg.Jail.AllowPaths[0] != "/tmp/scratch" {
		t.Errorf("jail allow_paths: got %v", cfg.Jail.AllowPaths)
	}
}

func TestLoad_ASHConfigOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	override := filepath.Join(t.TempDir(), "override.toml")
	if err := writeFile(t, override, `[jail]
enabled = true
`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASH_CONFIG", override)

	cfg, source, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Jail.Enabled {
		t.Errorf("ASH_CONFIG override not applied")
	}
	if source != "$ASH_CONFIG="+override {
		t.Errorf("source label: %q", source)
	}
}

func TestLoad_ASHConfigMissing(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ASH_CONFIG", filepath.Join(root, "nonexistent.toml"))
	_, _, err := Load(root)
	if err == nil {
		t.Fatal("expected error for missing ASH_CONFIG path")
	}
}

func TestLoad_Malformed(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ASH_CONFIG", "")
	if err := writeFile(t, filepath.Join(root, "ash.toml"),
		"this is = not = valid toml ===\n"); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load(root)
	if err == nil {
		t.Fatal("expected error for malformed TOML")
	}
}

func writeFile(t *testing.T, path, content string) error {
	t.Helper()
	if err := mkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	return writeBytes(path, []byte(content))
}
