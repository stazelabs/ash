// Package envprobe is a fixture used by the ASH-132 integration test:
// it ships a single test that asserts on a probe env var so the
// integration suite can prove `ash test` forwards the client's shell
// env to the `go test` subprocess. The body is empty — only the test
// file matters — but a non-test .go file is required to make this a
// real package that `go test` can resolve.
package envprobe
