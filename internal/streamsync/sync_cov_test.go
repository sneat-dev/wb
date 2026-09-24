package streamsync

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The stubs below override exactly one port method each, so a test can drive
// one failure without modelling the rest of the engine's world twice.

type stCovVerifierFunc func(ctx context.Context, dir string) (VerificationRun, error)

func (verify stCovVerifierFunc) Verify(ctx context.Context, dir string) (VerificationRun, error) {
	return verify(ctx, dir)
}

type stCovCIFunc func(dir string) (map[string]bool, bool, error)

func (present stCovCIFunc) Present(dir string) (map[string]bool, bool, error) { return present(dir) }

type stCovIsCleanErrGit struct {
	*fakeGit
	err error
}

func (git stCovIsCleanErrGit) IsClean(context.Context, string) (bool, error) { return false, git.err }

type stCovFetchErrGit struct {
	*fakeGit
	err error
}

func (git stCovFetchErrGit) Fetch(context.Context, string) error { return git.err }

// stCovHeadCounterGit fails the Head call at one zero-based occurrence of a
// revision, so a later read of the same revision can be made to fail.
type stCovHeadCounterGit struct {
	*fakeGit
	revision string
	failAt   int
	seen     int
	err      error
}

func (git *stCovHeadCounterGit) Head(ctx context.Context, dir, revision string) (string, error) {
	if revision == git.revision {
		if git.seen == git.failAt {
			git.seen++
			return "", git.err
		}
		git.seen++
	}
	return git.fakeGit.Head(ctx, dir, revision)
}

type stCovRebaseErrGit struct {
	*fakeGit
	branch string
	err    error
}

func (git stCovRebaseErrGit) Rebase(ctx context.Context, dir, branch, upstream string) ([]string, error) {
	if branch == git.branch {
		return nil, git.err
	}
	return git.fakeGit.Rebase(ctx, dir, branch, upstream)
}

type stCovCheckoutErrGit struct {
	*fakeGit
	err error
}

func (git stCovCheckoutErrGit) Checkout(context.Context, string, string) error { return git.err }

type stCovRestoreErrGit struct {
	*fakeGit
	err error
}

func (git stCovRestoreErrGit) RestoreTo(context.Context, string, string) error { return git.err }

type stCovCommitAllErrGit struct {
	*fakeGit
	err error
}

func (git stCovCommitAllErrGit) CommitAll(context.Context, string, string) (string, bool, error) {
	return "", false, git.err
}

type stCovBumper struct {
	*fakeBumper
	requiredErr map[string]error
}

func (bumper stCovBumper) Required(ctx context.Context, dir string, library Library) (string, bool, error) {
	if err := bumper.requiredErr[library.Name]; err != nil {
		return "", false, err
	}
	return bumper.fakeBumper.Required(ctx, dir, library)
}

