package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

//nolint:paralleltest // native Git/WB fixture owns process-wide environment.
func TestAdoptedTerminalHistoryRequiresCompletedNativeCleanupAndPreservesEvidence(t *testing.T) {
	fixture := newGitFixture(t)
	task, path := adoptAndCommit(t, fixture, "feature/adopt-terminal-history", "feature.txt")
	claim, _, _, err := activeWorkLogClaimReadOnly(fixture.home, path)
	if err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(fixture.home, "worklogs", claim.EffortID, "runs", claim.RunID, "claims", claim.ClaimID+".json")
	before, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	if completed, err := completedAdoptedHistory(fixture.home, fixture.projectsRoot, claim); err != nil || completed {
		t.Fatalf("live adopted history became terminal: %t %v", completed, err)
	}
	head := gitTestOutput(t, path, "rev-parse", "HEAD")
	gitTest(t, path, "push", "-u", "origin", claim.Branch)
	gitTest(t, fixture.canonical, "merge", "--no-ff", claim.Branch, "-m", "merge adopted terminal")
	gitTest(t, fixture.canonical, "push", "origin", "main")
	installMergedPullRequestFixture(t, head, time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC))
	outcome, err := Cleanup(context.Background(), CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: task, Base: "main", Apply: true, Now: func() time.Time { return time.Date(2026, time.July, 3, 12, 0, 0, 0, time.UTC) }})
	if err != nil || len(outcome.Results) != 1 || !outcome.Results[0].Applied {
		t.Fatalf("native cleanup: %+v %v", outcome, err)
	}
	for i := 0; i < 2; i++ {
		completed, err := completedAdoptedHistory(fixture.home, fixture.projectsRoot, claim)
		listed, listErr := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: task})
		if err != nil || !completed || listErr != nil || len(listed.Results) != 0 || len(listed.Diagnostics) != 0 {
			t.Fatalf("completed history obstructs repeated inventory: %t %v %+v %v", completed, err, listed, listErr)
		}
	}
	terminalPath := filepath.Join(fixture.home, "worklogs", claim.EffortID, "runs", claim.RunID, "terminals", claim.ClaimID+".json")
	terminalBytes, err := os.ReadFile(terminalPath)
	if err != nil {
		t.Fatal(err)
	}
	entry := ListResult{Task: task, Repository: claim.Repository, CanonicalDir: fixture.canonical, WorktreeDir: path, Branch: claim.Branch, HeadSHA: head}
	backlogPath := lifecycleBacklogPath(fixture.home, lifecycleBacklogID(entry, "removed"))
	backlogBytes, err := os.ReadFile(backlogPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"missing-terminal", "corrupt-terminal", "unfinished-backlog"} {
		t.Run(kind, func(t *testing.T) {
			switch kind {
			case "missing-terminal":
				if err := os.Remove(terminalPath); err != nil {
					t.Fatal(err)
				}
			case "corrupt-terminal":
				if err := os.WriteFile(terminalPath, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unfinished-backlog":
				var record lifecycleBacklogRecord
				if err := json.Unmarshal(backlogBytes, &record); err != nil {
					t.Fatal(err)
				}
				record.Stage = lifecycleStageRemovingLocalBranch
				raw, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(backlogPath, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			completed, proofErr := completedAdoptedHistory(fixture.home, fixture.projectsRoot, claim)
			listed, listErr := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: task})
			if completed || proofErr == nil || listErr != nil || len(listed.Results) != 0 || len(listed.Diagnostics) != 1 || listed.Diagnostics[0].DiscoveryScope != "selected-proof" || listed.Diagnostics[0].Task != task {
				t.Fatalf("invalid terminal acquired authority: %t %v %+v %v", completed, proofErr, listed, listErr)
			}
			if err := os.WriteFile(terminalPath, terminalBytes, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(backlogPath, backlogBytes, 0600); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(claimPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("immutable claim changed: %v", err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("history recreated checkout: %v", err)
			}
		})
	}
}

//nolint:paralleltest // native fixture owns process-wide Git/WB settings.
func TestAbsentActiveAdoptedCheckoutCannotBecomeCompletedHistory(t *testing.T) {
	fixture := newGitFixture(t)
	task, path := adoptAndCommit(t, fixture, "feature/unsealed-adopted-history", "feature.txt")
	claim, _, _, err := activeWorkLogClaimReadOnly(fixture.home, path)
	if err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(fixture.home, "worklogs", claim.EffortID, "runs", claim.RunID, "claims", claim.ClaimID+".json")
	before, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "worktree", "remove", "--force", path)
	completed, proofErr := completedAdoptedHistory(fixture.home, fixture.projectsRoot, claim)
	results, diagnostics := listTaskScopedClaimedRegistryWorktrees(context.Background(), fixture.projectsRoot, fixture.home, nil, map[string]bool{task: true}, "main", "", "", false, 1, nil, inspectPolicy{})
	if completed || proofErr == nil || len(results) != 0 || len(diagnostics) != 1 || diagnostics[0].Task != task || diagnostics[0].DiscoveryScope != "selected-proof" {
		t.Fatalf("unsealed absence gained authority: %t %v %+v %+v", completed, proofErr, results, diagnostics)
	}
	after, err := os.ReadFile(claimPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("active immutable claim changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.home, "worklogs", claim.EffortID, "runs", claim.RunID, "terminals", claim.ClaimID+".json")); !os.IsNotExist(err) {
		t.Fatalf("discovery fabricated terminal: %v", err)
	}
}
