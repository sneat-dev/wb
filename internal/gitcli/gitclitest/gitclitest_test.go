package gitclitest

import (
	"context"
	"errors"
	"testing"

	"github.com/sneat-dev/wb/internal/gitcli"
	"github.com/sneat-dev/wb/internal/testsweep"
)

// mustPanic runs fn and returns the recovered value. It recovers inside its
// own call frame rather than via a deferred function in the caller, so a
// parallel test can use it without tripping the paralleltest check-cleanup
// rule (defer alongside t.Parallel() in the same function).
func mustPanic(t *testing.T, fn func()) any {
	t.Helper()
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		fn()
	}()
	if recovered == nil {
		t.Fatal("want a panic")
	}
	return recovered
}

func TestFakeCurrentBranchReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{CurrentBranchByDir: map[string]Result{"/repo": {Value: "main"}}}
	branch, err := fake.CurrentBranch(context.Background(), "/repo")
	if err != nil || branch != "main" {
		t.Fatalf("CurrentBranch() = (%q, %v)", branch, err)
	}
}

func TestFakeCurrentBranchPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_, _ = (&Fake{}).CurrentBranch(context.Background(), "/repo")
	})
}

func TestFakeRevParseReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{RevParseByDirAndRev: map[string]Result{key("/repo", "HEAD"): {Value: "abc123"}}}
	sha, err := fake.RevParse(context.Background(), "/repo", "HEAD")
	if err != nil || sha != "abc123" {
		t.Fatalf("RevParse() = (%q, %v)", sha, err)
	}
}

func TestFakeRevParsePanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_, _ = (&Fake{}).RevParse(context.Background(), "/repo", "HEAD")
	})
}

func TestFakeIsAncestorReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{IsAncestorByCase: map[string]BoolResult{key("/repo", "base", "head"): {Value: true, Err: wantErr}}}
	ok, err := fake.IsAncestor(context.Background(), "/repo", "base", "head")
	if !ok || err != wantErr {
		t.Fatalf("IsAncestor() = (%v, %v)", ok, err)
	}
}

func TestFakeIsAncestorPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_, _ = (&Fake{}).IsAncestor(context.Background(), "/repo", "base", "head")
	})
}

func TestFakeFetchReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("network unreachable")
	fake := &Fake{FetchErrByDirAndRemote: map[string]error{key("/repo", "origin"): wantErr}}
	if err := fake.Fetch(context.Background(), "/repo", "origin"); err != wantErr {
		t.Fatalf("Fetch() = %v, want %v", err, wantErr)
	}
}

func TestFakeFetchPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_ = (&Fake{}).Fetch(context.Background(), "/repo", "origin")
	})
}

func TestFakeCallCountReportsCallsAnsweredSoFar(t *testing.T) {
	t.Parallel()
	fake := &Fake{
		CurrentBranchByDir:  map[string]Result{"/repo": {Value: "main"}},
		RevParseByDirAndRev: map[string]Result{key("/repo", "HEAD"): {Value: "abc123"}},
	}
	if got := fake.CallCount(); got != 0 {
		t.Fatalf("CallCount() = %d before any call, want 0", got)
	}
	_, _ = fake.CurrentBranch(context.Background(), "/repo")
	if got := fake.CallCount(); got != 1 {
		t.Fatalf("CallCount() = %d after one call, want 1", got)
	}
	_, _ = fake.RevParse(context.Background(), "/repo", "HEAD")
	if got := fake.CallCount(); got != 2 {
		t.Fatalf("CallCount() = %d after two calls, want 2", got)
	}
}

// TestFakeFailCallFailsExactlyOneCallAndPassesTheRest exercises FailCall
// across three different port methods (CurrentBranch, IsAncestor, Fetch),
// mirroring internal/runner/runnertest's
// TestFakeFailCallFailsExactlyOneCallAndPassesTheRest: only the targeted
// call number is overridden, and it returns its zero value alongside
// failErr rather than its scripted result.
func TestFakeFailCallFailsExactlyOneCallAndPassesTheRest(t *testing.T) {
	t.Parallel()
	errBoom := errors.New("boom")
	fake := &Fake{
		CurrentBranchByDir: map[string]Result{"/repo": {Value: "main"}},
		IsAncestorByCase:   map[string]BoolResult{key("/repo", "base", "head"): {Value: true}},
		FetchErrByDirAndRemote: map[string]error{
			key("/repo", "origin"): nil,
		},
	}
	fake.FailCall(2, errBoom)

	branch, err := fake.CurrentBranch(context.Background(), "/repo")
	if err != nil || branch != "main" {
		t.Fatalf("call 1 = (%q, %v), want (\"main\", nil)", branch, err)
	}
	ok, err := fake.IsAncestor(context.Background(), "/repo", "base", "head")
	if !errors.Is(err, errBoom) {
		t.Fatalf("call 2 err = %v, want errBoom", err)
	}
	if ok {
		t.Fatal("call 2 result = true, want false (the zero value) when FailCall overrides it")
	}
	if err := fake.Fetch(context.Background(), "/repo", "origin"); err != nil {
		t.Fatalf("call 3 err = %v, want nil: FailCall must pass every call but the one it targets", err)
	}
}

