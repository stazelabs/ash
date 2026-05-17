// Package lang implements the `ash lang` verb — semantic queries served
// by the LSP broker (ASH-136), cached at the per-file level (ASH-137).
//
// Ops:
//
//	outline --path <p>                       per-file symbols
//	def     --symbol <name> [--in <p>]       definition site(s)
//	refs    --symbol <name> [--in <p>]       every reference (call site)
//	callers --symbol <name>                  incoming call-graph (1 level)
//	impl    --interface <name>               implementations of an iface
//
// outline routes to textDocument/documentSymbol and is fully cached on
// (path_abs, mtime_ns, op, args_hash). The other four are workspace-
// scoped — gopls's answer depends on workspace state, which we cannot
// invalidate cleanly via per-file mtime keying. They are intentionally
// uncached today; ASH-141's replay validation will tell us whether a
// workspace-watermark cache layer is worth building.
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
	Op        string
	Path      string
	Symbol    string
	Interface string
	In        string
	MaxRefs   int
	Context   bool
}

// Result is the structured response wire shape.
type Result struct {
	Op       string         `msgpack:"op"`
	Symbols  []SymbolRecord `msgpack:"symbols,omitempty"`
	CacheHit bool           `msgpack:"cache_hit,omitempty"`
}

// SymbolRecord is the unified row type for outline / def / refs / callers
// / impl. Different ops populate different subsets — Kind and Detail are
// empty for refs (where there is no symbol; only a position), Container
// carries the caller name for callers, and so on.
//
// Positions are 1-based to match the rest of the ash verb surface; LSP
// wire ranges are 0-based and converted on the way out.
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
	// ContextLine is the single line of source at Line (for refs/callers
	// when --context=true). Trimmed of leading whitespace; capped at
	// ~200 chars so a single long line cannot blow the response.
	ContextLine string `msgpack:"context_line,omitempty"`
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
	"op": {}, "path": {}, "symbol": {}, "interface": {},
	"in": {}, "max": {}, "context": {},
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
	case "outline", "def", "refs", "callers", "impl":
	default:
		return nil, &proto.Error{Code: "args", Msg: "unknown op: " + a.Op, Hint: "valid ops: outline, def, refs, callers, impl"}
	}
	if a.Path, perr = argutil.OptionalString(in, "path", ""); perr != nil {
		return nil, perr
	}
	if a.Symbol, perr = argutil.OptionalString(in, "symbol", ""); perr != nil {
		return nil, perr
	}
	if a.Interface, perr = argutil.OptionalString(in, "interface", ""); perr != nil {
		return nil, perr
	}
	if a.In, perr = argutil.OptionalString(in, "in", ""); perr != nil {
		return nil, perr
	}
	if a.MaxRefs, perr = argutil.OptionalPosInt(in, "max", 256, 4096); perr != nil {
		return nil, perr
	}
	if a.Context, perr = argutil.OptionalBool(in, "context", true); perr != nil {
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
	case "def", "refs", "callers":
		if a.Symbol == "" {
			return nil, &proto.Error{Code: "args", Msg: a.Op + " requires --symbol"}
		}
	case "impl":
		if a.Interface == "" {
			return nil, &proto.Error{Code: "args", Msg: "impl requires --interface"}
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
	case "refs":
		return runRefs(d, a)
	case "callers":
		return runCallers(d, a)
	case "impl":
		return runImpl(d, a)
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

// resolveSymbolPosition takes a symbol name and returns a (uri, line, col)
// suitable for the position-based LSP methods (references, callHierarchy,
// implementation). The lookup uses workspace/symbol filtered to an exact
// name match, optionally constrained to a single file via inFile.
//
// Returns lsp_not_found if no exact-name match exists, or lsp_ambiguous
// when multiple matches survive the inFile filter — those cases need a
// caller decision ("which Foo?") rather than a silent pick.
func resolveSymbolPosition(ctx context.Context, d Deps, name, inFile string) (string, int, int, *proto.Error) {
	var raw json.RawMessage
	if err := d.Broker.Request(ctx, "workspace/symbol", map[string]any{
		"query": name,
	}, &raw); err != nil {
		return "", 0, 0, brokerError(err)
	}
	var symbols []symbolInformation
	if err := json.Unmarshal(raw, &symbols); err != nil {
		return "", 0, 0, &proto.Error{Code: "lsp_decode", Msg: err.Error()}
	}
	matched := matched[:0:0]
	for _, s := range symbols {
		if s.Name != name {
			continue
		}
		if inFile != "" {
			absIn, _ := filepath.Abs(inFile)
			absSym := uriToPath(s.Location.URI)
			if absSym != absIn {
				continue
			}
		}
		matched = append(matched, s)
	}
	switch len(matched) {
	case 0:
		hint := ""
		if inFile != "" {
			hint = "--in narrowed the search to one file; try without it"
		}
		return "", 0, 0, &proto.Error{Code: "lsp_not_found", Msg: "no exact match for symbol " + name, Hint: hint}
	case 1:
	default:
		paths := make([]string, 0, len(matched))
		for _, s := range matched {
			paths = append(paths, relPath(uriToPath(s.Location.URI), d.ProjectRoot))
		}
		return "", 0, 0, &proto.Error{
			Code: "lsp_ambiguous",
			Msg:  fmt.Sprintf("symbol %q matched %d locations", name, len(matched)),
			Hint: "narrow with --in <path>; candidates: " + strings.Join(paths, ", "),
		}
	}
	loc := matched[0].Location
	return loc.URI, loc.Range.Start.Line, loc.Range.Start.Character, nil
}

// matched is a typed nil sentinel for resolveSymbolPosition's
// accumulator — using a named var keeps the [:0:0] slice trick out of
// the function body where it would just be noise.
var matched []symbolInformation

func runRefs(d Deps, a *Args) (*Result, *proto.Error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	uri, line, col, perr := resolveSymbolPosition(ctx, d, a.Symbol, a.In)
	if perr != nil {
		return nil, perr
	}
	var raw json.RawMessage
	if err := d.Broker.Request(ctx, "textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": line, "character": col},
		"context":      map[string]any{"includeDeclaration": true},
	}, &raw); err != nil {
		return nil, brokerError(err)
	}
	locs, ok := decodeLocations(raw)
	if !ok {
		return nil, &proto.Error{Code: "lsp_decode", Msg: "could not parse references response"}
	}
	records := locationsToRecords(locs, d.ProjectRoot, a.MaxRefs, a.Context)
	return &Result{Op: "refs", Symbols: records}, nil
}

