package worktrees

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

const (
	retirementRepo    = "/fixture/repository"
	retirementHead    = "1111111111111111111111111111111111111111"
	retirementLanding = "2222222222222222222222222222222222222222"
	retirementTarget  = "3333333333333333333333333333333333333333"
	retirementTree    = "4444444444444444444444444444444444444444"
)

func expectRetirementGit(fake *runnertest.Fake, result runner.Result, err error, args ...string) {
	fake.ExpectArgv(append([]string{"git", "-C", retirementRepo}, args...), result, err)
}

func expectRetirementAncestor(fake *runnertest.Fake, ancestor, descendant string, result runner.Result, err error) {
	expectRetirementGit(fake, result, err, "merge-base", "--is-ancestor", ancestor, descendant)
}

func expectRetirementMerge(fake *runnertest.Fake, ours, theirs string, result runner.Result, err error) {
	expectRetirementGit(fake, result, err, "merge-tree", "--write-tree", "--no-messages", "--end-of-options", ours, theirs)
}

func TestInjectedAncestryDistinguishesContainmentAbsenceAndGitFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		result       runner.Result
		err          error
		wantAncestor bool
		wantError    bool
	}{
		{name: "contained"},
		{name: "not contained", result: runner.Result{ExitCode: 1}, err: errors.New("exit status 1")},
		{name: "query failed", result: runner.Result{ExitCode: 128}, err: errors.New("bad object"), wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			expectRetirementAncestor(fake, retirementHead, retirementTarget, tc.result, tc.err)
			ctx := withGitRunner(context.Background(), fake)
			got, err := isAncestor(ctx, retirementRepo, retirementHead, retirementTarget)
			want := tc.name == "contained"
			if got != want || (err != nil) != tc.wantError {
				t.Fatalf("isAncestor = (%v, %v), want (%v, error=%v)", got, err, want, tc.wantError)
			}
			if tc.wantError && !strings.Contains(err.Error(), "bad object") {
				t.Fatalf("Git failure was lost: %v", err)
			}
			if fake.CallCount() != 1 {
				t.Fatalf("Git calls = %d, want 1", fake.CallCount())
			}
		})
	}
}

func TestInjectedAncestryMemoizesBothVerdictsWithoutRunningGitAgain(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	expectRetirementAncestor(fake, retirementHead, retirementTarget, runner.Result{}, nil)
	expectRetirementAncestor(fake, retirementTarget, retirementHead, runner.Result{ExitCode: 1}, errors.New("exit status 1"))
	ctx := withGitQueryMemo(withGitRunner(context.Background(), fake))
	for i := 0; i < 2; i++ {
		if got, err := isAncestor(ctx, retirementRepo, retirementHead, retirementTarget); !got || err != nil {
			t.Fatalf("contained query %d = (%v, %v)", i, got, err)
		}
		if got, err := isAncestor(ctx, retirementRepo, retirementTarget, retirementHead); got || err != nil {
			t.Fatalf("absent query %d = (%v, %v)", i, got, err)
		}
	}
	if fake.CallCount() != 2 {
		t.Fatalf("Git calls = %d, want one per distinct query", fake.CallCount())
	}
}