func TestFakeFailCallOfZeroDisablesTheOverride(t *testing.T) {
	t.Parallel()
	fake := &Fake{CurrentBranchByDir: map[string]Result{"/repo": {Value: "main"}}}
	fake.FailCall(1, errors.New("boom"))
	fake.FailCall(0, nil)

	branch, err := fake.CurrentBranch(context.Background(), "/repo")
	if err != nil || branch != "main" {
		t.Fatalf("CurrentBranch() = (%q, %v), want (\"main\", nil): FailCall(0, ...) should disable the override", branch, err)
	}
}

func TestFakeFailCallReplacesItsPreviousTarget(t *testing.T) {
	t.Parallel()
	errBoom := errors.New("boom")
	fake := &Fake{
		CurrentBranchByDir:  map[string]Result{"/repo": {Value: "main"}},
		RevParseByDirAndRev: map[string]Result{key("/repo", "HEAD"): {Value: "abc123"}},
	}
	fake.FailCall(1, errors.New("stale target"))
	fake.FailCall(2, errBoom)

	if _, err := fake.CurrentBranch(context.Background(), "/repo"); err != nil {
		t.Fatalf("call 1 err = %v, want nil: the later FailCall(2, ...) must replace the FailCall(1, ...) target", err)
	}
	if _, err := fake.RevParse(context.Background(), "/repo", "HEAD"); !errors.Is(err, errBoom) {
		t.Fatalf("call 2 err = %v, want errBoom", err)
	}
}

// multiCallConsumer exercises all four gitcli.Git port methods in sequence,
// stopping at the first error -- a stand-in for a real caller: task-8's
// plan (Verifies 2) calls for a sweep test over a multi-call consumer of
// the fake, and no production package imports gitcli.Git directly yet (see
// gitcli.go's package doc), so this is that consumer, written for the test
// alone.
func multiCallConsumer(ctx context.Context, g gitcli.Git, dir string) error {
	if _, err := g.CurrentBranch(ctx, dir); err != nil {
		return err
	}
	if _, err := g.RevParse(ctx, dir, "HEAD"); err != nil {
		return err
	}
	if _, err := g.IsAncestor(ctx, dir, "main", "HEAD"); err != nil {
		return err
	}
	return g.Fetch(ctx, dir, "origin")
}

// TestFakeWorksAsATestsweepFailer is task-8's proof that Fake plugs into
// internal/testsweep.Sweep: multiCallConsumer's happy path makes four real
// port calls, and Sweep drives each one to fail in turn.
func TestFakeWorksAsATestsweepFailer(t *testing.T) {
	t.Parallel()
	errBoom := errors.New("boom")
	dir := "/repo"

	newFake := func() *Fake {
		return &Fake{
			CurrentBranchByDir:     map[string]Result{dir: {Value: "main"}},
			RevParseByDirAndRev:    map[string]Result{key(dir, "HEAD"): {Value: "abc123"}},
			IsAncestorByCase:       map[string]BoolResult{key(dir, "main", "HEAD"): {Value: true}},
			FetchErrByDirAndRemote: map[string]error{key(dir, "origin"): nil},
		}
	}
	body := func(fake *Fake) error {
		return multiCallConsumer(context.Background(), fake, dir)
	}

	sawCallNums := map[int]bool{}
	testsweep.Sweep(t, newFake, errBoom, body,
		func(t testing.TB, callNum, total int, err error) {
			if total != 4 {
				t.Fatalf("total = %d, want 4", total)
			}
			if !errors.Is(err, errBoom) {
				t.Fatalf("call %d err = %v, want it to wrap errBoom", callNum, err)
			}
			sawCallNums[callNum] = true
		})

	for _, want := range []int{1, 2, 3, 4} {
		if !sawCallNums[want] {
			t.Fatalf("Sweep never checked call %d", want)
		}
	}
}

// The tests below cover the methods gitclitest.go adds for
// spec/plans/coverage-to-100 task-17 (internal/orchestrate's Git port): one
// scripted-success case and one unscripted-panic case per method, the same
// shape every method above already follows.

func TestFakeWorktreeRemoveForceReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{WorktreeRemoveForceErrByCase: map[string]error{key("/repo", "/scratch"): wantErr}}
	if err := fake.WorktreeRemoveForce(context.Background(), "/repo", "/scratch"); err != wantErr {
		t.Fatalf("WorktreeRemoveForce() = %v, want %v", err, wantErr)
	}
}

func TestFakeWorktreeRemoveForcePanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_ = (&Fake{}).WorktreeRemoveForce(context.Background(), "/repo", "/scratch") //nolint:errcheck
	})
}

func TestFakeWorktreeAddDetachedReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{WorktreeAddDetachedErrByCase: map[string]error{key("/repo", "/scratch", "abc123"): wantErr}}
	if err := fake.WorktreeAddDetached(context.Background(), "/repo", "/scratch", "abc123"); err != wantErr {
		t.Fatalf("WorktreeAddDetached() = %v, want %v", err, wantErr)
	}
}

func TestFakeWorktreeAddDetachedPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_ = (&Fake{}).WorktreeAddDetached(context.Background(), "/repo", "/scratch", "abc123") //nolint:errcheck
	})
}

func TestFakeCherryPickNoCommitReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{CherryPickNoCommitErrByCase: map[string]error{key("/repo", "sha1,sha2"): wantErr}}
	if err := fake.CherryPickNoCommit(context.Background(), "/repo", "sha1", "sha2"); err != wantErr {
		t.Fatalf("CherryPickNoCommit() = %v, want %v", err, wantErr)
	}
}

func TestFakeCherryPickNoCommitPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_ = (&Fake{}).CherryPickNoCommit(context.Background(), "/repo", "sha1") //nolint:errcheck
	})
}

func TestFakeCommitNoVerifyReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{CommitNoVerifyErrByCase: map[string]error{key("/repo", "aggregate"): wantErr}}
	if err := fake.CommitNoVerify(context.Background(), "/repo", "aggregate"); err != wantErr {
		t.Fatalf("CommitNoVerify() = %v, want %v", err, wantErr)
	}
}

func TestFakeCommitNoVerifyPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_ = (&Fake{}).CommitNoVerify(context.Background(), "/repo", "aggregate") //nolint:errcheck
	})
}

func TestFakeCherryPickReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{CherryPickErrByCase: map[string]error{key("/repo", "sha1"): wantErr}}
	if err := fake.CherryPick(context.Background(), "/repo", "sha1"); err != wantErr {
		t.Fatalf("CherryPick() = %v, want %v", err, wantErr)
	}
}

func TestFakeCherryPickPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_ = (&Fake{}).CherryPick(context.Background(), "/repo", "sha1") //nolint:errcheck
	})
}

func TestFakePushForceWithLeaseHeadReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{PushForceWithLeaseHeadErrByCase: map[string]error{key("/repo", "feature", "sha1"): wantErr}}
	if err := fake.PushForceWithLeaseHead(context.Background(), "/repo", "feature", "sha1"); err != wantErr {
		t.Fatalf("PushForceWithLeaseHead() = %v, want %v", err, wantErr)
	}
}

func TestFakePushForceWithLeaseHeadPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_ = (&Fake{}).PushForceWithLeaseHead(context.Background(), "/repo", "feature", "sha1") //nolint:errcheck
	})
}

func TestFakeFetchRefsReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{FetchRefsErrByCase: map[string]error{key("/repo", "origin", "main,feature"): wantErr}}
	if err := fake.FetchRefs(context.Background(), "/repo", "origin", "main", "feature"); err != wantErr {
		t.Fatalf("FetchRefs() = %v, want %v", err, wantErr)
	}
}

func TestFakeFetchRefsPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_ = (&Fake{}).FetchRefs(context.Background(), "/repo", "origin", "main") //nolint:errcheck
	})
}

func TestFakeRevListReverseRangeReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{RevListReverseRangeByCase: map[string]Result{key("/repo", "base", "head"): {Value: "sha1\nsha2"}}}
	out, err := fake.RevListReverseRange(context.Background(), "/repo", "base", "head")
	if err != nil || out != "sha1\nsha2" {
		t.Fatalf("RevListReverseRange() = (%q, %v)", out, err)
	}
}

func TestFakeRevListReverseRangePanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_, _ = (&Fake{}).RevListReverseRange(context.Background(), "/repo", "base", "head") //nolint:errcheck
	})
}

func TestFakeRemotePushURLReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{RemotePushURLByDirAndRemote: map[string]Result{key("/repo", "origin"): {Value: "git@example.test:o/r.git"}}}
	url, err := fake.RemotePushURL(context.Background(), "/repo", "origin")
	if err != nil || url != "git@example.test:o/r.git" {
		t.Fatalf("RemotePushURL() = (%q, %v)", url, err)
	}
}

func TestFakeRemotePushURLPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_, _ = (&Fake{}).RemotePushURL(context.Background(), "/repo", "origin") //nolint:errcheck
	})
}

func TestFakeStatusPorcelainReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{StatusPorcelainByDir: map[string]Result{"/repo": {Value: "M file.go"}}}
	status, err := fake.StatusPorcelain(context.Background(), "/repo")
	if err != nil || status != "M file.go" {
		t.Fatalf("StatusPorcelain() = (%q, %v)", status, err)
	}
}

func TestFakeStatusPorcelainPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_, _ = (&Fake{}).StatusPorcelain(context.Background(), "/repo") //nolint:errcheck
	})
}

func TestFakeBranchShowCurrentReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{BranchShowCurrentByDir: map[string]Result{"/repo": {Value: "feature"}}}
	branch, err := fake.BranchShowCurrent(context.Background(), "/repo")
	if err != nil || branch != "feature" {
		t.Fatalf("BranchShowCurrent() = (%q, %v)", branch, err)
	}
}

func TestFakeBranchShowCurrentPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_, _ = (&Fake{}).BranchShowCurrent(context.Background(), "/repo") //nolint:errcheck
	})
}

func TestFakeMergeBaseIsAncestorStrictReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{MergeBaseIsAncestorStrictErrByCase: map[string]error{key("/repo", "base", "head"): wantErr}}
	if err := fake.MergeBaseIsAncestorStrict(context.Background(), "/repo", "base", "head"); err != wantErr {
		t.Fatalf("MergeBaseIsAncestorStrict() = %v, want %v", err, wantErr)
	}
}

func TestFakeMergeBaseIsAncestorStrictPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_ = (&Fake{}).MergeBaseIsAncestorStrict(context.Background(), "/repo", "base", "head") //nolint:errcheck
	})
}

func TestFakeBranchSetUpstreamToReturnsScriptedError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{BranchSetUpstreamToErrByCase: map[string]error{key("/repo", "origin/feature", "feature"): wantErr}}
	if err := fake.BranchSetUpstreamTo(context.Background(), "/repo", "origin/feature", "feature"); err != wantErr {
		t.Fatalf("BranchSetUpstreamTo() = %v, want %v", err, wantErr)
	}
}

func TestFakeBranchSetUpstreamToPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_ = (&Fake{}).BranchSetUpstreamTo(context.Background(), "/repo", "origin/feature", "feature") //nolint:errcheck
	})
}

func TestFakeMergeTreeWriteTreeReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{MergeTreeWriteTreeByCase: map[string]Result{key("/repo", "a", "b"): {Value: "treeid"}}}
	tree, err := fake.MergeTreeWriteTree(context.Background(), "/repo", "a", "b")
	if err != nil || tree != "treeid" {
		t.Fatalf("MergeTreeWriteTree() = (%q, %v)", tree, err)
	}
}

func TestFakeMergeTreeWriteTreePanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_, _ = (&Fake{}).MergeTreeWriteTree(context.Background(), "/repo", "a", "b") //nolint:errcheck
	})
}

func TestFakeShowTreeFormatReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	fake := &Fake{ShowTreeFormatByCase: map[string]Result{key("/repo", "head1"): {Value: "treeid"}}}
	tree, err := fake.ShowTreeFormat(context.Background(), "/repo", "head1")
	if err != nil || tree != "treeid" {
		t.Fatalf("ShowTreeFormat() = (%q, %v)", tree, err)
	}
}

func TestFakeShowTreeFormatPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_, _ = (&Fake{}).ShowTreeFormat(context.Background(), "/repo", "head1") //nolint:errcheck
	})
}

func TestFakeCommitObjectExistsReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{CommitObjectExistsByCase: map[string]BoolResult{key("/repo", "sha1"): {Value: true, Err: wantErr}}}
	exists, err := fake.CommitObjectExists(context.Background(), "/repo", "sha1")
	if !exists || err != wantErr {
		t.Fatalf("CommitObjectExists() = (%v, %v), want (true, %v)", exists, err, wantErr)
	}
}

func TestFakeCommitObjectExistsPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_, _ = (&Fake{}).CommitObjectExists(context.Background(), "/repo", "sha1") //nolint:errcheck
	})
}

func TestFakeConfigRegexpMatchesReturnsScriptedResult(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &Fake{ConfigRegexpMatchesByCase: map[string]BoolResult{key("/repo", "^remote\\.x\\."): {Value: true, Err: wantErr}}}
	matched, err := fake.ConfigRegexpMatches(context.Background(), "/repo", "^remote\\.x\\.")
	if !matched || err != wantErr {
		t.Fatalf("ConfigRegexpMatches() = (%v, %v)", matched, err)
	}
}

func TestFakeConfigRegexpMatchesPanicsWhenUnscripted(t *testing.T) {
	t.Parallel()
	mustPanic(t, func() {
		_, _ = (&Fake{}).ConfigRegexpMatches(context.Background(), "/repo", "^remote\\.x\\.") //nolint:errcheck
	})
}
