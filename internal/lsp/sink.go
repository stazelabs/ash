package lsp

import (
	"context"
	"sync/atomic"
	"time"
)

// Sink is the indirection that lets write/edit notify the broker without
// importing it directly. The daemon registers the active broker's
// Notify method via SetSink; verb packages call Notify(path) on every
// successful write.
//
// The default (zero-value) sink is a no-op, so tests and non-daemon
// callers (e.g. encexplore, bench harnesses) need no setup.

// sink is the active notifier callback. atomic.Value carries func(path
// string) directly so reads in the hot write/edit path are wait-free.
var sink atomic.Pointer[notifier]

type notifier func(path string)

// SetSink installs fn as the active write/edit notification hook. Pass
// nil to remove the active sink (used by ashd on shutdown to make sure
// no in-flight Notify reaches a half-closed broker).
//
// Safe to call concurrently with Notify.
func SetSink(fn func(path string)) {
	if fn == nil {
		sink.Store(nil)
		return
	}
	n := notifier(fn)
	sink.Store(&n)
}

// Notify fans a successful-write event out to the registered sink, if
// any. It is intentionally non-blocking and best-effort: a slow or
// failing language server must never delay write/edit's return.
//
// path should be the absolute path of the just-written file. The
// callback is invoked synchronously in the caller's goroutine.
func Notify(path string) {
	p := sink.Load()
	if p == nil {
		return
	}
	(*p)(path)
}

// NotifyBroker is the canonical sink installer for a *Broker. The
// returned function dispatches Notify into a background goroutine: the
// first write that triggers Ensure() can take seconds (gopls cold
// start + initialize handshake), and verb latency must NOT include that
// — write/edit return as soon as the file lands, regardless of LSP
// sync progress. Subsequent calls take only the time of a JSON-RPC
// notify write, but we keep the dispatch async for shape consistency.
//
// The detached context carries a 20-second budget so a hung gopls cannot
// pin a goroutine forever.
func NotifyBroker(b *Broker) func(path string) {
	return func(path string) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			b.Notify(ctx, path)
		}()
	}
}