func TestInjectedMergeTreeDistinguishesCleanConflictAndFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		result    runner.Result
		err       error
		wantTree  string
		wantClean bool
		wantError string
	}{
		{name: "clean", result: runner.Result{Stdout: retirementTree + "\n"}, wantTree: retirementTree, wantClean: true},
		{name: "conflict", result: runner.Result{ExitCode: 1}, err: errors.New("exit status 1")},
		{name: "Git failure", result: runner.Result{ExitCode: 128, Stderr: "missing object"}, err: errors.New("exit status 128"), wantError: "missing object"},
		{name: "invalid tree", result: runner.Result{Stdout: "not a tree\n"}, wantError: "invalid tree"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			expectRetirementMerge(fake, retirementLanding, retirementHead, tc.result, tc.err)
			gotTree, clean, err := mergeResultTree(withGitRunner(context.Background(), fake), retirementRepo, retirementLanding, retirementHead)
			if gotTree != tc.wantTree || clean != tc.wantClean {
				t.Fatalf("merge tree = (%q, %v, %v), want (%q, %v)", gotTree, clean, err, tc.wantTree, tc.wantClean)
			}
			if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("merge error = %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestRetirementEvidenceRefusesInvalidReceiptAndChangedContainment(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		disposition    string
		landing        string
		ancestorResult runner.Result
		ancestorErr    error
		wantError      string
	}{
		{name: "missing landing", disposition: BranchReceipted, wantError: "no landing commit"},
		{name: "landing no longer contained", disposition: BranchReceipted, landing: retirementLanding, ancestorResult: runner.Result{ExitCode: 1}, ancestorErr: errors.New("exit status 1"), wantError: "no longer contained"},
		{name: "landing query failed", disposition: BranchReceipted, landing: retirementLanding, ancestorResult: runner.Result{ExitCode: 128}, ancestorErr: errors.New("bad object"), wantError: "bad object"},
		{name: "branch no longer contained", disposition: BranchContained, ancestorResult: runner.Result{ExitCode: 1}, ancestorErr: errors.New("exit status 1"), wantError: "no longer an ancestor"},
		{name: "branch query failed", disposition: BranchContained, ancestorResult: runner.Result{ExitCode: 128}, ancestorErr: errors.New("bad object"), wantError: "bad object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			if tc.landing != "" {
				expectRetirementAncestor(fake, retirementLanding, retirementTarget, tc.ancestorResult, tc.ancestorErr)
			} else if tc.disposition == BranchContained {
				expectRetirementAncestor(fake, retirementHead, retirementTarget, tc.ancestorResult, tc.ancestorErr)
			}
			result := BranchCleanupResult{BranchEntry: BranchEntry{Disposition: tc.disposition, LandingSHA: tc.landing}}
			if ok := recheckDeletionEvidence(withGitRunner(context.Background(), fake), retirementRepo, retirementHead, retirementTarget, &result); ok || result.Outcome != "failed" || !strings.Contains(result.Error, tc.wantError) {
				t.Fatalf("recheck = (%v, %q), want failed with %q", result.Outcome, result.Error, tc.wantError)
			}
		})
	}
}

func TestRetirementEvidenceRechecksReceiptAgainstLandingAndCurrentTarget(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		landingTree string
		targetTree  string
		wantValid   bool
	}{
		{name: "still absorbed", landingTree: retirementTree, targetTree: retirementTree, wantValid: true},
		{name: "reverted after landing", landingTree: retirementTree, targetTree: retirementHead},
		{name: "missing from landing", landingTree: retirementHead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			expectRetirementAncestor(fake, retirementLanding, retirementTarget, runner.Result{}, nil)
			expectRetirementMerge(fake, retirementLanding, retirementHead, runner.Result{Stdout: tc.landingTree + "\n"}, nil)
			expectRetirementGit(fake, runner.Result{CombinedOutput: retirementTree + "\n"}, nil, "rev-parse", retirementLanding+"^{tree}")
			if tc.landingTree == retirementTree {
				expectRetirementMerge(fake, retirementTarget, retirementHead, runner.Result{Stdout: tc.targetTree + "\n"}, nil)
				expectRetirementGit(fake, runner.Result{CombinedOutput: retirementTree + "\n"}, nil, "rev-parse", retirementTarget+"^{tree}")
			}
			result := BranchCleanupResult{BranchEntry: BranchEntry{Disposition: BranchReceipted, LandingSHA: retirementLanding}}
			ok := recheckDeletionEvidence(withGitRunner(context.Background(), fake), retirementRepo, retirementHead, retirementTarget, &result)
			if ok != tc.wantValid {
				t.Fatalf("recheck = %v, outcome=%s error=%q, want %v", ok, result.Outcome, result.Error, tc.wantValid)
			}
			if !ok && (result.Outcome != "failed" || !strings.Contains(result.Error, "receipt no longer holds")) {
				t.Fatalf("refusal = (%q, %q), want changed receipt proof", result.Outcome, result.Error)
			}
		})
	}
}

