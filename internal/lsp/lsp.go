// Package lsp is the ashd-owned broker for Language Server Protocol
// subprocesses (today: gopls only, ASH-136). It is daemon infrastructure;
// no user-facing verb consumes it yet.
//
// Responsibilities:
//
//   - Spawn the language server lazily on the first Ensure call.
//   - Drive the LSP initialize / initialized handshake.
//   - Route textDocument/didOpen and didChange notifications fired by
//     ash write and ash edit (via package lsp's Notify entry point)
//     so the server's in-memory view stays in sync with disk.
//   - Provide a typed Request method for verbs that issue LSP requests
//     (documentSymbol, definition, references, ...).
//   - Re-spawn gopls if it dies mid-session; bail loudly after repeated
//     failure with a stable error code instead of crashing the daemon.
//   - Shut down cleanly when the daemon exits — no orphan gopls.
//
// Wire framing follows LSP §3.1: an HTTP-like Content-Length header,
// CRLF separator, then a JSON-RPC 2.0 body. We carry just enough of the
// protocol to satisfy gopls; the JSON-RPC types here are deliberately
// minimal — adding methods is a matter of marshaling params and decoding
// the result, not extending a generated client.
package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config is the broker's runtime configuration. Root must be the
// absolute project root used by ashd; the broker uses it as the LSP
// workspace folder. GoplsPath defaults to "gopls" (resolved via $PATH)
// when empty.
type Config struct {
	Enabled   bool
	GoplsPath string
	Root      string
}

// Broker manages a single gopls subprocess. The zero value is not
// useful; construct one via New. Broker is safe for concurrent use:
// all wire writes are serialized through writeMu, and the reader
// goroutine fans incoming responses to pending channels indexed by
// JSON-RPC ID.
type Broker struct {
	cfg Config

	mu          sync.Mutex
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stderrCopy  *strings.Builder
	stderrPipe  io.ReadCloser
	started     bool
	closed      bool
	initDur     time.Duration
	initStarted time.Time

	writeMu sync.Mutex
	nextID  atomic.Int64

	pendMu  sync.Mutex
	pending map[int64]chan json.RawMessage
	pendErr map[int64]chan *rpcError

	openMu sync.Mutex
	open   map[string]int // path -> version (1 = didOpen sent; >1 = didChange'd)

	failMu              sync.Mutex
	failCount           int
	failWindow          time.Time
	permanentlyDisabled bool

	onInit     func(d time.Duration, err error)
	logger     *log.Logger
	readerDone chan struct{}
}

// Option configures a Broker at construction time.
type Option func(*Broker)

// WithInitCallback installs a callback fired exactly once per successful
// (or failed) gopls initialization. The first arg is the elapsed init
// duration; the second is nil on success. ashd uses this hook to record
// the init-latency row in the ledger so it is queryable separately from
// per-call latency (ASH-136 verification).
func WithInitCallback(fn func(d time.Duration, err error)) Option {
	return func(b *Broker) { b.onInit = fn }
}

// WithLogger swaps the logger used for diagnostic messages (gopls stderr
// drops, re-spawn announcements, etc). Nil keeps the default (stdlib log).
func WithLogger(l *log.Logger) Option {
	return func(b *Broker) { b.logger = l }
}

// New returns a Broker bound to cfg. The subprocess is not spawned until
// the first Ensure call; a disabled broker performs no work and answers
// every method with lsp_disabled or a no-op.
func New(cfg Config, opts ...Option) *Broker {
	b := &Broker{
		cfg:     cfg,
		pending: map[int64]chan json.RawMessage{},
		pendErr: map[int64]chan *rpcError{},
		open:    map[string]int{},
		logger:  log.Default(),
	}
	for _, o := range opts {
		o(b)
	}
	if b.cfg.GoplsPath == "" {
		b.cfg.GoplsPath = "gopls"
	}
	return b
}

// Enabled reports whether the broker would attempt to spawn gopls. A
// disabled broker is still safe to call — Notify and Request are no-ops
// or return lsp_disabled — but Ensure does no work.
func (b *Broker) Enabled() bool { return b != nil && b.cfg.Enabled }

// LastInit returns the most recent successful init duration. Zero before
// the first successful Ensure.
func (b *Broker) LastInit() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.initDur
}

