// Package gitclitest is the in-memory fake for internal/gitcli.Git, so a
// unit test can substitute it for a real gitcli.Client without touching a
// real repository. internal/gitcli/contract_test.go (//go:build e2e) runs
// the same cases against both, proving the fake does not diverge from real
// git for the cases it emulates.
//
// Fake also has task-8's fail-call-N mode (decision 23), mirrored from
// internal/runner/runnertest.Fake: FailCall makes one chosen call fail on
// its own, standalone, and Fake implements internal/testsweep.Failer so
// internal/testsweep.Sweep can drive it once per call a happy-path body
// makes. See CallCount and FailCall, and gitclitest_test.go's
// multiCallConsumer for a worked example.
package gitclitest

import (
	"context"
	"sync"

	"github.com/sneat-dev/wb/internal/gitcli"
	"github.com/sneat-dev/wb/internal/testsweep"
)

// Fake is a scriptable gitcli.Git: each method looks up dir (and, for
// IsAncestor, the ancestor/descendant pair) in a map the test populates
// directly, and fails loudly via panic on an unscripted call, so a test
// omission is caught immediately rather than answering with a silent zero
// value.
//
// Fake also has task-8's fail-call-N mode (decision 23): FailCall makes one
// chosen call number fail regardless of its scripted result, on its own or
// driven by internal/testsweep.Sweep across every call a happy path makes.
// See CallCount and FailCall.
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

	mu      sync.Mutex
	calls   int
	failAt  int // 1-indexed call number FailCall targets; 0 disables it.
	failErr error
}

var _ gitcli.Git = (*Fake)(nil)
var _ testsweep.Failer = (*Fake)(nil)

// CallCount reports how many calls the Fake has answered so far, across
// CurrentBranch, RevParse, IsAncestor and Fetch combined, in the order it
// answered them. It implements internal/testsweep.Failer.
func (f *Fake) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// FailCall arranges for the callNum'th call (1-indexed, counting
// CurrentBranch, RevParse, IsAncestor and Fetch together in the order the
// Fake answers them) to fail with failErr instead of returning its scripted
// result; every other call keeps returning its own scripted result
// unchanged, so FailCall fails exactly call N and passes the rest. The call
// FailCall targets must still be scripted (present in the relevant map)
// first -- FailCall replaces that call's result, not the lookup that
// catches an unscripted call.
//
// It works standalone, or driven once per call number by
// internal/testsweep.Sweep, which is why Fake implements
// internal/testsweep.Failer. A callNum of 0 disables the override; calling
// FailCall again replaces the previous target rather than adding a second
// one.
func (f *Fake) FailCall(callNum int, failErr error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failAt = callNum
	f.failErr = failErr
}

// record counts one answered call and reports whether FailCall last
// targeted it; when it did, the caller returns failErr in place of its own
// scripted result.
func (f *Fake) record() (failErr error, fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failAt != 0 && f.calls == f.failAt {
		return f.failErr, true
	}
	return nil, false
}

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
	if failErr, fail := f.record(); fail {
		return "", failErr
	}
	return result.Value, result.Err
}

// RevParse implements gitcli.Git.
func (f *Fake) RevParse(_ context.Context, dir, rev string) (string, error) {
	result, ok := f.RevParseByDirAndRev[key(dir, rev)]
	if !ok {
		panic("gitclitest.Fake: RevParse not scripted for dir " + dir + " rev " + rev)
	}
	if failErr, fail := f.record(); fail {
		return "", failErr
	}
	return result.Value, result.Err
}

// IsAncestor implements gitcli.Git.
func (f *Fake) IsAncestor(_ context.Context, dir, ancestor, descendant string) (bool, error) {
	result, ok := f.IsAncestorByCase[key(dir, ancestor, descendant)]
	if !ok {
		panic("gitclitest.Fake: IsAncestor not scripted for " + key(dir, ancestor, descendant))
	}
	if failErr, fail := f.record(); fail {
		return false, failErr
	}
	return result.Value, result.Err
}

// Fetch implements gitcli.Git.
func (f *Fake) Fetch(_ context.Context, dir, remote string) error {
	err, ok := f.FetchErrByDirAndRemote[key(dir, remote)]
	if !ok {
		panic("gitclitest.Fake: Fetch not scripted for dir " + dir + " remote " + remote)
	}
	if failErr, fail := f.record(); fail {
		return failErr
	}
	return err
}