func TestRetirementGuardsRefuseProtectedCheckedOutAndUnreadableState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		head         string
		headErr      error
		worktrees    string
		worktreesErr error
		wantError    string
	}{
		{name: "HEAD unreadable", headErr: errors.New("repository unavailable"), wantError: "recheck canonical HEAD"},
		{name: "protected branch", head: "feature", wantError: "now protected"},
		{name: "linked worktrees unreadable", head: "main", worktreesErr: errors.New("worktree list failed"), wantError: "enumerate linked worktrees"},
		{name: "branch checked out", head: "main", worktrees: "worktree /other\nbranch refs/heads/feature\n", wantError: "became checked out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			expectRetirementGit(fake, runner.Result{CombinedOutput: tc.head + "\n"}, tc.headErr, "rev-parse", "--abbrev-ref", "HEAD")
			if tc.head == "main" && tc.headErr == nil {
				expectRetirementGit(fake, runner.Result{CombinedOutput: tc.worktrees}, tc.worktreesErr, "worktree", "list", "--porcelain")
			}
			result := BranchCleanupResult{BranchEntry: BranchEntry{Repository: "acme/app", Branch: "feature", Base: "main"}}
			if ok := recheckBranchRetirementGuards(withGitRunner(context.Background(), fake), "", retirementRepo, &result); ok || result.Outcome != "failed" || !strings.Contains(result.Error, tc.wantError) {
				t.Fatalf("guard = (%v, %q), want failed with %q", result.Outcome, result.Error, tc.wantError)
			}
		})
	}
}

func expectExactRetirementTarget(fake *runnertest.Fake, fetchError error) {
	expectExactRetirementTargetAt(fake, retirementRepo, fetchError)
}

func expectExactRetirementTargetAt(fake *runnertest.Fake, repositoryPath string, fetchError error) {
	var privateRef string
	fake.Expect(func(call runnertest.Call) bool {
		if !ordinaryGitCall(call, repositoryPath, "fetch") || len(call.Args) < 1 {
			return false
		}
		refspec := call.Args[len(call.Args)-1]
		if !strings.HasPrefix(refspec, "+refs/heads/main:refs/wb/fetch-base/") {
			return false
		}
		privateRef = strings.TrimPrefix(refspec, "+refs/heads/main:")
		return true
	}, runner.Result{}, fetchError)
	if fetchError == nil {
		fake.Expect(func(call runnertest.Call) bool {
			return ordinaryGitCall(call, repositoryPath, "rev-parse") && len(call.Args) > 0 && call.Args[len(call.Args)-1] == privateRef+"^{commit}"
		}, runner.Result{CombinedOutput: retirementTarget + "\n"}, nil)
	}
	fake.Expect(func(call runnertest.Call) bool {
		return ordinaryGitCall(call, repositoryPath, "update-ref") && len(call.Args) > 0 && call.Args[len(call.Args)-1] == privateRef
	}, runner.Result{}, nil)
}

func TestApplyBranchCleanupKeepsLocalFailureScopedToItsCandidate(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	expectExactRetirementTarget(fake, errors.New("target unavailable"))
	results := []BranchCleanupResult{
		{BranchEntry: BranchEntry{Repository: "acme/app", Branch: "feature", Base: "main", Scope: BranchScopeLocal, SHA: retirementHead}, Eligible: true, Outcome: "planned"},
		{BranchEntry: BranchEntry{Repository: "acme/app", Branch: "skipped", Scope: BranchScopeRemote}, Eligible: false, Outcome: "skipped"},
	}
	applyBranchCleanup(withGitRunner(context.Background(), fake), results,
		map[string]string{"acme/app": retirementRepo}, BranchCleanupOptions{}, time.Now())
	if results[0].Applied || results[0].Outcome != "failed" || !strings.Contains(results[0].Error, "target unavailable") {
		t.Fatalf("local candidate failure = %#v", results[0])
	}
	if results[1].Outcome != "skipped" || results[1].Applied {
		t.Fatalf("ineligible candidate changed = %#v", results[1])
	}
}