// Ensure lazily starts gopls and completes the initialize handshake. On
// subsequent calls, returns nil immediately when the subprocess is alive.
// If gopls died since the last call, Ensure attempts a re-spawn; if the
// re-spawn quota is exhausted it returns an error with code lsp_disabled.
func (b *Broker) Ensure(ctx context.Context) error {
	if !b.Enabled() {
		return &Error{Code: "lsp_disabled", Msg: "lsp broker is disabled"}
	}
	b.failMu.Lock()
	disabled := b.permanentlyDisabled
	b.failMu.Unlock()
	if disabled {
		return &Error{Code: "lsp_disabled", Msg: "lsp broker disabled after repeated init failures"}
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return &Error{Code: "lsp_closed", Msg: "lsp broker is closed"}
	}
	if b.started && b.alive() {
		b.mu.Unlock()
		return nil
	}
	// Stale state from a previous run — clean up before respawning.
	b.resetLocked()
	b.mu.Unlock()

	if err := b.start(ctx); err != nil {
		b.recordFailure()
		if b.onInit != nil {
			b.onInit(0, err)
		}
		return err
	}
	if b.onInit != nil {
		b.onInit(b.LastInit(), nil)
	}
	return nil
}

// Notify is the write/edit hook entry point. The first call for a path
// emits textDocument/didOpen with the current file contents; subsequent
// calls emit textDocument/didChange (full-document sync). Errors are
// logged and swallowed — a misbehaving language server must not propagate
// back into the write verb's success path.
//
// Notify is a no-op when the broker is disabled, closed, or has not
// completed initialize.
func (b *Broker) Notify(ctx context.Context, path string) {
	if !b.Enabled() {
		return
	}
	if err := b.Ensure(ctx); err != nil {
		b.logger.Printf("ashd lsp: Ensure: %v", err)
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		b.logger.Printf("ashd lsp: abs(%s): %v", path, err)
		return
	}
	// Only sync files gopls cares about. textDocument/didChange on
	// non-Go files is harmless but wastes wire and ledger.
	if !goplsRelevant(abs) {
		return
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		// File may have been written and immediately deleted in the
		// same op; skip silently.
		return
	}
	b.openMu.Lock()
	ver, isOpen := b.open[abs]
	ver++
	b.open[abs] = ver
	b.openMu.Unlock()

	uri := pathToURI(abs)
	if !isOpen {
		if err := b.notify("textDocument/didOpen", didOpenParams{
			TextDocument: textDocumentItem{
				URI:        uri,
				LanguageID: languageIDForPath(abs),
				Version:    ver,
				Text:       string(data),
			},
		}); err != nil {
			b.logger.Printf("ashd lsp: didOpen %s: %v", abs, err)
		}
		return
	}
	if err := b.notify("textDocument/didChange", didChangeParams{
		TextDocument: versionedTextDocumentIdentifier{URI: uri, Version: ver},
		ContentChanges: []contentChange{
			{Text: string(data)},
		},
	}); err != nil {
		b.logger.Printf("ashd lsp: didChange %s: %v", abs, err)
	}
}

// Request issues a JSON-RPC request to gopls and waits for the response.
// params and result may be any json-marshalable / json-unmarshalable
// value. result may be nil when the caller does not care about the
// response body. The context bounds the wait — a cancelled ctx unblocks
// the call but does NOT cancel gopls's view of the request (LSP $/cancel
// is not implemented today).
func (b *Broker) Request(ctx context.Context, method string, params, result any) error {
	if err := b.Ensure(ctx); err != nil {
		return err
	}
	id := b.nextID.Add(1)
	rc := make(chan json.RawMessage, 1)
	ec := make(chan *rpcError, 1)
	b.pendMu.Lock()
	b.pending[id] = rc
	b.pendErr[id] = ec
	b.pendMu.Unlock()
	defer func() {
		b.pendMu.Lock()
		delete(b.pending, id)
		delete(b.pendErr, id)
		b.pendMu.Unlock()
	}()

	if err := b.writeMsg(rpcMessage{Jsonrpc: "2.0", ID: idValue(id), Method: method, Params: params}); err != nil {
		return &Error{Code: "lsp_write", Msg: err.Error()}
	}

	select {
	case raw := <-rc:
		if result == nil {
			return nil
		}
		if len(raw) == 0 {
			return nil
		}
		if err := json.Unmarshal(raw, result); err != nil {
			return &Error{Code: "lsp_decode", Msg: err.Error()}
		}
		return nil
	case rerr := <-ec:
		return &Error{Code: "lsp_request", Msg: rerr.Message, Hint: fmt.Sprintf("gopls code %d", rerr.Code)}
	case <-ctx.Done():
		return &Error{Code: "lsp_timeout", Msg: ctx.Err().Error()}
	}
}

