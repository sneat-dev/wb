package orchestrate

import (
	"testing"
	"time"
)

func TestPreparedRebatchOriginalEligibilityPreservesAuthenticatedShapes(t *testing.T) {
	t.Parallel()
	prepared := WorktreeMergeReceipt{Phase: WorktreeMergePhasePrepare, Status: WorktreeMergePrepared}
	for _, tc := range []struct {
		name          string
		receipt       WorktreeMergeReceipt
		authenticated bool
		want          bool
	}{
		{"prepared", prepared, false, true},
		{"preparing authenticated", WorktreeMergeReceipt{Phase: WorktreeMergePhasePrepare, Status: WorktreeMergePreparing}, true, true},
		{"preparing unauthenticated", WorktreeMergeReceipt{Phase: WorktreeMergePhasePrepare, Status: WorktreeMergePreparing}, false, false},
		{"land phase prepared", WorktreeMergeReceipt{Phase: WorktreeMergePhaseLand, Status: WorktreeMergePrepared}, true, false},
		{"prepared PR", WorktreeMergeReceipt{Phase: WorktreeMergePhasePrepare, Status: WorktreeMergePrepared, PullRequest: "41"}, true, false},
		{"prepared published", WorktreeMergeReceipt{Phase: WorktreeMergePhasePrepare, Status: WorktreeMergePrepared, PublishedCandidateSHA: "sha"}, true, false},
		{"prepared landed", WorktreeMergeReceipt{Phase: WorktreeMergePhasePrepare, Status: WorktreeMergePrepared, LandingSHA: "sha"}, true, false},
		{"terminal", WorktreeMergeReceipt{Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeComplete}, true, false},
		{"published", WorktreeMergeReceipt{Phase: WorktreeMergePhaseLand, Status: WorktreeMergePublished, PullRequest: "41", Candidate: WorktreeMergeCandidate{SHA: "sha"}, PublishedCandidateSHA: "sha"}, false, true},
		{"failed checks", WorktreeMergeReceipt{Phase: WorktreeMergePhaseLand, Status: WorktreeMergeChecksFailed, PullRequest: "41", Candidate: WorktreeMergeCandidate{SHA: "sha"}, PublishedCandidateSHA: "sha"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := preparedRebatchOriginalEligible(tc.receipt, tc.authenticated); got != tc.want {
				t.Fatalf("eligibility=%t want %t for %+v", got, tc.want, tc.receipt)
			}
		})
	}
}

func TestPreparedRebatchReplacementRequiresExactPathCandidateAndSources(t *testing.T) {
	t.Parallel()
	source := WorktreeMergeSource{Task: "source", Worktree: "/owned/source", Branch: "feature", SHA: "source-sha"}
	candidate := WorktreeMergeCandidate{Task: "candidate", Worktree: "/owned/candidate", Branch: "integration", SHA: "candidate-sha"}
	original := WorktreeMergePreparedRebatch{ReplacementReceiptPath: "/owned/replacement.json", Replacement: candidate, Sources: []WorktreeMergeSource{source}}
	for _, name := range []string{"exact", "path", "candidate", "source"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			replacement := WorktreeMergeReceipt{ReceiptPath: original.ReplacementReceiptPath, Candidate: candidate, Sources: []WorktreeMergeSource{source}}
			switch name {
			case "path":
				replacement.ReceiptPath += ".different"
			case "candidate":
				replacement.Candidate.SHA = "other"
			case "source":
				replacement.Sources[0].SHA = "other"
			}
			if got := preparedRebatchReplacementMatches(original, replacement); got != (name == "exact") {
				t.Fatalf("replacement match=%t for %s", got, name)
			}
		})
	}
}

func TestCompletePreparedRebatchCopiesReplacementSources(t *testing.T) {
	t.Parallel()
	replacement := WorktreeMergeReceipt{ReceiptPath: "/owned/replacement.json", Candidate: WorktreeMergeCandidate{SHA: "candidate"}, Sources: []WorktreeMergeSource{{SHA: "source"}}}
	before := time.Now().UTC()
	got := completePreparedWorktreeMergeRebatch(WorktreeMergePreparedRebatch{ReceiptPath: "/owned/original.json"}, replacement)
	if !preparedRebatchReplacementMatches(got, replacement) || got.ID == "" || got.ID != preparedRebatchID(got) || got.RecordedAt.Before(before) || got.RecordedAt.After(time.Now().UTC()) {
		t.Fatalf("completed rebatch=%+v", got)
	}
	replacement.Sources[0].SHA = "changed"
	if got.Sources[0].SHA != "source" {
		t.Fatalf("completed sources alias caller: %+v", got.Sources)
	}
}
