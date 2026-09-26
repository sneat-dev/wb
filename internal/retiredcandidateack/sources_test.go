package retiredcandidateack

import "testing"

func TestSameSourcesLengthMismatch(t *testing.T) {
	t.Parallel()

	a := []Source{{Task: "t1", Worktree: "w1", Branch: "b1", SHA: "s1"}}
	b := []Source{}
	if sameSources(a, b) {
		t.Fatalf("sameSources with mismatched lengths = true, want false")
	}
}

func TestSameSourcesEqualAndDiffering(t *testing.T) {
	t.Parallel()

	a := []Source{{Task: "t1", Worktree: "w1", Branch: "b1", SHA: "s1"}}
	b := []Source{{Task: "t1", Worktree: "w1", Branch: "b1", SHA: "s1"}}
	if !sameSources(a, b) {
		t.Fatalf("sameSources with identical entries = false, want true")
	}

	c := []Source{{Task: "t2", Worktree: "w1", Branch: "b1", SHA: "s1"}}
	if sameSources(a, c) {
		t.Fatalf("sameSources with differing entry = true, want false")
	}
}

func TestDistinctCandidateIncompleteOrEmpty(t *testing.T) {
	t.Parallel()

	incomplete := Source{Task: "t1", Worktree: "w1", Branch: "b1"} // SHA missing
	if DistinctCandidate(incomplete, []Source{{Task: "t2", Worktree: "w2", Branch: "b2", SHA: "s2"}}) {
		t.Fatalf("DistinctCandidate with incomplete candidate = true, want false")
	}

	complete := Source{Task: "t1", Worktree: "w1", Branch: "b1", SHA: "s1"}
	if DistinctCandidate(complete, nil) {
		t.Fatalf("DistinctCandidate with no sources = true, want false")
	}
}

func TestDistinctCandidateOverlapping(t *testing.T) {
	t.Parallel()

	candidate := Source{Task: "t1", Worktree: "w1", Branch: "b1", SHA: "s1"}

	// Overlaps on Task with an otherwise-complete source: not distinct.
	overlapping := []Source{{Task: "t1", Worktree: "w2", Branch: "b2", SHA: "s2"}}
	if DistinctCandidate(candidate, overlapping) {
		t.Fatalf("DistinctCandidate overlapping on task = true, want false")
	}

	// Fully disjoint source: distinct.
	disjoint := []Source{{Task: "t2", Worktree: "w2", Branch: "b2", SHA: "s2"}}
	if !DistinctCandidate(candidate, disjoint) {
		t.Fatalf("DistinctCandidate disjoint = false, want true")
	}
}
