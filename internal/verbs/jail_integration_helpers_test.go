package verbs

import (
	"os"
	"testing"

	"github.com/stazelabs/ash/internal/proto"
)

// protoErrorShim is a thin alias over *proto.Error so the per-case
// closures in jail_integration_test.go can return a single type.
// proto.Error is already exported; this is purely cosmetic.
type protoErrorShim = proto.Error

func shim(p *proto.Error) *protoErrorShim {
	return p
}

func mustWrite(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}
