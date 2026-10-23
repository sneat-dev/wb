package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"
)

const worktreeMergeQueueRules = `[{"type":"required_status_checks","ruleset_id":12,"ruleset_source_type":"Repository","ruleset_source":"acme/app","parameters":{"strict_required_status_checks_policy":false,"required_status_checks":[{"context":"CI","integration_id":42}]}},` +
	`{"type":"merge_queue","ruleset_id":12,"ruleset_source_type":"Repository","ruleset_source":"acme/app","parameters":{"merge_method":"MERGE","grouping_strategy":"ALLGREEN"}}]`

// prepareQueuedWorktreeMerge prepares one candidate onto a target whose
// rules require a merge queue that behaves as outcome says.
func prepareQueuedWorktreeMerge(t *testing.T, task, outcome string) (engineFixture, *wmEngineGH, WorktreeMergeLandOptions) {
	t.Helper()
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, task+"-source", "feature/"+task, task+".txt", task+"\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	gh := installWorktreeMergeEngineGH(t, fixture, receipt.Candidate.SHA, receipt.Candidate.Branch)
	gh.writeState(t, "rules", worktreeMergeQueueRules)
	gh.writeState(t, "queue-outcome", outcome)
	options := wmEngineLandOptions(fixture, receipt.ReceiptPath)
	options.Route = WorktreeMergeRouteAuto
	return fixture, gh, options
}

func assertWorktreeMergeNeverMergedDirectly(t *testing.T, gh *wmEngineGH) {
	t.Helper()
	log := gh.ghLog(t)
	for _, forbidden := range []string{"pulls/41/merge", "enablePullRequestAutoMerge", "update-branch", "api repos/acme/app\n"} {
		if strings.Contains(log, forbidden) {
			t.Fatalf("a merge-queue worktree landing called %q:\n%s", forbidden, log)
		}
	}
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestWorktreeLandOnAMergeQueueTargetEnqueuesThroughThePullRequestRoute(t *testing.T) {
	_, gh, options := prepareQueuedWorktreeMerge(t, "queue-lands", "merge")
	landed, err := ResumeWorktreeMerge(context.Background(), options)
	if err != nil {
		t.Fatalf("receipt = %+v, err = %v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded || landed.LandingSHA == "" || landed.MergedBy != "github merge queue" || landed.Route.Route != WorktreeMergeRoutePullRequest || !landed.Route.MergeQueue {
		t.Fatalf("receipt = %+v", landed)
	}
	if !strings.Contains(gh.readState(t, "enqueue-args"), "head=") {
		t.Fatalf("the candidate was never enqueued:\n%s", gh.ghLog(t))
	}
	assertWorktreeMergeNeverMergedDirectly(t, gh)
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestWorktreeLandOnAMergeQueueTargetReportsEachUnmergedOutcome(t *testing.T) {
	for _, test := range []struct {
		name, outcome, marker string
		status                WorktreeMergeStatus
		want                  string
	}{
		{name: "merge group failed", outcome: "fail", status: WorktreeMergeChecksFailed, want: LandRefusalMergeGroupFailed + ": "},
		{name: "dequeued", outcome: "dequeue", status: WorktreeMergeConflict, want: LandRefusalMergeQueueDequeued + ": "},
		{name: "still queued", outcome: "pending", status: WorktreeMergeChecksPending, want: "GitHub merges it without WB"},
		{name: "queue unreadable", outcome: "pending", marker: "queue-state-fails", status: WorktreeMergeChecksPending, want: "HTTP 401"},
	} {
		//nolint:paralleltest // Rows share the process-wide fixture environment and private lane evidence.
		t.Run(test.name, func(t *testing.T) {
			_, gh, options := prepareQueuedWorktreeMerge(t, "queue-"+test.outcome, test.outcome)
			if test.marker != "" {
				gh.writeState(t, test.marker, "1")
			}
			options.Timeout, options.CheckPollInterval = 4*time.Second, 50*time.Millisecond
			failed, err := ResumeWorktreeMerge(context.Background(), options)
			if err == nil || failed.Status != test.status || !strings.Contains(err.Error(), test.want) ||
				!strings.Contains(err.Error(), "resume with wb worktree merge resume "+options.Receipt) {
				t.Fatalf("status = %s, err = %v", failed.Status, err)
			}
			assertWorktreeMergeNeverMergedDirectly(t, gh)
		})
	}
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestWorktreeLandFailsClosedWhenTheMergeQueueRuleIsUnreadable(t *testing.T) {
	_, gh, options := prepareQueuedWorktreeMerge(t, "queue-rules-unreadable", "merge")
	gh.writeState(t, "rules", `[{"type":"merge_queue","ruleset_id":12,"parameters":{"merge_method":"OCTOPUS"}}]`)
	failed, err := ResumeWorktreeMerge(context.Background(), options)
	if err == nil || failed.Status != WorktreeMergeConflict || !strings.Contains(err.Error(), `names merge method "OCTOPUS"`) {
		t.Fatalf("status = %s, err = %v", failed.Status, err)
	}
	assertWorktreeMergeNeverMergedDirectly(t, gh)
}