func runImpl(d Deps, a *Args) (*Result, *proto.Error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	uri, line, col, perr := resolveSymbolPosition(ctx, d, a.Interface, a.In)
	if perr != nil {
		return nil, perr
	}
	var raw json.RawMessage
	if err := d.Broker.Request(ctx, "textDocument/implementation", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": line, "character": col},
	}, &raw); err != nil {
		return nil, brokerError(err)
	}
	locs, ok := decodeLocations(raw)
	if !ok {
		return nil, &proto.Error{Code: "lsp_decode", Msg: "could not parse implementation response"}
	}
	records := locationsToRecords(locs, d.ProjectRoot, a.MaxRefs, a.Context)
	for i := range records {
		records[i].Kind = "impl"
	}
	return &Result{Op: "impl", Symbols: records}, nil
}

func runCallers(d Deps, a *Args) (*Result, *proto.Error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	uri, line, col, perr := resolveSymbolPosition(ctx, d, a.Symbol, a.In)
	if perr != nil {
		return nil, perr
	}
	// Step 1: prepareCallHierarchy → CallHierarchyItem[]. gopls returns
	// the item(s) at the cursor; pick the first.
	var prepRaw json.RawMessage
	if err := d.Broker.Request(ctx, "textDocument/prepareCallHierarchy", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": line, "character": col},
	}, &prepRaw); err != nil {
		return nil, brokerError(err)
	}
	var items []callHierarchyItem
	if err := json.Unmarshal(prepRaw, &items); err != nil || len(items) == 0 {
		return &Result{Op: "callers", Symbols: nil}, nil
	}
	// Step 2: incomingCalls(item) → CallHierarchyIncomingCall[].
	var inRaw json.RawMessage
	if err := d.Broker.Request(ctx, "callHierarchy/incomingCalls", map[string]any{
		"item": items[0],
	}, &inRaw); err != nil {
		return nil, brokerError(err)
	}
	var incoming []callHierarchyIncomingCall
	if err := json.Unmarshal(inRaw, &incoming); err != nil {
		return nil, &proto.Error{Code: "lsp_decode", Msg: err.Error()}
	}
	out := make([]SymbolRecord, 0, len(incoming))
	for _, ic := range incoming {
		// Each fromRange marks a call site inside ic.From. Emit one row
		// per call site so a caller that invokes the target twice shows
		// both lines. Cap by a.MaxRefs.
		for _, fr := range ic.FromRanges {
			if len(out) >= a.MaxRefs {
				break
			}
			rec := SymbolRecord{
				Name:      ic.From.Name,
				Kind:      lspKindName(ic.From.Kind),
				Container: ic.From.Detail,
				Path:      relPath(uriToPath(ic.From.URI), d.ProjectRoot),
				Line:      fr.Start.Line + 1,
				Col:       fr.Start.Character + 1,
				EndLine:   fr.End.Line + 1,
				EndCol:    fr.End.Character + 1,
			}
			if a.Context {
				rec.ContextLine = readContextLine(uriToPath(ic.From.URI), fr.Start.Line)
			}
			out = append(out, rec)
		}
		if len(out) >= a.MaxRefs {
			break
		}
	}
	return &Result{Op: "callers", Symbols: out}, nil
}

