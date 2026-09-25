package orchestrate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/gitcli/gitclitest"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

// This file closes review round 2's B1/B2/B4 findings on task-17's
// coverage-to-100 migration (spec/plans/coverage-to-100): the
// resolveGit()/resolveRunner() seam PullRequestLandOptions and
// WorktreeMergeLandOptions carry (ports.go) was structurally correct but
// never actually substituted by any test, so its non-nil branch -- and two
// of the error-propagation paths it exists to make provable -- sat at
// coverage count 0. Each test below injects a gitclitest.Fake or
// runnertest.Fake through the option field the seam was built for, proving
// it is not just shaped right but actually reachable without starting a
// real git or gh process.

// TestPullRequestLandOptionsResolveGitReturnsTheInjectedGit covers
// pr_land.go's resolveGit() non-nil branch: a caller that sets options.git
// gets that exact value back, never defaultGit.
func TestPullRequestLandOptionsResolveGitReturnsTheInjectedGit(t *testing.T) {
	t.Parallel()
	fake := &gitclitest.Fake{}
	options := PullRequestLandOptions{git: fake}
	if got := options.resolveGit(); got != Git(fake) {
		t.Fatalf("resolveGit() with an injected git = %v, want the fake itself", got)
	}
}

// TestPullRequestLandOptionsResolveRunnerReturnsTheInjectedRunner covers
// pr_land.go's resolveRunner() non-nil branch, the same shape as
// resolveGit() above for this package's non-git-port runner.Runner seam.
func TestPullRequestLandOptionsResolveRunnerReturnsTheInjectedRunner(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	options := PullRequestLandOptions{run: fake}
	if got := options.resolveRunner(); got != runner.Runner(fake) {
		t.Fatalf("resolveRunner() with an injected run = %v, want the fake itself", got)
	}
}

// TestWorktreeMergeLandOptionsResolveGitReturnsTheInjectedGit covers
// worktree_merge.go's own resolveGit() non-nil branch: WorktreeMergeLandOptions
// carries the identical seam PullRequestLandOptions does, for `wb worktree
// merge`'s land/resume path.
func TestWorktreeMergeLandOptionsResolveGitReturnsTheInjectedGit(t *testing.T) {
	t.Parallel()
	fake := &gitclitest.Fake{}
	options := WorktreeMergeLandOptions{git: fake}
	if got := options.resolveGit(); got != Git(fake) {
		t.Fatalf("resolveGit() with an injected git = %v, want the fake itself", got)
	}
}

// TestCommitExistsLocallyTreatsAnEmptySHAAsAbsentWithoutCallingGit covers
// commitExistsLocally's empty-sha guard (worktree_merge_pr_land.go). The
// Fake is deliberately left with every map nil, so any call the guard did
// not actually short-circuit before would panic (gitclitest.Fake's
// documented behaviour for an unscripted case) instead of quietly
// returning a zero value -- reaching the assertion below is itself proof
// the guard ran before touching git at all.
func TestCommitExistsLocallyTreatsAnEmptySHAAsAbsentWithoutCallingGit(t *testing.T) {
	t.Parallel()
	fake := &gitclitest.Fake{}
	exists, err := commitExistsLocally(context.Background(), fake, "/some/worktree", "")
	if exists || err != nil {
		t.Fatalf(`commitExistsLocally(sha="") = (%t, %v), want (false, nil)`, exists, err)
	}
}

// TestVerifyUpdateBranchMergeProofPropagatesACommitExistsLocallyError
// covers verifyUpdateBranchMergeProof's B5 fix (worktree_merge_pr_land.go):
// a real command failure from commitExistsLocally -- including a guarded
// runner refusing to start the process at all -- must come back to the
// caller as an error, never be folded into headLocal=false the way an
// ordinary "object absent" negative is.
func TestVerifyUpdateBranchMergeProofPropagatesACommitExistsLocallyError(t *testing.T) {
	t.Parallel()
	const worktree = "/fake/worktree"
	const headSHA = "abc123headsha"
	wantErr := errors.New("boom: guarded runner refused to start")
	fake := &gitclitest.Fake{
		CommitObjectExistsByCase: map[string]gitclitest.BoolResult{
			worktree + "\x00" + headSHA: {Err: wantErr},
		},
	}

	proved, err := verifyUpdateBranchMergeProof(context.Background(), fake, worktree, "", "main", "acme/app", "candidatesha", "targetparentsha", headSHA)
	if proved {
		t.Fatalf("proved = true, want false when commitExistsLocally errors")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want it to be (or wrap) %v", err, wantErr)
	}
}

// TestBuildAtSurfacesAFailingBuildThroughTheInjectedRunner covers
// pr_land_keep.go's buildAt: a kept commit whose build fails must be
// refused with the failing command's own captured output, reached here
// through a runnertest.Fake rather than a real `go build` in the unit
// tier.
func TestBuildAtSurfacesAFailingBuildThroughTheInjectedRunner(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	writeEngineFile(t, filepath.Join(worktree, "go.mod"), "module example.test/keepbuild\n\ngo 1.24\n")

	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"go", "build", "./..."},
		runner.Result{Stdout: "compiling\n", Stderr: "undefined: Foo\n"},
		errors.New("exit status 2"))

	refusal := buildAt(context.Background(), fake, worktree, SourceCommit{SHA: "0123456789abcdef", Subject: "add thing"}, nil)
	if refusal == nil || refusal.code != LandRefusalKeepDoesNotBuild {
		t.Fatalf("failing build refusal = %+v", refusal)
	}
	if !strings.Contains(refusal.reason, "does not build") || !strings.Contains(refusal.reason, "undefined: Foo") {
		t.Fatalf("failing build refusal reason = %q, want it to surface the injected runner's captured output", refusal.reason)
	}
}

// TestSyncLocalWorktreeAfterUpdateBranchUsesTheInjectedGitForTheFastForward
// covers pr_land_local_sync.go's #611 call site: syncLocalWorktreeAfterUpdateBranch
// now resolves its Git port through options.resolveGit() rather than a
// package-level var, and this proves a landing that reaches it can be
// driven entirely through the injected Fake once the real worktree lookup
// (a plain `git worktree list --porcelain`, unaffected by the Fake) finds
// the branch's local checkout.
//
//nolint:paralleltest // calls a fixture helper (newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestSyncLocalWorktreeAfterUpdateBranchUsesTheInjectedGitForTheFastForward(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "task-sync-seam", "feature/sync-seam", "seam.txt", "seam\n")

	statusErr := errors.New("boom: injected status failure")
	fake := &gitclitest.Fake{
		StatusPorcelainByDir: map[string]gitclitest.Result{
			source.WorktreeDir: {Err: statusErr},
		},
	}
	options := PullRequestLandOptions{
		ProjectsRoot: fixture.githubDir,
		Repository:   fixture.repository.Slug,
		git:          fake,
	}

	note := syncLocalWorktreeAfterUpdateBranch(context.Background(), options, "feature/sync-seam", "deadbeef")
	if !strings.Contains(note, statusErr.Error()) {
		t.Fatalf("note = %q, want it to surface the injected git's StatusPorcelain error (proving options.resolveGit() reached the fake)", note)
	}
}
