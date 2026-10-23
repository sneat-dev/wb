//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

// landBatchFixture makes the land fixture's pull request a WB batch of one
// recorded source, with a first-parent history straight back to its base.
func landBatchFixture(t *testing.T) (*landFixture, PullRequestLandOptions, *fakeSourceRemote) {
	t.Helper()
	fixture := newLandFixture(t, "bump/deps", "go.mod", "go.sum")
	state := BatchState{Version: 1, Repository: "acme/app", Target: "main", Branch: "bump/deps", Base: fixture.baseSHA,
		MergeOptions: batchMergeOptions, BuiltBy: []string{"opus@claude-code@builder"},
		Steps: []BatchStep{{Kind: BatchStepSource, PullRequest: 510, Head: fixture.commitSHAs[0], State: BatchStateMerged}}}
	fixture.writeState(t, "body", jsonEscapedBody(RenderBatchBody(state)))
	review := "**Review by opus@codex@delta-reviewer**\nReviewed-Head: " + fixture.headSHA + "\n"
	fixture.writeState(t, "comment-body", jsonEscapedBody(review))
	parents := map[string][]string{}
	previous := fixture.baseSHA
	for _, sha := range fixture.commitSHAs {
		parents[sha] = []string{previous}
		previous = sha
	}
	remote := &fakeSourceRemote{}
	ports := &batchLandingPorts{
		comment: func(context.Context, string, int64) (BatchComment, error) {
			return BatchComment{Body: review, PullRequest: 7}, nil
		},
		parents:    func(_ context.Context, _, sha string) ([]string, error) { return parents[sha], nil },
		contains:   func(context.Context, string, string, string) (bool, string) { return true, "" },
		targetHead: func(context.Context, string, string) (string, string) { return fixture.baseSHA, "" },
		pullRequest: func(context.Context, string, int) (githubchecks.PullRequestView, error) {
			return githubchecks.PullRequestView{}, errors.New("not read")
		},
		source: remote,
	}
	opts := landOptions(fixture)
	opts.batchPorts = ports
	return fixture, opts, remote
}

//nolint:paralleltest // Native Land fixture sets WB_PROJECTS_ROOT and installs the process-wide GH/PATH shim.
func TestE2ELandRefusesABatchWithoutABatchReview(t *testing.T) {
	_, opts, _ := landBatchFixture(t)
	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandRefused || result.RefusalCode != LandRefusalBatchDeltaUnreviewed {
		t.Fatalf("outcome = %s/%s (%s)", result.Outcome, result.RefusalCode, result.Reason)
	}
}

//nolint:paralleltest // Native Land fixture sets WB_PROJECTS_ROOT and installs the process-wide GH/PATH shim.
func TestE2ELandLandsAReviewedBatchAndRecordsItsSources(t *testing.T) {
	_, opts, _ := landBatchFixture(t)
	opts.ApprovedBy = "https://github.com/acme/app/pull/7#issuecomment-2"
	merged := githubchecks.PullRequestView{Number: 510, Merged: true}
	opts.batchPorts.pullRequest = func(context.Context, string, int) (githubchecks.PullRequestView, error) { return merged, nil }
	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess || result.Batch == nil || result.Evidence["batch"] != "bump/deps" {
		t.Fatalf("outcome = %s (%s: %s), batch = %v", result.Outcome, result.RefusalCode, result.Reason, result.Batch)
	}
	if len(result.BatchSources) != 1 || result.BatchSources[0].State != "merged" {
		t.Fatalf("batch sources = %+v", result.BatchSources)
	}
}

//nolint:paralleltest // Native Land fixture sets WB_PROJECTS_ROOT and installs the process-wide GH/PATH shim.
func TestE2ELandFailsWhenTheBatchTargetCannotBeRead(t *testing.T) {
	_, opts, _ := landBatchFixture(t)
	opts.ApprovedBy = "https://github.com/acme/app/pull/7#issuecomment-2"
	opts.batchPorts.targetHead = func(context.Context, string, string) (string, string) { return "", "target unavailable" }
	if _, err := LandPullRequest(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "target unavailable") {
		t.Fatalf("err = %v", err)
	}
}