func TestResultFailedNamesEveryReason(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		result Result
		want   bool
	}{
		{name: "recorded error", result: Result{Errors: []string{"boom"}}, want: true},
		{name: "failed bump", result: Result{StreamRebase: RebaseResult{Rebased: true}, Bumps: []BumpResult{{Action: BumpFailed}}}, want: true},
		{name: "failed batch", result: Result{StreamRebase: RebaseResult{Rebased: true}, Batch: &BatchResult{Passed: false}}, want: true},
		{name: "agent conflict", result: Result{StreamRebase: RebaseResult{Rebased: true}, AgentRebases: []RebaseResult{{Conflicts: []string{"a.go"}}}}, want: true},
		{name: "stream not rebased", result: Result{StreamRebase: RebaseResult{Rebased: false}}, want: true},
		{name: "clean", result: Result{StreamRebase: RebaseResult{Rebased: true}}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.result.Failed(); got != test.want {
				t.Fatalf("Failed() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestSyncReportsUnreadableGitState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("cleanliness", func(t *testing.T) {
		t.Parallel()
		git := newFakeGit()
		engine := &Engine{Git: stCovIsCleanErrGit{fakeGit: git, err: errors.New("status unreadable")}, Bumper: newFakeBumper(), Verifier: &fakeVerifier{}, Events: &fakeEvents{}}
		if _, err := engine.sync(ctx, baseOptions()); err == nil || !strings.Contains(err.Error(), "status unreadable") {
			t.Fatalf("error = %v, want the cleanliness failure", err)
		}
		if len(git.calls) != 0 {
			t.Fatalf("sync acted before it could read the worktree: %v", git.calls)
		}
	})

	t.Run("fetch", func(t *testing.T) {
		t.Parallel()
		engine := &Engine{Git: stCovFetchErrGit{fakeGit: newFakeGit(), err: errors.New("origin unreachable")}, Bumper: newFakeBumper(), Verifier: &fakeVerifier{}, Events: &fakeEvents{}}
		if _, err := engine.sync(ctx, baseOptions()); err == nil || !strings.Contains(err.Error(), "re-read origin before rebasing") {
			t.Fatalf("error = %v, want a fetch failure", err)
		}
	})

	t.Run("local head", func(t *testing.T) {
		t.Parallel()
		git := newFakeGit()
		engine := &Engine{Git: &stCovHeadCounterGit{fakeGit: git, revision: "stream/checkout", failAt: 0, err: errors.New("head unreadable")}, Bumper: newFakeBumper(), Verifier: &fakeVerifier{}, Events: &fakeEvents{}}
		if _, err := engine.sync(ctx, baseOptions()); err == nil || !strings.Contains(err.Error(), "head unreadable") {
			t.Fatalf("error = %v, want the pre-rebase head failure", err)
		}
	})
}

// A stream branch that cannot be rebased at all is a refusal to continue: the
// tree is restored, the reason is recorded, and no agent branch is touched.
func TestSyncReportsAStreamRebaseFailure(t *testing.T) {
	t.Parallel()
	git := newFakeGit()
	engine := &Engine{Git: stCovRebaseErrGit{fakeGit: git, branch: "stream/checkout", err: errors.New("rebase exploded")}, Bumper: newFakeBumper(), Verifier: &fakeVerifier{}, Events: &fakeEvents{}}
	options := baseOptions()
	options.AgentBranches = []AgentBranch{{Branch: "agent/one", Agent: "wbs-1"}}

	result, err := engine.sync(context.Background(), options)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.StreamRebase.Rebased {
		t.Fatal("a failed rebase was reported as rebased")
	}
	if !strings.Contains(result.StreamRebase.Detail, "rebase exploded") {
		t.Fatalf("stream rebase detail = %q", result.StreamRebase.Detail)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "rebase stream/checkout onto origin/main") {
		t.Fatalf("errors = %v, want the rebase failure recorded", result.Errors)
	}
	if len(result.AgentRebases) != 0 {
		t.Fatalf("agent rebases = %#v; none may run after a failed stream rebase", result.AgentRebases)
	}
	if !stCovAbortedRebase(git) {
		t.Fatal("the failed rebase was not aborted")
	}
}

func TestSyncReportsAHeadFailureAfterTheRebase(t *testing.T) {
	t.Parallel()
	git := newFakeGit()
	// The first read is BaseBefore; the second is BaseAfter, after the rebase.
	engine := &Engine{Git: &stCovHeadCounterGit{fakeGit: git, revision: "stream/checkout", failAt: 1, err: errors.New("post-rebase head unreadable")}, Bumper: newFakeBumper(), Verifier: &fakeVerifier{}, Events: &fakeEvents{}}
	if _, err := engine.sync(context.Background(), baseOptions()); err == nil || !strings.Contains(err.Error(), "post-rebase head unreadable") {
		t.Fatalf("error = %v, want the post-rebase head failure", err)
	}
}

func TestSyncReportsAnAgentRebaseFailure(t *testing.T) {
	t.Parallel()
	git := newFakeGit()
	engine := &Engine{Git: stCovRebaseErrGit{fakeGit: git, branch: "agent/one", err: errors.New("agent rebase exploded")}, Bumper: newFakeBumper(), Verifier: &fakeVerifier{}, Events: &fakeEvents{}}
	options := baseOptions()
	options.AgentBranches = []AgentBranch{{Branch: "agent/one", Agent: "wbs-1"}}

	result, err := engine.sync(context.Background(), options)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(result.AgentRebases) != 1 || result.AgentRebases[0].Rebased {
		t.Fatalf("agent rebases = %#v, want the failure reported", result.AgentRebases)
	}
	if !strings.Contains(result.AgentRebases[0].Detail, "agent rebase exploded") {
		t.Fatalf("agent rebase detail = %q", result.AgentRebases[0].Detail)
	}
	if !stCovAbortedRebase(git) {
		t.Fatal("the failed agent rebase was not aborted")
	}
}

func TestSyncReportsACheckoutFailureAfterAgentRebases(t *testing.T) {
	t.Parallel()
	git := newFakeGit()
	engine := &Engine{Git: stCovCheckoutErrGit{fakeGit: git, err: errors.New("checkout refused")}, Bumper: newFakeBumper(), Verifier: &fakeVerifier{}, Events: &fakeEvents{}}
	options := baseOptions()
	options.AgentBranches = []AgentBranch{{Branch: "agent/one", Agent: "wbs-1"}}

	if _, err := engine.sync(context.Background(), options); err == nil || !strings.Contains(err.Error(), "checkout refused") {
		t.Fatalf("error = %v, want the checkout failure", err)
	}
}

func TestSyncReportsABatchVerificationFailure(t *testing.T) {
	t.Parallel()
	engine, _, _, _, _ := newTestEngine()
	engine.Verifier = stCovVerifierFunc(func(context.Context, string) (VerificationRun, error) {
		return VerificationRun{}, errors.New("suite could not start")
	})
	options := baseOptions()
	options.Verify = true

	if _, err := engine.sync(context.Background(), options); err == nil || !strings.Contains(err.Error(), "suite could not start") {
		t.Fatalf("error = %v, want the verification failure", err)
	}
}

func TestBumpReportsAnUnreadableOrAbsentLibrary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	options := baseOptions()
	library := Library{Name: "L", Target: "v2.0.0", Ecosystem: "go"}

	t.Run("unreadable requirement", func(t *testing.T) {
		t.Parallel()
		bumper := stCovBumper{fakeBumper: newFakeBumper(), requiredErr: map[string]error{"L": errors.New("manifest unreadable")}}
		engine := &Engine{Git: newFakeGit(), Bumper: bumper, Verifier: &fakeVerifier{}, Events: &fakeEvents{}}
		outcome := engine.bump(ctx, options, library)
		if outcome.Action != BumpFailed || !strings.Contains(outcome.Detail, "manifest unreadable") {
			t.Fatalf("outcome = %#v, want a failed bump carrying the reason", outcome)
		}
	})

	t.Run("library not declared", func(t *testing.T) {
		t.Parallel()
		bumper := newFakeBumper()
		bumper.missing["L"] = true
		engine := &Engine{Git: newFakeGit(), Bumper: bumper, Verifier: &fakeVerifier{}, Events: &fakeEvents{}}
		outcome := engine.bump(ctx, options, library)
		if outcome.Action != BumpNotRequired {
			t.Fatalf("outcome = %#v, want %s", outcome, BumpNotRequired)
		}
	})
}

