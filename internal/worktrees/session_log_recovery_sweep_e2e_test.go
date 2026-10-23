//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:paralleltest // the Git fixture configures process-wide environment
func TestE2ELogVerbRefusalsKeepClaimAndJournalAuthority(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "log-sweep-refusal",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	ctx := context.Background()
	if _, err := LogHandoff(ctx, LogHandoffOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Successor: "next"}); err == nil || !strings.Contains(err.Error(), "summary") {
		t.Fatalf("missing summary accepted: %v", err)
	}
	if _, err := LogIntegrate(ctx, LogIntegrateOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree}); err == nil || !strings.Contains(err.Error(), "checkpoint") {
		t.Fatalf("uncheckpointed integration accepted: %v", err)
	}
	if _, err := LogRecover(ctx, LogRecoverOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Apply: true, Takeover: true}); err == nil || !strings.Contains(err.Error(), "actor") {
		t.Fatalf("anonymous takeover accepted: %v", err)
	}
	if _, err := LogArchive(ctx, LogArchiveOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree}); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("active journal archived: %v", err)
	}
	view, err := LoadWorkLogView(ctx, LoadWorkLogOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree})
	if err != nil || view.Claim == nil || view.Claim.Lifecycle != "active" {
		t.Fatalf("claim changed after refusals: view=%#v err=%v", view, err)
	}
	finalized, err := LogFinalize(ctx, LogFinalizeOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree,
		Result: "success", Message: "finished", Apply: true})
	if err != nil || !finalized.Applied {
		t.Fatalf("finalize before archive = %#v, %v", finalized, err)
	}
	if _, err := LogArchive(ctx, LogArchiveOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree}); err == nil || !strings.Contains(err.Error(), "seven days") {
		t.Fatalf("recent terminal journal archived: %v", err)
	}
	eventsPath := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, "events.jsonl")
	if err := os.WriteFile(eventsPath, []byte("{invalid event JSON\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, readErr := readLocalEvents(worktree)
	if readErr == nil {
		t.Fatal("corrupt journal fixture was still readable")
	}
	recovery, err := LogRecover(ctx, LogRecoverOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree})
	if err != nil {
		t.Fatal(err)
	}
	foundCorruption := false
	for _, diagnosis := range recovery.Diagnosis {
		foundCorruption = foundCorruption || strings.Contains(diagnosis, readErr.Error())
	}
	if !foundCorruption || recovery.Applied {
		t.Fatalf("read-only recover did not disclose corrupt journal: %#v", recovery)
	}
}

//nolint:paralleltest // the Git fixture configures process-wide environment
func TestE2ELogRefreshRecordsFailedFetchWithoutMutatingCheckout(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "log-sweep-refresh",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	before := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	result, err := LogRefresh(context.Background(), LogRefreshOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Base: "missing-target"})
	if err != nil || result.Event == nil || result.Event.Type != LocalEventRefreshNeed || result.Event.Conflict != "fetch_failed" ||
		len(result.Notes) == 0 {
		t.Fatalf("failed refresh = %#v, %v", result, err)
	}
	if after := gitTestOutput(t, worktree, "rev-parse", "HEAD"); after != before {
		t.Fatalf("refresh moved HEAD from %s to %s", before, after)
	}
	if _, err := LogIntegrate(context.Background(), LogIntegrateOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Base: "missing-target"}); err == nil {
		t.Fatal("integration accepted a failed target fetch")
	}
}

//nolint:paralleltest // the shell retirement fixture configures process-wide environment
func TestE2ERetiredStageRecoveryFiltersExactStageAndInventoriesLinks(t *testing.T) {
	projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
	const task = "sweep-stage"
	stageName := ".wb-retired-stage-99999999999999999999999999999999"
	stage := filepath.Join(worktreesRoot, task, stageName)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "evidence"), []byte("exact bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("evidence", filepath.Join(stage, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(stage, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "nested", "more"), []byte("more"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	other, err := RecoverRetiredStages(ctx, RetiredStageRecoveryOptions{ProjectsRoot: projectsRoot, Task: task,
		Stage: ".wb-retired-stage-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if err != nil || len(other.Results) != 0 {
		t.Fatalf("wrong exact stage selected: %#v, %v", other, err)
	}
	plan, err := RecoverRetiredStages(ctx, RetiredStageRecoveryOptions{ProjectsRoot: projectsRoot, Task: task, Stage: stageName})
	if err != nil || len(plan.Results) != 1 {
		t.Fatalf("stage plan = %#v, %v", plan, err)
	}
	got := plan.Results[0]
	if !got.Eligible || got.Applied || got.FileCount != 3 || got.SymlinkCount != 1 || got.ContentDigest == "" || got.Durable {
		t.Fatalf("stage inventory = %#v", got)
	}
	if _, err := os.Lstat(stage); err != nil {
		t.Fatalf("dry-run moved stage: %v", err)
	}
	second := filepath.Join(worktreesRoot, task, ".wb-retired-stage-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err := os.Mkdir(second, 0o700); err != nil {
		t.Fatal(err)
	}
	all, err := RecoverRetiredStages(ctx, RetiredStageRecoveryOptions{ProjectsRoot: projectsRoot, Task: task})
	if err != nil || len(all.Results) != 2 || all.Results[0].Stage != stageName || all.Results[1].Path != second {
		t.Fatalf("sorted stage inventory = %#v, %v", all, err)
	}
}