func TestApplyBranchCleanupRefusesLostRepositoryPathPerCandidate(t *testing.T) {
	t.Parallel()
	results := []BranchCleanupResult{
		{BranchEntry: BranchEntry{Repository: "acme/app", Branch: "feature/old", Scope: BranchScopeLocal}, Eligible: true, Outcome: "planned"},
		{BranchEntry: BranchEntry{Repository: "acme/app", Branch: "feature/skipped", Scope: BranchScopeRemote}, Outcome: "skipped"},
	}
	applyBranchCleanup(context.Background(), results, map[string]string{}, BranchCleanupOptions{}, time.Now())
	if results[0].Applied || results[0].Outcome != "failed" || !strings.Contains(results[0].Error, "repository path was not retained") || results[1].Outcome != "skipped" {
		t.Fatalf("lost path contaminated candidates: %#v", results)
	}
}

func TestLocalBranchRetirementRefusesMissingCanonicalAfterProof(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	expectExactRetirementTarget(fake, nil)
	expectRetirementGit(fake, runner.Result{CombinedOutput: retirementHead + "\n"}, nil,
		"rev-parse", "--verify", "refs/heads/feature")
	expectRetirementGit(fake, runner.Result{CombinedOutput: "main\n"}, nil, "rev-parse", "--abbrev-ref", "HEAD")
	expectRetirementGit(fake, runner.Result{}, nil, "worktree", "list", "--porcelain")
	expectRetirementAncestor(fake, retirementHead, retirementTarget, runner.Result{}, nil)
	result := BranchCleanupResult{BranchEntry: BranchEntry{
		Repository: "acme/app", Branch: "feature", Base: "main", SHA: retirementHead, Disposition: BranchContained,
	}}
	applyLocalBranchDeletion(withGitRunner(context.Background(), fake), retirementRepo, &result, BranchCleanupOptions{})
	if result.Applied || result.Outcome != "failed" || !strings.Contains(result.Error, "open canonical repository") {
		t.Fatalf("missing canonical repository after proof = %#v", result)
	}
}

func TestLocalBranchRetirementFailsClosedAtChangingInputs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		fetchError     error
		branchError    error
		guardError     error
		ancestorResult runner.Result
		ancestorError  error
		wantError      string
	}{
		{name: "target fetch failed", fetchError: errors.New("remote unavailable"), wantError: "refetch exact origin/main target"},
		{name: "branch disappeared", branchError: errors.New("unknown ref"), wantError: "branch no longer exists"},
		{name: "canonical HEAD unreadable", guardError: errors.New("corrupt HEAD"), wantError: "recheck canonical HEAD"},
		{name: "branch no longer contained", ancestorResult: runner.Result{ExitCode: 1}, ancestorError: errors.New("exit status 1"), wantError: "no longer an ancestor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			expectExactRetirementTarget(fake, tc.fetchError)
			if tc.fetchError == nil {
				expectRetirementGit(fake, runner.Result{CombinedOutput: retirementHead + "\n"}, tc.branchError, "rev-parse", "--verify", "refs/heads/feature")
			}
			if tc.fetchError == nil && tc.branchError == nil {
				expectRetirementGit(fake, runner.Result{CombinedOutput: "main\n"}, tc.guardError, "rev-parse", "--abbrev-ref", "HEAD")
			}
			if tc.name == "branch no longer contained" {
				expectRetirementGit(fake, runner.Result{}, nil, "worktree", "list", "--porcelain")
				expectRetirementAncestor(fake, retirementHead, retirementTarget, tc.ancestorResult, tc.ancestorError)
			}
			result := BranchCleanupResult{BranchEntry: BranchEntry{Repository: "acme/app", Branch: "feature", Base: "main", SHA: retirementHead, Disposition: BranchContained}}
			applyLocalBranchDeletion(withGitRunner(context.Background(), fake), retirementRepo, &result, BranchCleanupOptions{})
			if result.Applied || result.Outcome != "failed" || !strings.Contains(result.Error, tc.wantError) {
				t.Fatalf("local retirement = (%v, %q, %q), want refusal %q", result.Applied, result.Outcome, result.Error, tc.wantError)
			}
		})
	}
}

