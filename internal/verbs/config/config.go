// Package config implements the `config` verb.
//
// Args:
//
//	(none) — prints the embedded ash.toml.example.
//
// The example is compiled into the binary at build time via //go:embed,
// so it is available from any install (brew, CI, repo checkout) without
// a filesystem search. The canonical copy lives at the repo root;
// internal/verbs/config/ash.toml.example is a byte-identical mirror kept
// in sync by `make config-sync` and gated by `make config-check`.
package config

import (
	_ "embed"
	"strings"

	"github.com/stazelabs/ash/internal/proto"
)

//go:embed ash.toml.example
var exampleToml []byte

type Args struct{}

type Result struct {
	Content string `msgpack:"content"`
}

func ParseArgs(_ map[string]any) (*Args, *proto.Error) {
	return &Args{}, nil
}

func Run(_ *Args, _ *proto.Tracer) (*Result, *proto.Error) {
	return &Result{Content: string(exampleToml)}, nil
}

func PrettyResponse(_ *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized config result>"
	}
	var b strings.Builder
	b.WriteString("§config: ash.toml.example\n")
	b.WriteString(r.Content)
	return strings.TrimRight(b.String(), "\n")
}
