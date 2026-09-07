package orchestrate

import (
	"context"
	"os"
	"testing"
)

// TestFindAbsorbedConflictAcknowledgementReadsAValidatedSidecar proves the
// read-only lookup other packages can use (see absorbed_conflict_lookup.go)
// finds the same receipt and acknowledgement AcknowledgeAbsorbedConflict just
// wrote, without duplicating its matching or validation logic.
func TestFindAbsorbedConflictAcknowledgementReadsAValidatedSidecar(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSourceOnBase(t, fixture, "task-lookup", "feature/lookup", "main", "lookup.txt", "lookup\n")

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main",
		Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeConflict
	receipt.Failure = "source changed during prepare: recovery fixture"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}

	runEngineGit(t, fixture.canonical, "fetch", "origin")
	runEngineGit(t, fixture.canonical, "checkout", "main")
	runEngineGit(t, fixture.canonical, "merge", "--ff-only", receipt.Sources[0].SHA)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	if err := os.RemoveAll(source.WorktreeDir); err != nil {
		t.Fatal(err)
	}

	// Before any acknowledgement is written, the lookup finds the receipt but
	// reports it unacknowledged.
	before, foundBefore, err := FindAbsorbedConflictAcknowledgement(fixture.githubDir, receipt.Candidate.Task, receipt.Candidate.Worktree)
	if err != nil {
		t.Fatal(err)
	}
	if !foundBefore {
		t.Fatal("found = false, want the receipt matched by candidate task/worktree")
	}
	if before.Acknowledged {
		t.Fatalf("lookup = %+v, want unacknowledged before AcknowledgeAbsorbedConflict runs", before)
	}
	if before.ReceiptPath != receipt.ReceiptPath {
		t.Fatalf("receipt path = %q, want %q", before.ReceiptPath, receipt.ReceiptPath)
	}

	ack, err := AcknowledgeAbsorbedConflict(context.Background(), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "audited absorbed conflict",
	})
	if err != nil {
		t.Fatal(err)
	}

	after, foundAfter, err := FindAbsorbedConflictAcknowledgement(fixture.githubDir, receipt.Candidate.Task, receipt.Candidate.Worktree)
	if err != nil {
		t.Fatal(err)
	}
	if !foundAfter {
		t.Fatal("found = false, want the receipt matched by candidate task/worktree")
	}
	if !after.Acknowledged {
		t.Fatalf("lookup = %+v, want acknowledged after AcknowledgeAbsorbedConflict --apply", after)
	}
	if after.Acknowledgement.ID != ack.ID {
		t.Fatalf("acknowledgement ID = %q, want %q", after.Acknowledgement.ID, ack.ID)
	}
}

// TestFindAbsorbedConflictAcknowledgementNoMatch proves an unrelated
// task/worktree pair -- no receipt under this projects root names it as a
// candidate -- reports found=false rather than a false match or an error.
func TestFindAbsorbedConflictAcknowledgementNoMatch(t *testing.T) {
	fixture := newEngineFixture(t)
	lookup, found, err := FindAbsorbedConflictAcknowledgement(fixture.githubDir, "no-such-task", "/tmp/no-such-worktree")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("found = true, want false: %+v", lookup)
	}
}