func TestRemoteBranchRetirementFailsClosedBeforeDeletion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		pruneError   error
		targetError  error
		remoteResult runner.Result
		remoteError  error
		wantError    string
	}{
		{name: "prune failed", pruneError: errors.New("remote unavailable"), wantError: "refetch --prune"},
		{name: "target fetch failed", targetError: errors.New("target unavailable"), wantError: "refetch exact origin/main target"},
		{name: "branch query failed", remoteError: errors.New("query failed"), wantError: "re-resolve remote branch"},
		{name: "branch disappeared", wantError: "remote branch no longer exists"},
		{name: "branch moved", remoteResult: runner.Result{CombinedOutput: retirementLanding + "\trefs/heads/feature\n"}, wantError: "remote branch moved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			expectRetirementGit(fake, runner.Result{}, tc.pruneError, "fetch", "--prune", "origin", "+refs/heads/*:refs/remotes/origin/*")
			if tc.pruneError == nil {
				expectExactRetirementTarget(fake, tc.targetError)
			}
			if tc.pruneError == nil && tc.targetError == nil {
				expectRetirementGit(fake, tc.remoteResult, tc.remoteError, "ls-remote", "--heads", "origin", "refs/heads/feature")
			}
			result := BranchCleanupResult{BranchEntry: BranchEntry{Repository: "acme/app", Branch: "feature", Base: "main", SHA: retirementHead, Scope: BranchScopeRemote}}
			applyRemoteBranchDeletion(withGitRunner(context.Background(), fake), retirementRepo, &result, BranchCleanupOptions{})
			if result.Applied || result.Outcome != "failed" || !strings.Contains(result.Error, tc.wantError) {
				t.Fatalf("remote retirement = (%v, %q, %q), want refusal %q", result.Applied, result.Outcome, result.Error, tc.wantError)
			}
		})
	}
}

func TestRetirementEvidenceRefusesChangedOrUnreadableSupersessionReceipt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		path       string
		wantDigest string
	}{
		{name: "unreadable", path: "/fixture/missing-receipt", wantDigest: ""},
		{name: "different digest", path: "", wantDigest: "different"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := tc.path
			if path == "" {
				path = t.TempDir() + "/receipt.json"
				if err := writeDurableFile(path, []byte("receipt changed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			result := BranchCleanupResult{BranchEntry: BranchEntry{SupersededAtOrigin: true, SupersessionReceipt: path, SupersessionSHA256: tc.wantDigest}}
			if ok := recheckDeletionEvidence(context.Background(), retirementRepo, retirementHead, retirementTarget, &result); ok || result.Outcome != "failed" || !strings.Contains(result.Error, "receipt bytes changed") {
				t.Fatalf("supersession recheck = (%v, %q), want refusal", result.Outcome, result.Error)
			}
		})
	}
}

func TestRetirementEvidenceRefusesFailedOrIncompleteMergeProof(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		mergeResult runner.Result
		mergeError  error
		wantError   string
	}{
		{name: "conflicted", mergeResult: runner.Result{ExitCode: 1}, mergeError: errors.New("exit status 1"), wantError: "receipt no longer holds"},
		{name: "Git failure", mergeResult: runner.Result{ExitCode: 128, Stderr: "object unavailable"}, mergeError: errors.New("exit status 128"), wantError: "object unavailable"},
		{name: "bad tree", mergeResult: runner.Result{Stdout: "not-a-tree\n"}, wantError: "invalid tree"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			expectRetirementAncestor(fake, retirementLanding, retirementTarget, runner.Result{}, nil)
			expectRetirementMerge(fake, retirementLanding, retirementHead, tc.mergeResult, tc.mergeError)
			result := BranchCleanupResult{BranchEntry: BranchEntry{Disposition: BranchReceipted, LandingSHA: retirementLanding}}
			if ok := recheckDeletionEvidence(withGitRunner(context.Background(), fake), retirementRepo, retirementHead, retirementTarget, &result); ok || result.Outcome != "failed" || !strings.Contains(result.Error, tc.wantError) {
				t.Fatalf("receipt recheck = (%v, %q), want refusal %q", result.Outcome, result.Error, tc.wantError)
			}
		})
	}
}
