// Package session resolves project root, the per-project daemon socket path,
// and on-disk locations for ledger and logs.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// Root walks up from start looking for a .git directory or go.mod file. If
// neither is found, returns start (made absolute). The walk is generous: any
// marker stops it, so a fresh repo without .git but with go.mod still works.
func Root(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	cur := abs
	for {
		if isProjectMarker(cur) {
			return cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil
		}
		cur = parent
	}
}

func isProjectMarker(dir string) bool {
	for _, m := range []string{".git", "go.mod"} {
		if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
			return true
		}
	}
	return false
}

// SocketPath returns the per-project Unix domain socket path. Stable for a
// given root; collisions across roots require a sha-256 prefix collision.
func SocketPath(root string) string {
	sum := sha256.Sum256([]byte(root))
	hash := hex.EncodeToString(sum[:8])
	return filepath.Join(socketBaseDir(), "ash-"+hash+".sock")
}

func socketBaseDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "ash")
	}
	return os.TempDir()
}

// LedgerPath returns where the SQLite ledger lives for a project root.
func LedgerPath(root string) string {
	return filepath.Join(root, ".ash", "ledger.db")
}

// PIDPath returns the daemon PID file path for a project root.
func PIDPath(root string) string {
	return filepath.Join(root, ".ash", "ashd.pid")
}

// LogPath returns the daemon log file path for a project root.
func LogPath(root string) string {
	return filepath.Join(root, ".ash", "ashd.log")
}

// EnsureRuntimeDirs creates the per-project .ash dir and the socket parent dir.
func EnsureRuntimeDirs(root string) error {
	if err := os.MkdirAll(filepath.Join(root, ".ash"), 0o755); err != nil {
		return err
	}
	return os.MkdirAll(socketBaseDir(), 0o755)
}
