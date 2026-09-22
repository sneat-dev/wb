package orchestrate

import "testing"

func TestValidateRetiredPrepareCandidateReceiptRejectsCandidateOrSourceConfusion(t *testing.T) {
	const targetSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	receipt := WorktreeMergeReceipt{
		ID: "legacy-prepare", Lane: worktreeMergeLaneID("acme/app", "deleted-target"),
		Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeConflict,
		Repository: "acme/app", Target: "deleted-target", TargetSHA: targetSHA,
		Candidate: WorktreeMergeCandidate{Task: "candidate", Worktree: "/candidate", Branch: "wb/integration/candidate"},
		Sources:   []WorktreeMergeSource{{Task: "source", Worktree: "/source", Branch: "feature/source", SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},
	}
	path := "/reports/legacy-prepare.json"
	receipt.ReceiptPath = path
	if err := validateRetiredPrepareCandidateReceipt(receipt, path); err != nil {
		t.Fatalf("valid legacy candidate-only shape rejected: %v", err)
	}

	withCandidateSHA := receipt
	withCandidateSHA.Candidate.SHA = targetSHA
	if err := validateRetiredPrepareCandidateReceipt(withCandidateSHA, path); err == nil {
		t.Fatal("receipt recording a candidate SHA was accepted")
	}
	withPublishedCandidate := receipt
	withPublishedCandidate.PublishedCandidateSHA = targetSHA
	if err := validateRetiredPrepareCandidateReceipt(withPublishedCandidate, path); err == nil {
		t.Fatal("published candidate receipt was accepted")
	}
	withConfusedSource := receipt
	withConfusedSource.Sources[0].Worktree = receipt.Candidate.Worktree
	if err := validateRetiredPrepareCandidateReceipt(withConfusedSource, path); err == nil {
		t.Fatal("candidate/source worktree confusion was accepted")
	}
}
