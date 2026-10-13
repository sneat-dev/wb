//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/progress"
	"os"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // Native merge journey installs a process-wide GH/PATH fixture and WB_TEST_TARGET_SHA.
func TestE2ELandWorktreeMergeAlreadyContainedBeforePrepare(t *testing.T) {
	fixture := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, fixture, "pre-merged-source", "feature/pre-merged", "pre-merged.txt", "pre merged\n")
	sourceHead := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))

	// Merge source into canonical main before PrepareWorktreeMerge (simulating PR merged on GitHub)
	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "-m", "merge pre-merged feature into main", sourceHead)
	targetSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	// PrepareWorktreeMerge sees that candidate is at targetSHA and contains source.SHA
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Candidate.SHA != targetSHA {
		t.Fatalf("candidate SHA = %s, want target SHA %s", receipt.Candidate.SHA, targetSHA)
	}

	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", targetSHA)

	var events []progress.Event
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Cleanup: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
		Progress: func(e progress.Event) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("LandWorktreeMerge error: %v", err)
	}
	if landed.Status != WorktreeMergeComplete {
		t.Fatalf("landed status = %s, want %s", landed.Status, WorktreeMergeComplete)
	}
	if landed.LandingSHA != targetSHA {
		t.Fatalf("landed.LandingSHA = %s, want %s", landed.LandingSHA, targetSHA)
	}

	// Verify "already_landed" progress event was emitted
	foundAlreadyLanded := false
	for _, e := range events {
		if e.Phase == "already_landed" && e.State == progress.Completed {
			foundAlreadyLanded = true
			break
		}
	}
	if !foundAlreadyLanded {
		t.Fatalf("expected already_landed progress event in: %+v", events)
	}

	// Verify candidate and source worktrees are cleaned up
	if _, err := os.Stat(receipt.Candidate.Worktree); !os.IsNotExist(err) {
		t.Fatalf("candidate worktree was not cleaned up: %v", err)
	}
	if _, err := os.Stat(source.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("source worktree was not cleaned up: %v", err)
	}
}

//nolint:paralleltest // Native merge journey installs a process-wide GH/PATH fixture and WB_TEST_TARGET_SHA.
func TestE2ELandWorktreeMergeAlreadyContainedBeforePrepareNoCleanup(t *testing.T) {
	fixture := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, fixture, "pre-merged-no-clean", "feature/pre-merged-no-clean", "no-clean.txt", "no clean\n")
	sourceHead := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))

	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "-m", "merge feature into main", sourceHead)
	targetSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", targetSHA)

	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Cleanup: false, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("LandWorktreeMerge error: %v", err)
	}
	if landed.Status != WorktreeMergeLanded {
		t.Fatalf("landed status = %s, want %s (landed_cleanup_pending)", landed.Status, WorktreeMergeLanded)
	}
	if landed.LandingSHA != targetSHA {
		t.Fatalf("landed.LandingSHA = %s, want %s", landed.LandingSHA, targetSHA)
	}

	// Verify worktrees are preserved when Cleanup is false
	if _, err := os.Stat(receipt.Candidate.Worktree); err != nil {
		t.Fatalf("candidate worktree should be preserved when cleanup=false: %v", err)
	}
	if _, err := os.Stat(source.WorktreeDir); err != nil {
		t.Fatalf("source worktree should be preserved when cleanup=false: %v", err)
	}
}

