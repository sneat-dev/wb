//go:build e2e

package orchestrate

import (
	"context"
	"encoding/json"
	"testing"
)

// landPromotionFixture makes the land fixture's pull request a promotion
// whose commits carry no lane merge, with injected review reads.
func landPromotionFixture(t *testing.T) (*landFixture, PullRequestLandOptions) {
	t.Helper()
	fixture := newLandFixture(t, "public-demo", "go.mod", "go.sum")
	fixture.writeState(t, "body", jsonEscapedBody(promoteMarker+"\nPromotes public-demo into main."))
	// The commits as GitHub lists them, with parents, so the walk runs.
	commits := make([]map[string]any, 0, len(fixture.commitSHAs))
	previous := fixture.baseSHA
	for _, sha := range fixture.commitSHAs {
		commits = append(commits, map[string]any{"sha": sha, "commit": map[string]string{"message": "fix\n\nbody"},
			"parents": []map[string]string{{"sha": previous}}})
		previous = sha
	}
	encoded, err := json.Marshal(commits)
	if err != nil {
		t.Fatal(err)
	}
	fixture.writeState(t, "commits", string(encoded))
	review := "**Review by opus@codex@delta-reviewer**\nReviewed-Head: " + fixture.headSHA + "\n"
	ports := &promoteLandingPorts{
		comment: func(context.Context, string, int64) (IssueComment, error) {
			return IssueComment{Body: review, PullRequest: 7}, nil
		},
		review:   &fakeCommitComments{comments: map[int64]CommitComment{}},
		contains: func(context.Context, string, string, string) (bool, string) { return true, "" },
		operator: func() ReviewerIdentity {
			return ReviewerIdentity{Model: "opus", Harness: "claude-code", Session: "operator"}
		},
		mergeMethod: func(context.Context, string) (string, error) { return "merge", nil },
	}
	opts := landOptions(fixture)
	opts.promotePorts = ports
	return fixture, opts
}

//nolint:paralleltest // Native Land fixture sets WB_PROJECTS_ROOT and installs the process-wide GH/PATH shim.
func TestE2ELandRefusesAPromotionWithoutAFreshReview(t *testing.T) {
	_, opts := landPromotionFixture(t)
	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandRefused || result.RefusalCode != LandRefusalBatchDeltaUnreviewed {
		t.Fatalf("outcome = %s/%s (%s)", result.Outcome, result.RefusalCode, result.Reason)
	}
}

//nolint:paralleltest // Native Land fixture sets WB_PROJECTS_ROOT and installs the process-wide GH/PATH shim.
func TestE2ELandLandsAReviewedPromotion(t *testing.T) {
	_, opts := landPromotionFixture(t)
	opts.ApprovedBy = "https://github.com/acme/app/pull/7#issuecomment-2"
	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess || result.Promotion == nil || result.Evidence["promotion"] == "" {
		t.Fatalf("outcome = %s (%s: %s), promotion = %+v", result.Outcome, result.RefusalCode, result.Reason, result.Promotion)
	}
}

//nolint:paralleltest // Native Land fixture sets WB_PROJECTS_ROOT and installs the process-wide GH/PATH shim.
func TestE2ELandFailsWhenThePromotionHistoryCannotBeRead(t *testing.T) {
	_, opts := landPromotionFixture(t)
	opts.ApprovedBy = "https://github.com/acme/app/pull/7#issuecomment-2"
	opts.promotePorts.contains = func(context.Context, string, string, string) (bool, string) { return false, "502" }
	if _, err := LandPullRequest(context.Background(), opts); err == nil {
		t.Fatal("a failed history read was swallowed")
	}
}

//nolint:paralleltest // Native Land fixture sets WB_PROJECTS_ROOT and installs the process-wide GH/PATH shim.
func TestE2ELandRefusesAPromotionBehindItsTarget(t *testing.T) {
	_, opts := landPromotionFixture(t)
	opts.ApprovedBy = "https://github.com/acme/app/pull/7#issuecomment-2"
	opts.promotePorts.contains = func(_ context.Context, _, ancestor, _ string) (bool, string) { return ancestor != "main", "" }
	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandRefused || result.RefusalCode != LandRefusalBatchBehindTarget {
		t.Fatalf("outcome = %s/%s (%s)", result.Outcome, result.RefusalCode, result.Reason)
	}
}