// Close shuts down gopls cleanly. It sends shutdown + exit with a bounded
// grace, then kills the process if it does not comply. Safe to call
// multiple times; subsequent calls are no-ops.
func (b *Broker) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	cmd := b.cmd
	stdin := b.stdin
	started := b.started
	readerDone := b.readerDone
	b.mu.Unlock()

	if !started || cmd == nil || cmd.Process == nil {
		return nil
	}

	// Best-effort LSP shutdown + exit. We do not wait for the response
	// on shutdown — gopls is required to exit on the exit notification
	// regardless of the shutdown roundtrip.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	_ = b.Request(shutdownCtx, "shutdown", nil, nil)
	cancel()
	_ = b.writeMsg(rpcMessage{Jsonrpc: "2.0", Method: "exit"})
	_ = stdin.Close()

	// Wait briefly for graceful exit; SIGKILL if it hangs.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	if readerDone != nil {
		<-readerDone
	}
	return nil
}

// ----------------------------------------------------------------------
// internals

func (b *Broker) alive() bool {
	if b.cmd == nil || b.cmd.Process == nil {
		return false
	}
	// readLoop closes readerDone when the stdout pipe drops, which
	// implies gopls exited (or is about to). Treat that signal as
	// liveness=false so the next Ensure respawns.
	if b.readerDone != nil {
		select {
		case <-b.readerDone:
			return false
		default:
		}
	}
	if b.cmd.ProcessState != nil && b.cmd.ProcessState.Exited() {
		return false
	}
	return true
}

func (b *Broker) resetLocked() {
	if b.stdin != nil {
		_ = b.stdin.Close()
		b.stdin = nil
	}
	if b.stderrPipe != nil {
		_ = b.stderrPipe.Close()
		b.stderrPipe = nil
	}
	// Reap the previous subprocess if we are respawning after a crash.
	// Without this the OS keeps the process slot around as a zombie
	// until ashd exits; with concurrent crash-loops we would also leak
	// the readerDone channel.
	if b.cmd != nil && b.cmd.Process != nil && b.cmd.ProcessState == nil {
		go func(c *exec.Cmd) { _ = c.Wait() }(b.cmd)
	}
	b.cmd = nil
	b.readerDone = nil
	b.started = false
	b.initDur = 0
	b.openMu.Lock()
	b.open = map[string]int{}
	b.openMu.Unlock()
	b.pendMu.Lock()
	// Any in-flight requests against the dead process will time out via
	// their context; we drop them here so a re-spawn does not reuse the
	// channels.
	b.pending = map[int64]chan json.RawMessage{}
	b.pendErr = map[int64]chan *rpcError{}
	b.pendMu.Unlock()
}

func (b *Broker) recordFailure() {
	b.failMu.Lock()
	defer b.failMu.Unlock()
	now := time.Now()
	if !b.failWindow.IsZero() && now.Sub(b.failWindow) > 30*time.Second {
		b.failCount = 0
	}
	b.failWindow = now
	b.failCount++
	if b.failCount >= 3 {
		b.permanentlyDisabled = true
	}
}