//nolint:paralleltest // Native merge journey installs a process-wide GH/PATH fixture and WB_TEST_TARGET_SHA.
func TestE2ELandWorktreeMergeAlreadyContainedAfterTargetDriftRebase(t *testing.T) {
	fixture := newExplicitRootEngineFixture(t)
	source1 := createMergeSource(t, fixture, "drift-source-1", "feature/drift-1", "drift1.txt", "drift 1\n")
	source1Head := strings.TrimSpace(runEngineGit(t, source1.WorktreeDir, "rev-parse", "HEAD"))
	source2 := createMergeSource(t, fixture, "drift-source-2", "feature/drift-2", "drift2.txt", "drift 2\n")
	source2Head := strings.TrimSpace(runEngineGit(t, source2.WorktreeDir, "rev-parse", "HEAD"))

	// Prepare before merging into target: candidate integrates both sources, creating a local merge commit
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source1.WorktreeDir, source2.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Now merge both sources into main on remote (external PR landing on GitHub)
	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "-m", "external landing of source 1", source1Head)
	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "-m", "external landing of source 2", source2Head)
	targetSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", targetSHA)

	var events []progress.Event
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		Cleanup: true, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
		Progress: func(e progress.Event) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("LandWorktreeMerge error: %v", err)
	}
	if landed.Status != WorktreeMergeComplete {
		t.Fatalf("landed status = %s, want %s", landed.Status, WorktreeMergeComplete)
	}
	if landed.LandingSHA != targetSHA {
		t.Fatalf("landed.LandingSHA = %s, want %s", landed.LandingSHA, targetSHA)
	}

	// Verify rebase was recorded and already_landed was emitted
	if landed.Rebase == nil {
		t.Fatal("expected rebase record after target drift")
	}
	foundAlreadyLanded := false
	for _, e := range events {
		if e.Phase == "already_landed" && e.State == progress.Completed {
			foundAlreadyLanded = true
			break
		}
	}
	if !foundAlreadyLanded {
		t.Fatalf("expected already_landed progress event after rebase in: %+v", events)
	}
}

func TestE2ELandWorktreeMergeAlreadyContainedRunnerErrors(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, fixture, "err-source", "feature/err-source", "err.txt", "err\n")
	sourceHead := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))

	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "-m", "merge err feature into main", sourceHead)
	targetSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("injected merge-base failure")
	faultRunner := &alreadyLandedFaultRunner{
		err: sentinel,
		refuse: func(dir, name string, args []string) bool {
			return len(args) >= 3 && args[0] == "merge-base" && args[1] == receipt.Candidate.SHA && args[2] == targetSHA
		},
	}

	got, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		run: faultRunner, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err == nil || !errors.Is(err, sentinel) || got.Status != WorktreeMergeConflict {
		t.Fatalf("expected conflict with sentinel error at Point 1, got %+v, %v", got, err)
	}
}

//nolint:paralleltest // Native merge journey installs a process-wide GH/PATH fixture and WB_TEST_TARGET_SHA.
func TestE2ELandWorktreeMergeAlreadyContainedAfterRebaseRunnerError(t *testing.T) {
	fixture := newExplicitRootEngineFixture(t)
	source1 := createMergeSource(t, fixture, "rebase-err-1", "feature/rebase-err-1", "rebase-err-1.txt", "1\n")
	source1Head := strings.TrimSpace(runEngineGit(t, source1.WorktreeDir, "rev-parse", "HEAD"))
	source2 := createMergeSource(t, fixture, "rebase-err-2", "feature/rebase-err-2", "rebase-err-2.txt", "2\n")
	source2Head := strings.TrimSpace(runEngineGit(t, source2.WorktreeDir, "rev-parse", "HEAD"))

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source1.WorktreeDir, source2.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "-m", "merge 1", source1Head)
	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "-m", "merge 2", source2Head)
	targetSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", targetSHA)

	sentinel := errors.New("injected post-rebase merge-base failure")
	rebaseCount := 0
	faultRunner := &alreadyLandedFaultRunner{
		err: sentinel,
		refuse: func(dir, name string, args []string) bool {
			if len(args) >= 1 && args[0] == "rebase" {
				rebaseCount++
			}
			// Only refuse merge-base calls after rebase has executed
			if rebaseCount > 0 && len(args) >= 3 && args[0] == "merge-base" && args[2] == targetSHA {
				return true
			}
			return false
		},
	}

	got, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto,
		run: faultRunner, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond,
	})
	if err == nil || !errors.Is(err, sentinel) || got.Status != WorktreeMergeConflict {
		t.Fatalf("expected conflict with sentinel error after rebase at Point 2, got %+v, %v", got, err)
	}
}
