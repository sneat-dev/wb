package orchestrate

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// These are receipt shape contracts; they supply no native terminal evidence.
func TestTerminalSyncOwnerTaskSetPreservesReceiptAndSortsAllAssetKinds(t *testing.T) {
	t.Parallel()
	r := WorktreeMergeReceipt{Sources: []WorktreeMergeSource{{Task: "z"}, {Task: "a"}, {Task: "z"}, {}}, Candidate: WorktreeMergeCandidate{Task: "b"}, RebatchedCandidates: []WorktreeMergeCandidate{{Task: "a"}, {Task: "c"}, {Task: "c"}, {}}}
	want := []string{"a", "b", "c", "z"}
	if got := sortedUniqueMergeTasks(r); !reflect.DeepEqual(got, want) {
		t.Fatalf("tasks=%v", got)
	}
	r.Candidate.Task = "a"
	if got := sortedUniqueMergeTasks(r); !reflect.DeepEqual(got, []string{"a", "c", "z"}) {
		t.Fatalf("duplicate candidate=%v", got)
	}
	r.Candidate.Task = ""
	if got := sortedUniqueMergeTasks(r); !reflect.DeepEqual(got, []string{"a", "c", "z"}) {
		t.Fatalf("empty candidate=%v", got)
	}
	if got := sortedUniqueMergeTasks(WorktreeMergeReceipt{}); len(got) != 0 {
		t.Fatalf("empty receipt=%v", got)
	}
	if len(r.Sources) != 4 || r.Sources[0].Task != "z" {
		t.Fatal("input receipt altered")
	}
}

func TestTerminalSyncOwnerExpectationPolicyAndConcreteDedup(t *testing.T) {
	t.Parallel()
	candidate := WorktreeMergeCandidate{Task: "b", Worktree: "/private/candidate", Branch: "wb/candidate", SHA: "candidate"}
	source := WorktreeMergeSource{Task: "a", Worktree: "/private/source", Branch: "feature/source", SHA: "source"}
	old := WorktreeMergeCandidate{Task: "c", Worktree: "/private/old", Branch: "wb/old", SHA: "old"}
	baseline := WorktreeMergeReceipt{Repository: "acme/app", Target: "main", Candidate: candidate, Sources: []WorktreeMergeSource{source, source}, RebatchedCandidates: []WorktreeMergeCandidate{old, old, candidate}}
	got, err := terminalWorkLogExpectations(baseline)
	want := []worktrees.TerminalWorkLogExpectation{{Task: "a", Repository: "acme/app", Worktree: source.Worktree, Branch: source.Branch, FinalCommit: source.SHA}, {Task: "b", Repository: "acme/app", Worktree: candidate.Worktree, Branch: candidate.Branch, Base: "main", FinalCommit: candidate.SHA}, {Task: "c", Repository: "acme/app", Worktree: old.Worktree, Branch: old.Branch, Base: "main", FinalCommit: old.SHA}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("expectations=%+v %v", got, err)
	}
	for _, mode := range []string{"repository", "target", "candidate task", "candidate path", "candidate branch", "candidate sha", "source identity", "rebatched identity", "source conflict", "rebatched conflict", "source candidate base conflict"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			r := baseline
			r.Sources = append([]WorktreeMergeSource(nil), baseline.Sources...)
			r.RebatchedCandidates = append([]WorktreeMergeCandidate(nil), baseline.RebatchedCandidates...)
			prefix := "receipt lacks exact candidate identity"
			switch mode {
			case "repository":
				r.Repository = ""
			case "target":
				r.Target = ""
			case "candidate task":
				r.Candidate.Task = ""
			case "candidate path":
				r.Candidate.Worktree = ""
			case "candidate branch":
				r.Candidate.Branch = ""
			case "candidate sha":
				r.Candidate.SHA = ""
			case "source identity":
				r.Sources[0].SHA = ""
				prefix = "receipt lacks exact source identity"
			case "rebatched identity":
				r.RebatchedCandidates[0].SHA = ""
				prefix = "receipt lacks exact rebatched candidate identity"
			case "source conflict":
				r.Sources[1].SHA = "changed"
				prefix = "receipt has conflicting terminal cleanup identities for task a"
			case "rebatched conflict":
				r.RebatchedCandidates[1].SHA = "changed"
				prefix = "receipt has conflicting terminal cleanup identities for task c"
			case "source candidate base conflict":
				r.Sources = []WorktreeMergeSource{{Task: candidate.Task, Worktree: candidate.Worktree, Branch: candidate.Branch, SHA: candidate.SHA}}
				prefix = "receipt has conflicting terminal cleanup identities for task b"
			}
			got, e := terminalWorkLogExpectations(r)
			if e == nil || got != nil || !strings.HasPrefix(e.Error(), prefix) {
				t.Fatalf("%s = %+v %v", mode, got, e)
			}
		})
	}
}

func TestTerminalSyncOwnerRecoveryNilAndInvalidIdentityBeforeFilesystem(t *testing.T) {
	t.Parallel()
	if complete, err := recoverAlreadyTerminalizedWorktreeMergeCleanup(t.Context(), t.TempDir(), nil, 0, 0); err == nil || complete || err.Error() != "nil merge receipt" {
		t.Fatalf("nil recovery=%t %v", complete, err)
	}
	r := WorktreeMergeReceipt{}
	if complete, err := recoverAlreadyTerminalizedWorktreeMergeCleanup(t.Context(), t.TempDir(), &r, 0, 0); err == nil || complete || !strings.Contains(err.Error(), "lacks exact candidate identity") {
		t.Fatalf("identity=%t %v", complete, err)
	}
}