// decodeLocations parses textDocument/references and
// textDocument/implementation responses, both of which are Location[].
// A null result (no references / no implementations) decodes to an
// empty slice with ok=true so callers do not need a special path.
func decodeLocations(raw json.RawMessage) ([]lspLocation, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}
	var locs []lspLocation
	if err := json.Unmarshal(raw, &locs); err != nil {
		return nil, false
	}
	return locs, true
}

func locationsToRecords(locs []lspLocation, projectRoot string, maxRefs int, includeContext bool) []SymbolRecord {
	if maxRefs <= 0 {
		maxRefs = len(locs)
	}
	if len(locs) > maxRefs {
		locs = locs[:maxRefs]
	}
	out := make([]SymbolRecord, 0, len(locs))
	for _, l := range locs {
		abs := uriToPath(l.URI)
		rec := SymbolRecord{
			Path:    relPath(abs, projectRoot),
			Line:    l.Range.Start.Line + 1,
			Col:     l.Range.Start.Character + 1,
			EndLine: l.Range.End.Line + 1,
			EndCol:  l.Range.End.Character + 1,
		}
		if includeContext {
			rec.ContextLine = readContextLine(abs, l.Range.Start.Line)
		}
		out = append(out, rec)
	}
	return out
}

// readContextLine returns the (0-based) line at path, trimmed of
// leading whitespace and capped at 200 chars. Empty string on any
// failure — context is best-effort decoration, not load-bearing.
func readContextLine(path string, line int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// Walk to the target line via a byte scan; cheaper than splitting
	// the whole file on a workspace-wide refs sweep.
	var start, idx int
	for i := 0; i < line; i++ {
		nl := bytesIndexByteFrom(data, '\n', start)
		if nl < 0 {
			return ""
		}
		start = nl + 1
		idx++
	}
	end := bytesIndexByteFrom(data, '\n', start)
	if end < 0 {
		end = len(data)
	}
	raw := strings.TrimLeft(string(data[start:end]), " \t")
	if len(raw) > 200 {
		raw = raw[:200] + "…"
	}
	return raw
}

func bytesIndexByteFrom(buf []byte, c byte, from int) int {
	if from >= len(buf) {
		return -1
	}
	for i := from; i < len(buf); i++ {
		if buf[i] == c {
			return i
		}
	}
	return -1
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

// callHierarchyItem mirrors LSP §3.16.2. We capture the fields we
// surface back to the agent + the opaque Data blob gopls round-trips
// to itself between prepareCallHierarchy and incomingCalls.
type callHierarchyItem struct {
	Name           string          `json:"name"`
	Kind           int             `json:"kind"`
	Tags           []int           `json:"tags,omitempty"`
	Detail         string          `json:"detail,omitempty"`
	URI            string          `json:"uri"`
	Range          lspRange        `json:"range"`
	SelectionRange lspRange        `json:"selectionRange"`
	Data           json.RawMessage `json:"data,omitempty"`
}

type callHierarchyIncomingCall struct {
	From       callHierarchyItem `json:"from"`
	FromRanges []lspRange        `json:"fromRanges"`
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
	fmt.Fprintf(&b, "§lang %s: %d row(s)%s\n", r.Op, len(r.Symbols), cacheTag)
	for _, s := range r.Symbols {
		container := ""
		if s.Container != "" {
			container = s.Container + "."
		}
		detail := ""
		if s.Detail != "" {
			detail = " " + s.Detail
		}
		// refs rows have no Name — emit just path:line:col + ctx.
		switch r.Op {
		case "refs", "impl":
			if s.ContextLine != "" {
				fmt.Fprintf(&b, "  %s:%d:%d  %s\n", s.Path, s.Line, s.Col, s.ContextLine)
			} else {
				fmt.Fprintf(&b, "  %s:%d:%d\n", s.Path, s.Line, s.Col)
			}
		default:
			label := strings.TrimSpace(container + s.Name + detail)
			if r.Op == "callers" && s.ContextLine != "" {
				fmt.Fprintf(&b, "  %-9s %s  [%s:%d:%d]  %s\n",
					s.Kind, label, s.Path, s.Line, s.Col, s.ContextLine)
			} else {
				fmt.Fprintf(&b, "  %-9s %s  [%s:%d:%d]\n",
					s.Kind, label, s.Path, s.Line, s.Col)
			}
		}
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
