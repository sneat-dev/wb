//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

//nolint:paralleltest // Shared-placement fixture selects process-wide WB configuration.
func TestE2EOperationTaskInspectionRejectsUnboundRepositoryEffort(t *testing.T) {
	f := newGitFixture(t)
	configureFixtureSharedWorktrees(t, f)
	ctx := context.Background()
	const operation = "campaign-test"
	const effort = "campaign-test.unrelated"
	base := gitTestOutput(t, f.canonical, "rev-parse", "HEAD")
	placement, err := ResolveWorktreePlacement(ctx, f.projectsRoot, f.canonical, base)
	if err != nil {
		t.Fatal(err)
	}
	created, err := CreateWorktreeAtPlacement(ctx, f.projectsRoot, f.canonical, placement, operation, "acme/app", "wb/campaign-test", "main", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(created.Close)
	result := CreateResult{Repository: "acme/app", WorktreeDir: created.Path, Branch: "wb/campaign-test", Base: "main", BaseSHA: base}
	if _, err := EnsureWorkLogClaim(f.home, effort, result, WorkLogOptions{EffortID: effort, RunID: operation, Model: "unknown"}); err != nil {
		t.Fatal(err)
	}
	inspect := lifecycleInspection{ctx: ctx, projectsRoot: f.projectsRoot, home: f.home, worktree: created.Path, task: effort, layout: wbhome.Layout{WorktreesRoot: placement.Root}, base: "main"}
	if err := inspect.locate(); err == nil || !strings.Contains(err.Error(), "does not corroborate shared worktree layout") {
		t.Fatalf("unbound repository effort accepted: %v", err)
	}
	inspect.task = "campaign-test.foreign"
	if err := inspect.locate(); err == nil || !strings.Contains(err.Error(), "belongs to task") {
		t.Fatalf("foreign task accepted: %v", err)
	}
}

//nolint:paralleltest // Shared-placement fixture selects process-wide WB configuration.
func TestE2EOperationBacklogSurvivesCheckoutRetirement(t *testing.T) {
	f := newGitFixture(t)
	configureFixtureSharedWorktrees(t, f)
	ctx := context.Background()
	const operation = "campaign-test"
	const effort = "campaign-test.acme-app"
	base := gitTestOutput(t, f.canonical, "rev-parse", "HEAD")
	placement, err := ResolveWorktreePlacement(ctx, f.projectsRoot, f.canonical, base)
	if err != nil {
		t.Fatal(err)
	}
	created, err := CreateWorktreeAtPlacement(ctx, f.projectsRoot, f.canonical, placement, operation, "acme/app", "wb/campaign-test", "main", base)
	if err != nil {
		t.Fatal(err)
	}
	created.Close()
	result := CreateResult{Repository: "acme/app", WorktreeDir: created.Path, Branch: "wb/campaign-test", Base: "main", BaseSHA: base}
	if _, err := EnsureWorkLogClaim(f.home, effort, result, WorkLogOptions{EffortID: effort, RunID: operation, Model: "unknown"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created.Path, "campaign.txt"), []byte("campaign change"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, created.Path, "add", "campaign.txt")
	gitTest(t, created.Path, "commit", "-m", "fixture: campaign change")
	head := gitTestOutput(t, created.Path, "rev-parse", "HEAD")
	gitTest(t, f.canonical, "merge", "--ff-only", result.Branch)
	gitTest(t, f.canonical, "push", "origin", "main")
	outcome, err := ListWithDiagnostics(ctx, ListOptions{ProjectsRoot: f.projectsRoot, Task: effort})
	if err != nil || len(outcome.Diagnostics) != 0 || len(outcome.Results) != 1 {
		t.Fatalf("inventory: %+v %v", outcome, err)
	}
	if err := sealWorkLogForCleanup(f.home, created.Path, head, &worktreeclaims.LandedEvidence{Target: "main", LandedSHA: head, Proof: worktreeclaims.LandedProofContained}); err != nil {
		t.Fatal(err)
	}
	record := newLifecycleBacklogRecord(f.projectsRoot, outcome.Results[0], "removed")
	// Reproduce an interruption after cleanup's immutable seal but before its
	// first durable journal publication. No existing evidence is rewritten.
	backlog := lifecycleBacklogDirectory(f.home)
	if err := os.MkdirAll(filepath.Dir(backlog), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backlog, []byte("fixture obstruction"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := persistLifecycleBacklog(f.home, &record, lifecycleStageSealed); err == nil {
		t.Fatal("journal obstruction did not interrupt cleanup")
	}
	if err := os.Remove(backlog); err != nil {
		t.Fatal(err)
	}
	sources, err := ListSourceWithDiagnostics(ctx, SourceListOptions{ProjectsRoot: f.projectsRoot, Repository: "acme/app", Branch: result.Branch, HeadSHA: head})
	if err != nil || len(sources.Diagnostics) != 0 || len(sources.Results) != 1 {
		t.Fatalf("sealed cleanup source: %+v %v", sources, err)
	}
	installMergedPullRequestFixture(t, head, time.Now().Add(-time.Hour))
	retried, err := Cleanup(ctx, CleanupOptions{ProjectsRoot: f.projectsRoot, Task: effort, Base: "main", Apply: true})
	if err != nil || len(retried.Diagnostics) != 0 || len(retried.Results) != 1 || !retried.Results[0].Applied {
		t.Fatalf("normal retry: %+v %v", retried, err)
	}
	if _, err := os.Stat(created.Path); !os.IsNotExist(err) {
		t.Fatalf("checkout remains: %v", err)
	}
}
