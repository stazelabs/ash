package config

import (
	"os"
	"path/filepath"
)

// GlobalPath returns the user-global config file path.
//
// Resolution order mirrors internal/registry/registry.go:
//   1. $XDG_CONFIG_HOME/ash/config.toml
//   2. ~/.config/ash/config.toml
//   3. $TMPDIR/ash-config.toml — last-resort writable fallback
//
// The directory is not created here; Load only reads.
func GlobalPath() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "ash", "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "ash-config.toml")
	}
	return filepath.Join(home, ".config", "ash", "config.toml")
}

// ProjectPath returns the project-level config file path for a root.
// Today: <root>/ash.toml. Committed by default; pairs with the global
// file as the higher-precedence layer.
func ProjectPath(root string) string {
	return filepath.Join(root, "ash.toml")
}
