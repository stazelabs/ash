// Package lang implements the `ash lang` verb — semantic queries served
// by the LSP broker (ASH-136), cached at the per-file level (ASH-137).
//
// Ops:
//
//	outline --path <p>             one row per top-level decl in a file
//	def     --symbol <name> [--in <p>]   definition site(s) by symbol name
//
// outline routes to textDocument/documentSymbol and is fully cached on
// (path_abs, mtime_ns, op, args_hash). def routes to workspace/symbol
// and is intentionally NOT cached today: a workspace-scoped LSP
// response cannot be invalidated by single-file mtime keying, and the
// alternative (workspace-wide invalidation) is out of scope for this
// ticket. ASH-D will revisit caching when richer workspace verbs land.
package lang

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/lsp"
	"github.com/stazelabs/ash/internal/lsp/cache"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

const (
	// requestTimeout bounds a single LSP request. Outline is cheap (<10ms
	// in the cached path, <100ms cold); workspace/symbol on a medium-
	// sized repo is dominated by gopls's first-call indexing — a 15s
	// budget covers cold-start on this codebase with room to spare.
	requestTimeout = 15 * time.Second
)

// Args is the daemon-side parsed argument set.
type Args struct {
	Op     string
	Path   string
	Symbol string
	In     string
}

// Result is the structured response wire shape.
type Result struct {
	Op       string         `msgpack:"op"`
	Symbols  []SymbolRecord `msgpack:"symbols,omitempty"`
	CacheHit bool           `msgpack:"cache_hit,omitempty"`
}

// SymbolRecord is one decl (outline) or one workspace match (def).
// Positions are 1-based to match the rest of the ash verb surface;
// LSP wire ranges are 0-based and converted on the way out.
type SymbolRecord struct {
	Name      string `msgpack:"name"`
	Kind      string `msgpack:"kind"`
	Container string `msgpack:"container,omitempty"`
	Detail    string `msgpack:"detail,omitempty"`
	Path      string `msgpack:"path"`
	Line      int    `msgpack:"line,omitempty"`
	Col       int    `msgpack:"col,omitempty"`
	EndLine   int    `msgpack:"end_line,omitempty"`
	EndCol    int    `msgpack:"end_col,omitempty"`
}

// Deps is the daemon-side dependency bundle. The runner in
// internal/verbs/verbs.go closes over the live broker + cache to
// populate this.
type Deps struct {
	Broker      *lsp.Broker
	Cache       *cache.Cache
	ProjectRoot string
}

var knownArgs = map[string]struct{}{
	"op": {}, "path": {}, "symbol": {}, "in": {},
}

// ParseArgs validates the loosely-typed args from the wire and produces
// a typed Args struct ready for RunWithDeps.
func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	var perr *proto.Error
	if a.Op, perr = argutil.RequireString(in, "op"); perr != nil {
		return nil, perr
	}
	switch a.Op {
	case "outline", "def":
	default:
		return nil, &proto.Error{Code: "args", Msg: "unknown op: " + a.Op, Hint: "valid ops: outline, def"}
	}
	if a.Path, perr = argutil.OptionalString(in, "path", ""); perr != nil {
		return nil, perr
	}
	if a.Symbol, perr = argutil.OptionalString(in, "symbol", ""); perr != nil {
		return nil, perr
	}
	if a.In, perr = argutil.OptionalString(in, "in", ""); perr != nil {
		return nil, perr
	}
	for k := range in {
		if _, ok := knownArgs[k]; !ok {
			return nil, &proto.Error{Code: "args", Msg: "unknown arg: --" + k}
		}
	}
	switch a.Op {
	case "outline":
		if a.Path == "" {
			return nil, &proto.Error{Code: "args", Msg: "outline requires --path"}
		}
	case "def":
		if a.Symbol == "" {
			return nil, &proto.Error{Code: "args", Msg: "def requires --symbol"}
		}
	}
	paths := map[string]string{}
	if a.Path != "" {
		paths["path"] = a.Path
	}
	if a.In != "" {
		paths["in"] = a.In
	}
	if len(paths) > 0 {
		if perr := jail.CheckPaths(paths); perr != nil {
			return nil, perr
		}
	}
	return a, nil
}

// RunWithDeps is the daemon entry point. Deps.Broker must be non-nil;
// when the broker is disabled or absent, the verb returns lsp_disabled
// so the caller learns the operational state without crashing.
func RunWithDeps(d Deps, a *Args) (*Result, *proto.Error) {
	if d.Broker == nil || !d.Broker.Enabled() {
		return nil, &proto.Error{Code: "lsp_disabled", Msg: "lsp broker is disabled", Hint: "set [lsp].enabled=true in ash.toml and run `ash stop`"}
	}
	switch a.Op {
	case "outline":
		return runOutline(d, a)
	case "def":
		return runDef(d, a)
	}
	return nil, &proto.Error{Code: "args", Msg: "unknown op: " + a.Op}
}