func (b *Broker) start(ctx context.Context) error {
	bin := b.cfg.GoplsPath
	if !filepath.IsAbs(bin) {
		resolved, err := exec.LookPath(bin)
		if err != nil {
			return &Error{Code: "gopls_not_found", Msg: fmt.Sprintf("gopls binary %q not found in $PATH", bin), Hint: "set [lsp].gopls_path in ash.toml or install gopls"}
		}
		bin = resolved
	} else {
		if _, err := os.Stat(bin); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return &Error{Code: "gopls_not_found", Msg: fmt.Sprintf("gopls binary %q does not exist", bin), Hint: "check [lsp].gopls_path in ash.toml"}
			}
			return &Error{Code: "gopls_not_found", Msg: err.Error()}
		}
	}
	cmd := exec.Command(bin)
	cmd.Dir = b.cfg.Root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return &Error{Code: "lsp_spawn", Msg: err.Error()}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return &Error{Code: "lsp_spawn", Msg: err.Error()}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return &Error{Code: "lsp_spawn", Msg: err.Error()}
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		stderr.Close()
		return &Error{Code: "lsp_spawn", Msg: err.Error()}
	}

	b.mu.Lock()
	b.cmd = cmd
	b.stdin = stdin
	b.stderrPipe = stderr
	b.initStarted = time.Now()
	b.readerDone = make(chan struct{})
	b.stderrCopy = &strings.Builder{}
	b.mu.Unlock()

	go b.readLoop(stdout)
	go b.drainStderr(stderr)

	// Drive the initialize handshake. gopls accepts WorkspaceFolders so
	// it can index the project tree.
	initStart := time.Now()
	rootURI := pathToURI(b.cfg.Root)
	pid := os.Getpid()
	initParams := initializeParams{
		ProcessID: &pid,
		RootURI:   rootURI,
		WorkspaceFolders: []workspaceFolder{{
			URI:  rootURI,
			Name: filepath.Base(b.cfg.Root),
		}},
		Capabilities: map[string]any{
			"textDocument": map[string]any{
				"synchronization": map[string]any{
					"didSave":             false,
					"willSave":            false,
					"dynamicRegistration": false,
				},
				"documentSymbol": map[string]any{
					"dynamicRegistration":               false,
					"hierarchicalDocumentSymbolSupport": true,
				},
			},
			"workspace": map[string]any{
				"workspaceFolders": true,
			},
		},
	}
	initCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var initResult json.RawMessage
	if err := b.requestUnchecked(initCtx, "initialize", initParams, &initResult); err != nil {
		_ = b.killPartial()
		return err
	}
	if err := b.notify("initialized", struct{}{}); err != nil {
		_ = b.killPartial()
		return &Error{Code: "lsp_init", Msg: err.Error()}
	}

	b.mu.Lock()
	b.started = true
	b.initDur = time.Since(initStart)
	b.mu.Unlock()
	b.failMu.Lock()
	b.failCount = 0
	b.failMu.Unlock()
	return nil
}

// requestUnchecked is the Request path that does NOT call Ensure — used
// during initialize itself, where Ensure would recurse.
func (b *Broker) requestUnchecked(ctx context.Context, method string, params, result any) error {
	id := b.nextID.Add(1)
	rc := make(chan json.RawMessage, 1)
	ec := make(chan *rpcError, 1)
	b.pendMu.Lock()
	b.pending[id] = rc
	b.pendErr[id] = ec
	b.pendMu.Unlock()
	defer func() {
		b.pendMu.Lock()
		delete(b.pending, id)
		delete(b.pendErr, id)
		b.pendMu.Unlock()
	}()
	if err := b.writeMsg(rpcMessage{Jsonrpc: "2.0", ID: idValue(id), Method: method, Params: params}); err != nil {
		return &Error{Code: "lsp_write", Msg: err.Error()}
	}
	select {
	case raw := <-rc:
		if result == nil {
			return nil
		}
		if len(raw) == 0 {
			return nil
		}
		if rr, ok := result.(*json.RawMessage); ok {
			*rr = append((*rr)[:0], raw...)
			return nil
		}
		return json.Unmarshal(raw, result)
	case rerr := <-ec:
		return &Error{Code: "lsp_request", Msg: rerr.Message, Hint: fmt.Sprintf("gopls code %d", rerr.Code)}
	case <-ctx.Done():
		return &Error{Code: "lsp_timeout", Msg: ctx.Err().Error()}
	}
}

func (b *Broker) killPartial() error {
	b.mu.Lock()
	cmd := b.cmd
	b.resetLocked()
	b.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	return nil
}

func (b *Broker) notify(method string, params any) error {
	return b.writeMsg(rpcMessage{Jsonrpc: "2.0", Method: method, Params: params})
}

func (b *Broker) writeMsg(m rpcMessage) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	if b.stdin == nil {
		return errors.New("lsp: stdin closed")
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(b.stdin, header); err != nil {
		return err
	}
	if _, err := b.stdin.Write(body); err != nil {
		return err
	}
	return nil
}

func (b *Broker) readLoop(stdout io.ReadCloser) {
	defer close(b.readerDone)
	defer stdout.Close()
	br := bufio.NewReaderSize(stdout, 64*1024)
	for {
		body, err := readMessage(br)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				b.logger.Printf("ashd lsp: read: %v", err)
			}
			// Flush pending requests with an error so callers unblock
			// instead of hanging until ctx timeout.
			b.failRemaining(fmt.Errorf("lsp connection closed"))
			return
		}
		b.dispatch(body)
	}
}

func (b *Broker) drainStderr(rd io.ReadCloser) {
	defer rd.Close()
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 8*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		b.mu.Lock()
		if b.stderrCopy != nil && b.stderrCopy.Len() < 16*1024 {
			b.stderrCopy.WriteString(line)
			b.stderrCopy.WriteByte('\n')
		}
		b.mu.Unlock()
	}
}

