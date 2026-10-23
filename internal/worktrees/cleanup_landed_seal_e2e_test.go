//go:build e2e

package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

// sealedByCleanup runs cleanup of task and returns the terminal record and
// the outbox receipt it sealed for the claim worktree held.
func sealedByCleanup(t *testing.T, fixture *gitFixture, task, worktree string, now time.Time) (workLogTerminalRecord, workLogPublicEvent) {
	t.Helper()
	projection, err := readWorkLogProjection(worktree)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, Apply: true, DeleteRemote: true,
		OlderThan: 0, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Applied {
		t.Fatalf("cleanup outcome = %#v", outcome.Results)
	}
	var terminal workLogTerminalRecord
	readSealedJSON(t, filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "terminals", projection.ClaimID+".json"), &terminal)
	var event workLogPublicEvent
	readSealedJSON(t, filepath.Join(fixture.home, "worklogs", projection.EffortID, "outbox", projection.RunID+"-"+projection.ClaimID+"-sealed.json"), &event)
	return terminal, event
}

func readSealedJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}

//nolint:paralleltest // newGitFixture changes process-wide Git environment for this native journey.
func TestE2ECleanupOfMergedWorkSealsLandedWithItsTargetAndCommit(t *testing.T) {
	const task = "cleanup-seals-landed"
	fixture, created, head, mergedAt := prepareMergedTask(t, task)
	installMergedPullRequestFixture(t, head, mergedAt)

	terminal, event := sealedByCleanup(t, fixture, task, created.WorktreeDir, mergedAt.Add(time.Hour))

	want := &worktreeclaims.LandedEvidence{Target: "main", LandedSHA: head, Proof: worktreeclaims.LandedProofMergedPullRequest, PullRequest: 17}
	if terminal.Disposition != "landed" || terminal.FinalCommit != head || !worktreeclaims.SameLandedEvidence(terminal.Landed, want) {
		t.Fatalf("terminal = %q at %s with %#v, want landed with %#v", terminal.Disposition, terminal.FinalCommit, terminal.Landed, want)
	}
	if event.Disposition != "landed" || !worktreeclaims.SameLandedEvidence(event.Landed, want) {
		t.Fatalf("outbox receipt = %q with %#v, want landed with %#v", event.Disposition, event.Landed, want)
	}
	// `wb worktree merge` resumes a landed batch by proving its worktrees
	// were removed: a terminal cleanup sealed `landed` is that proof.
	if err := ValidateRemovedTerminalWorkLogs(fixture.projectsRoot, []TerminalWorkLogExpectation{{
		Task: task, Repository: created.Repository, Worktree: created.WorktreeDir, Branch: created.Branch, FinalCommit: head,
	}}); err != nil {
		t.Fatalf("the landed terminal does not prove the removal: %v", err)
	}
}

//nolint:paralleltest // newGitFixture changes process-wide Git environment for this native journey.
func TestE2ECleanupOfAWorktreeThatNeverCommittedSealsRemoved(t *testing.T) {
	const task = "cleanup-seals-removed"
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: task, WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	installPullRequestResponses(t, "[]", "")

	terminal, event := sealedByCleanup(t, fixture, task, created[0].WorktreeDir, time.Date(2026, time.July, 1, 13, 0, 0, 0, time.UTC))

	if terminal.Disposition != "removed" || terminal.Landed != nil || event.Disposition != "removed" || event.Landed != nil {
		t.Fatalf("terminal = %q with %#v, outbox = %q with %#v, want removed without a landing",
			terminal.Disposition, terminal.Landed, event.Disposition, event.Landed)
	}
}
