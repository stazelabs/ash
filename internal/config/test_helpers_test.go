package config

import "os"

// Small filesystem helpers shared across tests in this package. Kept
// in a *_test.go file so they don't ship in the production binary.

func mkdirAll(path string) error  { return os.MkdirAll(path, 0o755) }
func writeBytes(path string, content []byte) error {
	return os.WriteFile(path, content, 0o644)
}