func runOutline(d Deps, a *Args) (*Result, *proto.Error) {
	abs, err := filepath.Abs(a.Path)
	if err != nil {
		return nil, &proto.Error{Code: "args", Msg: err.Error()}
	}
	if st, err := os.Stat(abs); err != nil {
		if os.IsNotExist(err) {
			return nil, &proto.Error{Code: "not_found", Msg: jail.PrettyPath(a.Path) + ": no such file"}
		}
		return nil, &proto.Error{Code: "stat", Msg: err.Error()}
	} else if st.IsDir() {
		return nil, &proto.Error{Code: "is_dir", Msg: jail.PrettyPath(a.Path) + " is a directory"}
	}

	// Cache lookup: (path_abs, mtime_ns, "documentSymbol", null).
	if d.Cache != nil {
		if raw, hit, err := d.Cache.Get(abs, "documentSymbol", nil); err == nil && hit {
			syms, ok := decodeDocumentSymbols(raw, abs, d.ProjectRoot)
			if ok {
				return &Result{Op: "outline", Symbols: syms, CacheHit: true}, nil
			}
			// Cached payload couldn't be decoded — fall through to a
			// fresh round-trip rather than serve garbage.
		}
	}

	// gopls needs the file in its in-memory view; the write/edit sink
	// keeps that current after ash mutations, but the very first call
	// after daemon start may race against an external editor. Notify
	// here too — best-effort, cost is one didOpen on first touch.
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	d.Broker.Notify(ctx, abs)

	uri := pathToURI(abs)
	var raw json.RawMessage
	if err := d.Broker.Request(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	}, &raw); err != nil {
		return nil, brokerError(err)
	}

	syms, ok := decodeDocumentSymbols(raw, abs, d.ProjectRoot)
	if !ok {
		return nil, &proto.Error{Code: "lsp_decode", Msg: "could not parse documentSymbol response"}
	}
	if d.Cache != nil {
		_ = d.Cache.Put(abs, "documentSymbol", nil, raw)
	}
	return &Result{Op: "outline", Symbols: syms}, nil
}

func runDef(d Deps, a *Args) (*Result, *proto.Error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	// workspace/symbol is workspace-scoped — not cached. See package doc.
	var raw json.RawMessage
	if err := d.Broker.Request(ctx, "workspace/symbol", map[string]any{
		"query": a.Symbol,
	}, &raw); err != nil {
		return nil, brokerError(err)
	}
	syms, ok := decodeSymbolInformation(raw, d.ProjectRoot)
	if !ok {
		return nil, &proto.Error{Code: "lsp_decode", Msg: "could not parse workspace/symbol response"}
	}
	// Exact-name match by default; gopls returns substring matches too.
	// Keep the strictness here — agents asking for `--symbol NewLedger`
	// don't want every `New*` returned.
	filtered := syms[:0]
	for _, s := range syms {
		if s.Name != a.Symbol {
			continue
		}
		if a.In != "" {
			absIn, _ := filepath.Abs(a.In)
			absSym, _ := filepath.Abs(filepath.Join(d.ProjectRoot, s.Path))
			if absSym != absIn {
				continue
			}
		}
		filtered = append(filtered, s)
	}
	return &Result{Op: "def", Symbols: filtered}, nil
}

func brokerError(err error) *proto.Error {
	if lerr, ok := err.(*lsp.Error); ok {
		return &proto.Error{Code: lerr.Code, Msg: lerr.Msg, Hint: lerr.Hint}
	}
	return &proto.Error{Code: "lsp_request", Msg: err.Error()}
}

// ----------------------------------------------------------------------
// LSP response decoding

type lspPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start lspPosition `json:"start"`
	End   lspPosition `json:"end"`
}

type lspLocation struct {
	URI   string   `json:"uri"`
	Range lspRange `json:"range"`
}

type documentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail"`
	Kind           int              `json:"kind"`
	Range          lspRange         `json:"range"`
	SelectionRange lspRange         `json:"selectionRange"`
	Children       []documentSymbol `json:"children,omitempty"`
}

type symbolInformation struct {
	Name          string      `json:"name"`
	Kind          int         `json:"kind"`
	Location      lspLocation `json:"location"`
	ContainerName string      `json:"containerName,omitempty"`
}

