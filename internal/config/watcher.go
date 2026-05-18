package config

import (
	"os"
	"sync"
	"time"
)

// Watcher tracks the mtimes (and sizes, for robustness on
// sub-second-resolution filesystems) of the config sources Load
// consults so the daemon can hot-reload enforcement-layer config
// without a restart (ASH-164).
//
// Refresh stat's the candidate source files (global, project,
// $ASH_CONFIG) and re-runs Load if any of their (mtime, size)
// signatures changed since the last successful refresh, OR if a
// previously-absent file now exists, OR if a previously-present file
// is now gone. The (mtime, size) pair guards against the same-second
// rewrite case the bare-mtime check would miss.
//
// Refresh is goroutine-safe — the daemon's per-request handler may
// fire it concurrently from many handler goroutines. The cost when
// nothing has changed is one stat per candidate path (microseconds;
// dwarfed by every other per-request cost).
//
// Soft-fail on parse error: a half-edited TOML file leaves the
// daemon on the previous config and surfaces the error to the
// caller, rather than panicking or hot-loading garbage.
type Watcher struct {
	root   string
	mu     sync.Mutex
	cfg    *Config
	source string
	state  map[string]fileState
}

// fileState is the (mtime, size) signature of one candidate config
// path. Zero-value means the file does not exist (or could not be
// stat'd) at the last refresh.
type fileState struct {
	mtime time.Time
	size  int64
}

// NewWatcher returns a Watcher seeded from Load(root). The returned
// *Config is the same value Load would have returned and should be
// used as the daemon's initial enforcement-layer config. The source
// label matches Load's third return value.
func NewWatcher(root string) (*Watcher, *Config, string, error) {
	cfg, source, err := Load(root)
	if err != nil {
		return nil, nil, "", err
	}
	w := &Watcher{
		root:   root,
		cfg:    cfg,
		source: source,
		state:  currentStates(root),
	}
	return w, cfg, source, nil
}

// Refresh stats the candidate config files; if any (mtime, size)
// signature changed, or a file appeared / disappeared, it re-runs
// Load and returns the fresh Config with changed=true. Otherwise
// returns the cached config with changed=false.
//
// On a Load error after a detected change, returns the previously-
// cached config with changed=false and the error — callers should
// log and continue on the old config rather than failing the
// dispatch path on a syntax error in ash.toml.
func (w *Watcher) Refresh() (cfg *Config, source string, changed bool, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	current := currentStates(w.root)
	if statesEqual(current, w.state) {
		return w.cfg, w.source, false, nil
	}
	newCfg, newSource, lerr := Load(w.root)
	if lerr != nil {
		return w.cfg, w.source, false, lerr
	}
	w.cfg = newCfg
	w.source = newSource
	w.state = current
	return newCfg, newSource, true, nil
}

// Config returns the currently-cached config without checking the
// filesystem. Use Refresh when you need fresh state.
func (w *Watcher) Config() *Config {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cfg
}

// candidatePaths is the ordered list of files Load consults. The
// project file is always checked; global and $ASH_CONFIG are
// included when relevant. Re-evaluated on every refresh so a session
// that exports ASH_CONFIG mid-run picks it up too.
func candidatePaths(root string) []string {
	paths := []string{GlobalPath(), ProjectPath(root)}
	if override := os.Getenv("ASH_CONFIG"); override != "" {
		paths = append(paths, override)
	}
	return paths
}

// currentStates returns the (mtime, size) signature for each
// candidate path. Missing files map to zero-value fileState — a
// future appearance is detected as a non-zero signature.
func currentStates(root string) map[string]fileState {
	paths := candidatePaths(root)
	out := make(map[string]fileState, len(paths))
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			out[p] = fileState{}
			continue
		}
		out[p] = fileState{mtime: st.ModTime(), size: st.Size()}
	}
	return out
}

func statesEqual(a, b map[string]fileState) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok {
			return false
		}
		if av.size != bv.size || !av.mtime.Equal(bv.mtime) {
			return false
		}
	}
	return true
}
