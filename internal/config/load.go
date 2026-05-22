package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// Load returns the effective configuration for a project root, plus a
// short label describing where the highest-precedence layer was read
// from (used in the daemon's ready log line).
//
// Layer order — last-wins:
//
//  1. Defaults() — compiled-in baseline.
//  2. GlobalPath() — $XDG_CONFIG_HOME/ash/config.toml (if present).
//  3. ProjectPath(root) — <root>/ash.toml (if present).
//  4. $ASH_CONFIG — explicit-path override (if non-empty).
//
// Each successful TOML decode happens against the same *Config value,
// so non-zero fields in higher layers override lower layers. Absent
// fields preserve the prior value. This is the standard
// BurntSushi/toml.DecodeFile semantic.
//
// Missing files at layers 2 and 3 are not errors. A non-empty
// $ASH_CONFIG that doesn't exist IS an error — the explicit override
// is the user telling us "use this exact file"; silent fallback would
// be surprising.
//
// The returned source label is one of:
//
//	"defaults"       — no file applied.
//	"<global path>"  — only the global file applied.
//	"<project path>" — project file applied (whether or not global did).
//	"$ASH_CONFIG=<p>" — explicit override applied.
func Load(root string) (*Config, string, error) {
	cfg := Defaults()
	source := "defaults"

	if global := GlobalPath(); fileExists(global) {
		if _, err := toml.DecodeFile(global, cfg); err != nil {
			return nil, "", fmt.Errorf("load global config %s: %w", global, err)
		}
		source = global
	}

	if project := ProjectPath(root); fileExists(project) {
		if _, err := toml.DecodeFile(project, cfg); err != nil {
			return nil, "", fmt.Errorf("load project config %s: %w", project, err)
		}
		source = project
	}

	if override := os.Getenv("ASH_CONFIG"); override != "" {
		if !fileExists(override) {
			return nil, "", fmt.Errorf("ASH_CONFIG=%s: file not found", override)
		}
		if _, err := toml.DecodeFile(override, cfg); err != nil {
			return nil, "", fmt.Errorf("load ASH_CONFIG %s: %w", override, err)
		}
		source = "$ASH_CONFIG=" + override
	}

	return cfg, source, nil
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
