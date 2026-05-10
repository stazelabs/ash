package bench

import (
	"os"
	"runtime"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/stazelabs/ash/internal/proto"
)

// Provenance is the run-context metadata persisted alongside each
// `ash bench` invocation. It exists so a bench row from six months ago
// is interpretable: which ash, which case set, which repo state, which
// machine.
type Provenance struct {
	AshVersion     string
	AshCommitSHA   string
	CaseSetVersion string
	RepoSHA        string
	RepoDirty      bool
	Hostname       string
	CPUCount       int
	DaemonUptimeUs int64
}

// CaptureProvenance fills a Provenance for the calling daemon. The
// projectRoot is the path the daemon serves; readRepoSHA opens it as a
// git repo to extract HEAD and worktree-dirty state. All fields are
// best-effort — if a lookup fails the field is left zero rather than
// failing the bench.
//
// During dogfooding the project root IS the ash repo, so AshCommitSHA
// and RepoSHA are identical. When ash is installed elsewhere they
// diverge: AshVersion is then the only authoritative version axis.
func CaptureProvenance(daemonStart time.Time, projectRoot string) Provenance {
	p := Provenance{
		AshVersion:     proto.AshVersion,
		CaseSetVersion: CaseSetVersion(),
		CPUCount:       runtime.NumCPU(),
		DaemonUptimeUs: time.Since(daemonStart).Microseconds(),
	}
	if h, err := os.Hostname(); err == nil {
		p.Hostname = h
	}
	if sha, dirty, ok := readRepoSHA(projectRoot); ok {
		p.RepoSHA = sha
		p.RepoDirty = dirty
		p.AshCommitSHA = sha
	}
	return p
}

// readRepoSHA opens projectRoot as a git repo and returns (HEAD SHA,
// worktree dirty, ok). Returns ok=false on any failure (no repo,
// detached/initial state, worktree error).
func readRepoSHA(projectRoot string) (sha string, dirty bool, ok bool) {
	repo, err := git.PlainOpenWithOptions(projectRoot, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return "", false, false
	}
	head, err := repo.Head()
	if err != nil {
		return "", false, false
	}
	sha = head.Hash().String()

	wt, err := repo.Worktree()
	if err != nil {
		return sha, false, true
	}
	st, err := wt.Status()
	if err != nil {
		return sha, false, true
	}
	dirty = !st.IsClean()
	return sha, dirty, true
}
