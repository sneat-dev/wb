package orchestrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/gitcli/gitclitest"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/testsweep"
)

// TestFastForwardWorktreeToUpdatedHeadRunnerCallsSweptByTestsweep proves
// fastForwardWorktreeToUpdatedHead's three runner.Runner-backed steps (the
// origin fetch, mergeRevision's `rev-parse --verify`, and the final
// `merge --ff-only`) are each reachable, and each error return reached,
// through a runnertest.Fake alone -- no real git process
// (spec/plans/coverage-to-100 task-17: runCommand and mergeRevision now take
// a runner.Runner rather than calling exec.CommandContext directly). The
// function's Git-port steps (StatusPorcelain, BranchShowCurrent,
// MergeBaseIsAncestorStrict) are held fixed on a fully scripted
// gitclitest.Fake so the sweep isolates the runner side; those steps'
// own error returns are already covered by
// TestFastForwardWorktreeToUpdatedHeadPropagatesABranchShowCurrentError and
// its neighbours in ports_seam_coverage_test.go.
func TestFastForwardWorktreeToUpdatedHeadRunnerCallsSweptByTestsweep(t *testing.T) {
	t.Parallel()
	const worktree = "/fake/worktree"
	const branch = "feature"
	const updatedHead = "0123456789012345678901234567890123456789"
	remoteRef := "refs/remotes/origin/" + branch

	git := &gitclitest.Fake{
		StatusPorcelainByDir:               map[string]gitclitest.Result{worktree: {Value: ""}},
		BranchShowCurrentByDir:             map[string]gitclitest.Result{worktree: {Value: branch}},
		MergeBaseIsAncestorStrictErrByCase: map[string]error{worktree + "\x00HEAD\x00" + remoteRef: nil},
		BranchSetUpstreamToErrByCase:       map[string]error{worktree + "\x00origin/" + branch + "\x00" + branch: nil},
	}
	failErr := errors.New("boom: guarded runner refused to start")

	body := func(fake *runnertest.Fake) error {
		fake.ExpectArgv([]string{"git", "fetch", "--no-tags", "origin", "+refs/heads/" + branch + ":" + remoteRef}, runner.Result{}, nil)
		fake.ExpectArgv([]string{"git", "rev-parse", "--verify", remoteRef + "^{commit}"}, runner.Result{Stdout: updatedHead + "\n"}, nil)
		fake.ExpectArgv([]string{"git", "merge", "--ff-only", remoteRef}, runner.Result{}, nil)
		note := fastForwardWorktreeToUpdatedHead(context.Background(), git, fake, worktree, branch, updatedHead)
		if strings.HasPrefix(note, "local worktree not fast-forwarded: ") {
			return errors.New(note)
		}
		if !strings.HasPrefix(note, "fast-forwarded worktree ") {
			return errors.New("unexpected note: " + note)
		}
		return nil
	}

	testsweep.Sweep(t, func() *runnertest.Fake { return runnertest.New(t) }, failErr, body,
		func(t testing.TB, callNum, total int, err error) {
			if total != 3 {
				t.Fatalf("total = %d, want 3: fastForwardWorktreeToUpdatedHead makes exactly 3 runner calls on the happy path (fetch, mergeRevision, merge --ff-only)", total)
			}
			if err == nil || !strings.Contains(err.Error(), failErr.Error()) {
				t.Fatalf("call %d/%d err = %v, want it to wrap %v", callNum, total, err, failErr)
			}
		})
}

// TestSyncLocalWorktreeAfterUpdateBranchReachesTheFastForwardThroughFakesAlone
// proves the whole chain syncLocalWorktreeAfterUpdateBranch ->
// registeredWorktreeForBranch (engine.go) -> fastForwardWorktreeToUpdatedHead
// is reachable end to end through PullRequestLandOptions' git/run seam
// alone, with no real git process: registeredWorktreeForBranch used to be
// called with a bare Options{} (always production's real defaultRunner,
// regardless of what the caller's own options carried), which this task
// fixed to Options{run: options.resolveRunner()} specifically so this path
// is fake-reachable.
func TestSyncLocalWorktreeAfterUpdateBranchReachesTheFastForwardThroughFakesAlone(t *testing.T) {
	t.Parallel()
	const worktree = "/registered/worktree"
	const branch = "feature"
	const updatedHead = "0123456789012345678901234567890123456789"
	remoteRef := "refs/remotes/origin/" + branch

	git := &gitclitest.Fake{
		StatusPorcelainByDir:               map[string]gitclitest.Result{worktree: {Value: ""}},
		BranchShowCurrentByDir:             map[string]gitclitest.Result{worktree: {Value: branch}},
		MergeBaseIsAncestorStrictErrByCase: map[string]error{worktree + "\x00HEAD\x00" + remoteRef: nil},
		BranchSetUpstreamToErrByCase:       map[string]error{worktree + "\x00origin/" + branch + "\x00" + branch: nil},
	}
	run := runnertest.New(t)
	run.ExpectArgv([]string{"git", "worktree", "list", "--porcelain"},
		runner.Result{Stdout: "worktree " + worktree + "\nbranch refs/heads/" + branch + "\n"}, nil)
	run.ExpectArgv([]string{"git", "fetch", "--no-tags", "origin", "+refs/heads/" + branch + ":" + remoteRef}, runner.Result{}, nil)
	run.ExpectArgv([]string{"git", "rev-parse", "--verify", remoteRef + "^{commit}"}, runner.Result{Stdout: updatedHead + "\n"}, nil)
	run.ExpectArgv([]string{"git", "merge", "--ff-only", remoteRef}, runner.Result{}, nil)

	options := PullRequestLandOptions{ProjectsRoot: t.TempDir(), Repository: "acme/app", git: git, run: run}

	note := syncLocalWorktreeAfterUpdateBranch(context.Background(), options, branch, updatedHead)
	if !strings.HasPrefix(note, "fast-forwarded worktree ") {
		t.Fatalf("note = %q, want a fast-forward success note reached entirely through fakes", note)
	}
}
