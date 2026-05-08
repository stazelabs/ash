package walker

import (
	"path/filepath"
	"runtime"
	"testing"
)

// repoRoot resolves the repo root from this package's source location so
// benchmarks have a real, representative tree to walk regardless of cwd.
func repoRoot(b *testing.B) string {
	b.Helper()
	_, file, _, _ := runtime.Caller(0)
	// internal/walker/walker_bench_test.go -> repo root is two levels up
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// noopVisit is a Visitor that does no work — isolates Walk's overhead.
func noopVisit(Entry) (Action, error) { return Continue, nil }

// BenchmarkWalkRepo_NoGlob walks the whole repo with no glob constraint
// (gitignore-respecting). Mirrors `ash find --path .` minus WantInfo.
func BenchmarkWalkRepo_NoGlob(b *testing.B) {
	root := repoRoot(b)
	opts := Options{RespectGitignore: true}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := Walk(root, opts, noopVisit); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWalkRepo_GoGlob is the most-common shape from the ledger
// (`*.go` and `**/*.go` dominate). Tests with the recursive form.
func BenchmarkWalkRepo_GoGlob(b *testing.B) {
	root := repoRoot(b)
	opts := Options{Glob: "**/*.go", RespectGitignore: true}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := Walk(root, opts, noopVisit); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWalkRepo_NoGitignore measures the same shape without the
// gitignore matcher in the loop, isolating its per-entry cost.
func BenchmarkWalkRepo_NoGitignore(b *testing.B) {
	root := repoRoot(b)
	opts := Options{Glob: "**/*.go"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := Walk(root, opts, noopVisit); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWalkRepo_NoFilters is the bare WalkDir + visitor cost; isolates
// what's left after removing both gitignore and glob.
func BenchmarkWalkRepo_NoFilters(b *testing.B) {
	root := repoRoot(b)
	opts := Options{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := Walk(root, opts, noopVisit); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWalkRepo_FindShape mirrors what `ash find` actually runs:
// WantInfo:true so Entry.Info is populated. Use this to measure the
// per-Lstat cost the find verb pays on every walk.
func BenchmarkWalkRepo_FindShape(b *testing.B) {
	root := repoRoot(b)
	opts := Options{Glob: "**/*.go", RespectGitignore: true, WantInfo: true}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := Walk(root, opts, noopVisit); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWalkRepo_GrepShape mirrors what `ash grep` runs after ASH-37:
// WantInfo:false; grep does its own Open+Fstat per file in searchOne.
func BenchmarkWalkRepo_GrepShape(b *testing.B) {
	root := repoRoot(b)
	opts := Options{Glob: "**/*.go", RespectGitignore: true}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := Walk(root, opts, noopVisit); err != nil {
			b.Fatal(err)
		}
	}
}
