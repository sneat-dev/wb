//go:build e2e

package worktrees

import (
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
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
	outcome, err := ListWithDiagnostics(ctx, ListOptions{ProjectsRoot: f.projectsRoot, Task: effort})
	if err != nil || len(outcome.Diagnostics) != 0 || len(outcome.Results) != 1 {
		t.Fatalf("inventory: %+v %v", outcome, err)
	}
	if err := sealWorkLogForCleanup(f.home, created.Path, base, nil); err != nil {
		t.Fatal(err)
	}
	record := newLifecycleBacklogRecord(f.projectsRoot, outcome.Results[0], "removed")
	if err := persistLifecycleBacklog(f.home, &record, lifecycleStageWorktreeRemoved); err != nil {
		t.Fatal(err)
	}
	gitTest(t, f.canonical, "worktree", "remove", created.Path)
	records, quarantine, err := loadResumableLifecycleBacklog(ctx, f.home, f.projectsRoot, []string{record.WorktreesRoot}, map[string]bool{effort: true}, "", "removed")
	if err != nil || len(quarantine) != 0 || len(records) != 1 {
		t.Fatalf("retired checkout journal: %+v %+v %v", records, quarantine, err)
	}
}
