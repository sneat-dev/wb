// Package gitclitest is the in-memory fake for internal/gitcli.Git, so a
// unit test can substitute it for a real gitcli.Client without touching a
// real repository. internal/gitcli/contract_test.go (//go:build e2e) runs
// the same cases against both, proving the fake does not diverge from real
// git for the cases it emulates.
package gitclitest

import (
	"context"

	"github.com/sneat-dev/wb/internal/gitcli"
)

// Fake is a scriptable gitcli.Git: each method looks up dir (and, for
// IsAncestor, the ancestor/descendant pair) in a map the test populates
// directly, and fails loudly via panic on an unscripted call, so a test
// omission is caught immediately rather than answering with a silent zero
// value.
type Fake struct {
	// CurrentBranchByDir maps a worktree directory to CurrentBranch's
	// canned (branch, error) result.
	CurrentBranchByDir map[string]Result
	// RevParseByDirAndRev maps "dir\x00rev" to RevParse's canned result.
	RevParseByDirAndRev map[string]Result
	// IsAncestorByCase maps "dir\x00ancestor\x00descendant" to IsAncestor's
	// canned (bool, error) result.
	IsAncestorByCase map[string]BoolResult
	// FetchErrByDirAndRemote maps "dir\x00remote" to Fetch's canned error.
	FetchErrByDirAndRemote map[string]error
}

var _ gitcli.Git = (*Fake)(nil)

// Result is one string-returning method's canned outcome.
type Result struct {
	Value string
	Err   error
}

// BoolResult is IsAncestor's canned outcome.
type BoolResult struct {
	Value bool
	Err   error
}

func key(parts ...string) string {
	joined := parts[0]
	for _, part := range parts[1:] {
		joined += "\x00" + part
	}
	return joined
}

// CurrentBranch implements gitcli.Git.
func (f *Fake) CurrentBranch(_ context.Context, dir string) (string, error) {
	result, ok := f.CurrentBranchByDir[dir]
	if !ok {
		panic("gitclitest.Fake: CurrentBranch not scripted for dir " + dir)
	}
	return result.Value, result.Err
}

// RevParse implements gitcli.Git.
func (f *Fake) RevParse(_ context.Context, dir, rev string) (string, error) {
	result, ok := f.RevParseByDirAndRev[key(dir, rev)]
	if !ok {
		panic("gitclitest.Fake: RevParse not scripted for dir " + dir + " rev " + rev)
	}
	return result.Value, result.Err
}

// IsAncestor implements gitcli.Git.
func (f *Fake) IsAncestor(_ context.Context, dir, ancestor, descendant string) (bool, error) {
	result, ok := f.IsAncestorByCase[key(dir, ancestor, descendant)]
	if !ok {
		panic("gitclitest.Fake: IsAncestor not scripted for " + key(dir, ancestor, descendant))
	}
	return result.Value, result.Err
}

// Fetch implements gitcli.Git.
func (f *Fake) Fetch(_ context.Context, dir, remote string) error {
	err, ok := f.FetchErrByDirAndRemote[key(dir, remote)]
	if !ok {
		panic("gitclitest.Fake: Fetch not scripted for dir " + dir + " remote " + remote)
	}
	return err
}
