//go:build e2e

package orchestrate

import (
	"context"
	"strings"
	"testing"
)

//nolint:paralleltest // Installs the private gh executable through process-wide PATH and WB_TEST_REMOTE/WB_TEST_GH_LOG/XDG_STATE_HOME, then mutates WB_TEST_PR_STATE/WB_TEST_CANDIDATE_SHA/WB_TEST_MERGE_COMMIT_SHA/WB_TEST_COMPARE_FAILURE_BASE with t.Setenv.
func TestE2EStrandedLandingRejectsUnrelatedPublishedPredecessor(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "stranded-predecessor-source", "feature/stranded-predecessor", "predecessor.txt", "candidate\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.PullRequest = "https://example.test/acme/app/pull/96"
	targetTree := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", receipt.TargetSHA+"^{tree}"))
	unrelated := strings.TrimSpace(runEngineGit(t, fixture.canonical, "commit-tree", targetTree, "-p", receipt.TargetSHA, "-m", "unrelated published predecessor"))
	runEngineGit(t, fixture.canonical, "push", "origin", unrelated+":refs/heads/unrelated-published-predecessor")
	receipt.PublishedCandidateSHA = unrelated
	runEngineGit(t, fixture.canonical, "merge", "--ff-only", receipt.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	installStrandedLandingGH(t, receipt.PullRequest, fixture.repository.CloneURL)
	t.Setenv("WB_TEST_PR_STATE", "MERGED")
	t.Setenv("WB_TEST_CANDIDATE_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_MERGE_COMMIT_SHA", receipt.Candidate.SHA)
	_, _, _, _, _, err = proveStrandedPullRequestLanding(context.Background(), receipt)
	if err == nil || !strings.Contains(err.Error(), "does not contain published predecessor") {
		t.Fatalf("unrelated published predecessor proof = %v", err)
	}
	t.Setenv("WB_TEST_COMPARE_FAILURE_BASE", unrelated)
	_, _, _, _, _, err = proveStrandedPullRequestLanding(context.Background(), receipt)
	if err == nil || !strings.Contains(err.Error(), "remote comparison unavailable") {
		t.Fatalf("remote predecessor comparison failure = %v", err)
	}
}
