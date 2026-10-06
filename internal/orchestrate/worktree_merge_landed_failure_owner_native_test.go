package orchestrate

import (
	"testing"
)

func landedFailureOwnerFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt) {
	t.Helper()
	f := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, f, "landed-failure-owner-source", "feature/landed-failure-owner", "owner.txt", "native owner\n")
	r, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	r.Status = WorktreeMergeValidationFailed
	r.Failure = "historical failed validation"
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, f.canonical, "update-ref", "refs/heads/main", r.Candidate.SHA)
	runEngineGit(t, f.canonical, "push", "origin", "main")
	return f, r
}