// decodeDocumentSymbols flattens gopls's documentSymbol response. gopls
// returns DocumentSymbol[] (hierarchical) when the client claims
// hierarchicalDocumentSymbolSupport — which the broker does (see
// lsp.go). The flatten is depth-first so containers precede their
// children in the output, matching how an agent would read top-down.
func decodeDocumentSymbols(raw json.RawMessage, abs, projectRoot string) ([]SymbolRecord, bool) {
	var symbols []documentSymbol
	if err := json.Unmarshal(raw, &symbols); err != nil {
		// gopls may also return SymbolInformation[]. Try that shape.
		var alt []symbolInformation
		if err2 := json.Unmarshal(raw, &alt); err2 != nil {
			return nil, false
		}
		out := make([]SymbolRecord, 0, len(alt))
		for _, s := range alt {
			out = append(out, symbolInfoToRecord(s, projectRoot))
		}
		return out, true
	}
	out := make([]SymbolRecord, 0, len(symbols))
	rel := relPath(abs, projectRoot)
	var walk func(s documentSymbol, container string)
	walk = func(s documentSymbol, container string) {
		out = append(out, SymbolRecord{
			Name:      s.Name,
			Kind:      lspKindName(s.Kind),
			Container: container,
			Detail:    s.Detail,
			Path:      rel,
			Line:      s.SelectionRange.Start.Line + 1,
			Col:       s.SelectionRange.Start.Character + 1,
			EndLine:   s.Range.End.Line + 1,
			EndCol:    s.Range.End.Character + 1,
		})
		// Children inherit the parent name as container — useful for
		// methods on a type, fields on a struct.
		next := s.Name
		if container != "" {
			next = container + "." + s.Name
		}
		for _, c := range s.Children {
			walk(c, next)
		}
	}
	for _, s := range symbols {
		walk(s, "")
	}
	return out, true
}

func decodeSymbolInformation(raw json.RawMessage, projectRoot string) ([]SymbolRecord, bool) {
	var symbols []symbolInformation
	if err := json.Unmarshal(raw, &symbols); err != nil {
		return nil, false
	}
	out := make([]SymbolRecord, 0, len(symbols))
	for _, s := range symbols {
		out = append(out, symbolInfoToRecord(s, projectRoot))
	}
	return out, true
}

func symbolInfoToRecord(s symbolInformation, projectRoot string) SymbolRecord {
	abs := uriToPath(s.Location.URI)
	return SymbolRecord{
		Name:      s.Name,
		Kind:      lspKindName(s.Kind),
		Container: s.ContainerName,
		Path:      relPath(abs, projectRoot),
		Line:      s.Location.Range.Start.Line + 1,
		Col:       s.Location.Range.Start.Character + 1,
		EndLine:   s.Location.Range.End.Line + 1,
		EndCol:    s.Location.Range.End.Character + 1,
	}
}

// lspKindName maps LSP SymbolKind integers to the names ash emits.
// Anything unrecognized produces "kind<N>" so the wire still carries
// signal.
func lspKindName(k int) string {
	switch k {
	case 1:
		return "file"
	case 2:
		return "module"
	case 3:
		return "namespace"
	case 4:
		return "package"
	case 5:
		return "class"
	case 6:
		return "method"
	case 7:
		return "property"
	case 8:
		return "field"
	case 9:
		return "constructor"
	case 10:
		return "enum"
	case 11:
		return "interface"
	case 12:
		return "function"
	case 13:
		return "variable"
	case 14:
		return "constant"
	case 22:
		return "enum_member"
	case 23:
		return "struct"
	case 26:
		return "type_parameter"
	default:
		return fmt.Sprintf("kind%d", k)
	}
}

// ----------------------------------------------------------------------
// pretty rendering

// PrettyResponse renders the lang verb's structured Result. Format
// optimizes for tokens — one symbol per line, kind+name+detail packed
// tight, position appended after a separator the agent can predict.
func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if rsp == nil {
		return ""
	}
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized lang result>"
	}
	var b strings.Builder
	cacheTag := ""
	if r.CacheHit {
		cacheTag = " cache=hit"
	}
	fmt.Fprintf(&b, "§lang %s: %d symbol(s)%s\n", r.Op, len(r.Symbols), cacheTag)
	for _, s := range r.Symbols {
		container := ""
		if s.Container != "" {
			container = s.Container + "."
		}
		detail := ""
		if s.Detail != "" {
			detail = " " + s.Detail
		}
		fmt.Fprintf(&b, "  %-9s %s%s%s  [%s:%d:%d]\n",
			s.Kind, container, s.Name, detail, s.Path, s.Line, s.Col)
	}
	return strings.TrimRight(b.String(), "\n")
}

// ----------------------------------------------------------------------
// path/URI helpers (kept package-local; lsp.go has its own).

func pathToURI(abs string) string {
	u := &url.URL{Scheme: "file", Path: abs}
	return u.String()
}

func uriToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	return u.Path
}

// relPath returns a project-root-relative form when abs is under root;
// otherwise returns abs unchanged. Mirrors the pattern in jail.PrettyPath
// but without the relative-jail policy concerns (the verb only emits
// paths gopls reported, which are already canonical).
func relPath(abs, root string) string {
	if root == "" || !filepath.IsAbs(abs) {
		return abs
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return abs
	}
	return rel
}