func (b *Broker) dispatch(body []byte) {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		b.logger.Printf("ashd lsp: decode envelope: %v", err)
		return
	}
	// Notifications from gopls (publishDiagnostics, $/progress, ...)
	// have a method and no id; we don't subscribe today.
	if env.ID == nil {
		return
	}
	id, ok := decodeID(env.ID)
	if !ok {
		return
	}
	b.pendMu.Lock()
	rc := b.pending[id]
	ec := b.pendErr[id]
	b.pendMu.Unlock()
	if rc == nil {
		return
	}
	if env.Error != nil {
		select {
		case ec <- env.Error:
		default:
		}
		return
	}
	raw := env.Result
	if raw == nil {
		raw = json.RawMessage("null")
	}
	select {
	case rc <- raw:
	default:
	}
}

func (b *Broker) failRemaining(err error) {
	b.pendMu.Lock()
	defer b.pendMu.Unlock()
	for id, ec := range b.pendErr {
		select {
		case ec <- &rpcError{Code: -32603, Message: err.Error()}:
		default:
		}
		_ = id
	}
}

// ----------------------------------------------------------------------
// wire framing

// readMessage parses one LSP message off br: header block terminated by
// CRLF CRLF, then Content-Length bytes of body. The body is the JSON-RPC
// 2.0 payload.
func readMessage(br *bufio.Reader) ([]byte, error) {
	var contentLength int
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if cl, ok := strings.CutPrefix(line, "Content-Length: "); ok {
			n, err := strconv.Atoi(strings.TrimSpace(cl))
			if err != nil || n < 0 {
				return nil, fmt.Errorf("invalid Content-Length: %q", line)
			}
			contentLength = n
			continue
		}
		// Other headers (Content-Type) are ignored.
	}
	if contentLength == 0 {
		return nil, errors.New("missing Content-Length")
	}
	buf := make([]byte, contentLength)
	if _, err := io.ReadFull(br, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// ----------------------------------------------------------------------
// JSON-RPC types (minimal)

type rpcMessage struct {
	Jsonrpc string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method,omitempty"`
	Params  any    `json:"params,omitempty"`
}

type envelope struct {
	Jsonrpc string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// idValue wraps an int64 so it marshals to a JSON number (not a string).
func idValue(id int64) any { return id }

func decodeID(raw json.RawMessage) (int64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0, false
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	return n, true
}

// ----------------------------------------------------------------------
// LSP message types (minimal)

type initializeParams struct {
	ProcessID        *int              `json:"processId"`
	RootURI          string            `json:"rootUri"`
	WorkspaceFolders []workspaceFolder `json:"workspaceFolders"`
	Capabilities     map[string]any    `json:"capabilities"`
}

type workspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

type textDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

type didOpenParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
}

type versionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type contentChange struct {
	Text string `json:"text"`
}

type didChangeParams struct {
	TextDocument   versionedTextDocumentIdentifier `json:"textDocument"`
	ContentChanges []contentChange                 `json:"contentChanges"`
}

// ----------------------------------------------------------------------
// Error type. Matches the shape of proto.Error so callers can hand it
// back through the wire envelope without re-wrapping, but we avoid the
// proto import to keep this package leaf-level.

// Error is the broker's typed error. Code is a stable identifier suitable
// for the ledger's err_code column.
type Error struct {
	Code string
	Msg  string
	Hint string
}

func (e *Error) Error() string {
	if e.Hint != "" {
		return e.Code + ": " + e.Msg + " (" + e.Hint + ")"
	}
	return e.Code + ": " + e.Msg
}

// ----------------------------------------------------------------------
// path / URI helpers

// pathToURI returns the file:// URI form of an absolute filesystem path.
// Matches gopls's expected input shape on macOS / Linux.
func pathToURI(abs string) string {
	u := &url.URL{Scheme: "file", Path: abs}
	return u.String()
}

func languageIDForPath(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".go":
		return "go"
	case ".mod":
		return "go.mod"
	case ".sum":
		return "go.sum"
	default:
		return "plaintext"
	}
}

// goplsRelevant reports whether the file extension is something gopls
// will index. Filters out the firehose of writes to non-Go files
// (markdown, TOML, generated artifacts) so we do not pay LSP wire cost
// for them.
func goplsRelevant(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".go", ".mod", ".sum":
		return true
	}
	return false
}
