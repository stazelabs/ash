package git

// Helpers shared by every gogit_*.go op: repository opening with the
// idiomatic detect-dotgit traversal, error mapping that matches the
// shellout backend's error codes, and a few small predicates.

import (
	"errors"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/proto"
)

// openRepo opens a repository rooted at or above path. DetectDotGit lets
// callers pass any path inside the work tree, matching shellout's
// `git -C <path>` behavior.
func openRepo(path string) (*git.Repository, *proto.Error) {
	repo, err := git.PlainOpenWithOptions(path, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return nil, repoOpenError(path, err)
	}
	return repo, nil
}

// repoOpenError maps a PlainOpen error into the verb's typed code set.
// not_a_repo is the most common case the agent needs to distinguish.
func repoOpenError(path string, err error) *proto.Error {
	if errors.Is(err, git.ErrRepositoryNotExists) {
		return &proto.Error{Code: "not_a_repo", Msg: jail.PrettyPath(path) + " is not inside a git repository"}
	}
	return &proto.Error{Code: "git_failed", Msg: err.Error()}
}

// gogitRefError maps a ResolveRevision / CommitObject failure for a
// user-supplied ref into ref_not_found, matching the shellout shape.
func gogitRefError(ref string, err error) *proto.Error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "reference not found") ||
		strings.Contains(lower, "object not found") ||
		strings.Contains(lower, "unknown revision") ||
		strings.Contains(lower, "ambiguous argument") {
		return &proto.Error{Code: "ref_not_found", Msg: ref + ": " + msg}
	}
	return &proto.Error{Code: "git_failed", Msg: msg}
}
