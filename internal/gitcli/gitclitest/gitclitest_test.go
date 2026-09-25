package gitclitest

import (
	"context"
	"errors"
	"testing"

	"github.com/sneat-dev/wb/internal/gitcli"
	"github.com/sneat-dev/wb/internal/testsweep"
)

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
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted dir")
		}
	}()
	(&Fake{}).CurrentBranch(context.Background(), "/repo") //nolint:errcheck
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
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted rev")
		}
	}()
	(&Fake{}).RevParse(context.Background(), "/repo", "HEAD") //nolint:errcheck
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
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted case")
		}
	}()
	(&Fake{}).IsAncestor(context.Background(), "/repo", "base", "head") //nolint:errcheck
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
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic for an unscripted remote")
		}
	}()
	(&Fake{}).Fetch(context.Background(), "/repo", "origin") //nolint:errcheck
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