func TestBumpReportsAFailedCommitAndRestoresTheWorktree(t *testing.T) {
	t.Parallel()
	git := newFakeGit()
	git.heads["HEAD"] = "pre-bump-head"
	bumper := newFakeBumper()
	bumper.required["L"] = "v1.0.0"
	engine := &Engine{Git: stCovCommitAllErrGit{fakeGit: git, err: errors.New("commit rejected")}, Bumper: bumper, Verifier: &fakeVerifier{}, Events: &fakeEvents{}}
	options := baseOptions()
	options.Libraries = []Library{{Name: "L", Target: "v2.0.0", Ecosystem: "go"}}

	outcome := engine.bump(context.Background(), options, options.Libraries[0])
	if outcome.Action != BumpFailed || !strings.Contains(outcome.Detail, "commit rejected") {
		t.Fatalf("outcome = %#v, want a failed bump carrying the commit error", outcome)
	}
	if len(git.restored) != 1 || git.restored[0] != "pre-bump-head" {
		t.Fatalf("restored = %v, want the pre-bump head", git.restored)
	}
}

func TestRestoreAfterFailedBumpReportsEveryRefusal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	options := baseOptions()

	t.Run("unreadable pre-bump head", func(t *testing.T) {
		t.Parallel()
		engine := &Engine{Git: newFakeGit(), Bumper: newFakeBumper(), Verifier: &fakeVerifier{}, Events: &fakeEvents{}}
		outcome := BumpResult{Action: BumpFailed, Detail: "apply failed"}
		engine.restoreAfterFailedBump(ctx, options, &outcome, "", errors.New("no head"))
		if !strings.Contains(outcome.Detail, "NOT restored") {
			t.Fatalf("detail = %q, want the unreadable-head warning", outcome.Detail)
		}
	})

	t.Run("restore refused", func(t *testing.T) {
		t.Parallel()
		git := newFakeGit()
		engine := &Engine{Git: stCovRestoreErrGit{fakeGit: git, err: errors.New("restore refused")}, Bumper: newFakeBumper(), Verifier: &fakeVerifier{}, Events: &fakeEvents{}}
		outcome := BumpResult{Action: BumpFailed, Detail: "apply failed"}
		engine.restoreAfterFailedBump(ctx, options, &outcome, "pre-bump-head", nil)
		if !strings.Contains(outcome.Detail, "could not be restored to pre-bump-head") {
			t.Fatalf("detail = %q, want the restore failure named", outcome.Detail)
		}
	})
}

