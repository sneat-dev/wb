//go:build e2e

package orchestrate

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestE2EPullRequestUpdateProductionProofUsesOwnedCanonicalDAG(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	head := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	options := PullRequestUpdateOptions{ProjectsRoot: fixture.githubDir, Repository: fixture.repository.Slug}
	proved, err := productionPullRequestUpdateOps().proveTree(context.Background(), options, "main", "main", head, head, head)
	if err != nil || !proved {
		t.Fatalf("native canonical proof=%t, %v", proved, err)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != head {
		t.Fatalf("proof changed canonical HEAD: %s -> %s", head, got)
	}
}

func TestE2EPullRequestUpdateLocalSyncRefusesUnclaimedRegistration(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	path := filepath.Join(t.TempDir(), "unclaimed")
	const branch = "feature/unclaimed-update"
	runEngineGit(t, fixture.canonical, "worktree", "add", "-b", branch, path, "HEAD")
	before := strings.TrimSpace(runEngineGit(t, path, "rev-parse", "HEAD"))
	options := PullRequestUpdateOptions{ProjectsRoot: fixture.githubDir, Repository: fixture.repository.Slug}
	note := syncOwnedPullRequestUpdateWorktree(context.Background(), options, branch, "unobserved-head")
	if note != "local sync skipped: checkout has no matching active WB claim" {
		t.Fatalf("unclaimed checkout note=%q", note)
	}
	if got := strings.TrimSpace(runEngineGit(t, path, "rev-parse", "HEAD")); got != before {
		t.Fatalf("unclaimed checkout moved: %s -> %s", before, got)
	}
}

func TestE2EPullRequestUpdateLocalSyncRechecksGuardAfterRegistrationObservation(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	const branch = "feature/guard-update"
	source := createMergeSource(t, fixture, "guard-update", branch, "guard.txt", "owned change\n")
	head := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	registration := runEngineGit(t, fixture.canonical, "worktree", "list", "--porcelain")
	// Preserve the genuinely observed registration, then detach the owned checkout.
	// The injected runner supplies only that stale observation; claim and guard reads remain native.
	runEngineGit(t, source.WorktreeDir, "checkout", "--detach", head)
	view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: source.WorktreeDir})
	if err != nil || view.Claim == nil || view.Claim.Branch != branch || view.Claim.Lifecycle != "active" {
		t.Fatalf("native active claim=%+v, %v", view, err)
	}
	if _, err := worktrees.Guard(context.Background(), source.WorktreeDir, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir, Base: view.Claim.Base}); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
		t.Fatalf("native guard refusal=%v", err)
	}
	run := runnertest.New(t).Expect(func(call runnertest.Call) bool {
		return call.Dir == fixture.canonical && strings.Join(call.Argv(), " ") == "git worktree list --porcelain"
	}, runner.Result{CombinedOutput: registration}, nil)
	options := PullRequestUpdateOptions{ProjectsRoot: fixture.githubDir, Repository: fixture.repository.Slug}
	note := syncOwnedPullRequestUpdateWorktreeWithRunner(context.Background(), options, branch, "unobserved-head", run)
	if note != "local sync skipped: checkout did not pass WB guard" || run.CallCount() != 1 {
		t.Fatalf("guard note=%q calls=%d", note, run.CallCount())
	}
	if got := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); got != head {
		t.Fatalf("guard-refused checkout moved: %s -> %s", head, got)
	}
}
