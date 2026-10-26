package orchestrate

import "testing"

func TestUnpublishedConflictClaimBaseRequiresExactProvedPredecessorForSources(t *testing.T) {
	t.Parallel()
	lineage := []UnpublishedConflictRebatchProof{{Candidate: WorktreeMergeCandidate{Branch: "wb/integration/main/prior", SHA: "exact-prior-head"}}}
	for _, tc := range []struct {
		name, base, sha string
		source          bool
		proofs          []UnpublishedConflictRebatchProof
		want            bool
	}{
		{name: "ordinary target base", base: "main", sha: "target-base", want: true},
		{name: "source uses exact predecessor pair", base: "wb/integration/main/prior", sha: "exact-prior-head", source: true, proofs: lineage, want: true},
		{name: "same head on unrelated branch", base: "unrelated", sha: "exact-prior-head", source: true, proofs: lineage},
		{name: "same branch with another head", base: "wb/integration/main/prior", sha: "unproved-head", source: true, proofs: lineage},
		{name: "branch without authenticated lineage", base: "wb/integration/main/prior", sha: "exact-prior-head", source: true},
		{name: "candidate cannot inherit predecessor exception", base: "wb/integration/main/prior", sha: "exact-prior-head", proofs: lineage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := unpublishedConflictClaimBaseMatches(tc.base, tc.sha, "main", tc.source, tc.proofs); got != tc.want {
				t.Fatalf("base pair %q/%q allowed=%t want=%t", tc.base, tc.sha, got, tc.want)
			}
		})
	}
}