func TestBumpCommitsKeepsOnlyWrittenBumps(t *testing.T) {
	t.Parallel()
	result := Result{Bumps: []BumpResult{
		{Library: Library{Name: "no-sha"}, Action: BumpApplied, Commit: ""},
		{Library: Library{Name: "not-applied"}, Action: BumpNotRequired, Commit: "sha-x"},
		{Library: Library{Name: "applied", Target: "v2.0.0"}, Action: BumpApplied, Commit: "sha-y", Required: "v1.0.0"},
	}}
	elements := (&Engine{}).bumpCommits(result)
	if len(elements) != 1 {
		t.Fatalf("elements = %#v, want only the written bump", elements)
	}
	if elements[0].Name != "applied" || elements[0].SHA != "sha-y" {
		t.Fatalf("element = %#v", elements[0])
	}
	if !strings.Contains(elements[0].Description, "applied v1.0.0 → v2.0.0") {
		t.Fatalf("description = %q, want the bump message", elements[0].Description)
	}
}

func TestFailureSummaryNamesABatchFailure(t *testing.T) {
	t.Parallel()
	result := Result{
		StreamRebase: RebaseResult{Rebased: true},
		Batch:        &BatchResult{Passed: false},
	}
	reasons := result.failureSummary()
	if len(reasons) != 1 || reasons[0] != "batch verification failed" {
		t.Fatalf("reasons = %v, want the batch failure named", reasons)
	}
	// The summary is what the terminal event carries, so a failed result that
	// names no reason would report nothing at all.
	git := newFakeGit()
	engine := &Engine{Git: git, Bumper: newFakeBumper(), Verifier: &fakeVerifier{}, Events: &fakeEvents{}}
	engine.recordOutcome(baseOptions(), result, nil)
}

func TestSyncWithoutAnEventSinkStillRuns(t *testing.T) {
	t.Parallel()
	engine, _, bumper, _, _ := newTestEngine()
	engine.Events = nil
	options := baseOptions()
	options.Libraries = []Library{{Name: "L", Target: "v2.0.0", Ecosystem: "go"}}
	bumper.required["L"] = "v1.0.0"

	result, err := engine.Sync(context.Background(), options)
	if err != nil {
		t.Fatalf("sync without an event sink: %v", err)
	}
	if len(result.Bumps) != 1 || result.Bumps[0].Action != BumpApplied {
		t.Fatalf("bumps = %#v, want the bump applied", result.Bumps)
	}
}

// stCovAbortedRebase reports whether any abort was recorded, so a test can
// assert the tree was handed back rather than left mid-rebase.
func stCovAbortedRebase(git *fakeGit) bool {
	for _, call := range git.calls {
		if call == "abort-rebase" {
			return true
		}
	}
	return false
}
