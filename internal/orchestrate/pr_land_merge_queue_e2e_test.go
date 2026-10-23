package orchestrate

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/landinglane"
)

// datatugMergeQueueRules is the shape datatug/datatug-apps adopted: required
// checks without the strict up-to-date policy, and a merge queue that merges
// with a merge commit once every check in the group is green.
const datatugMergeQueueRules = `[{"type":"required_status_checks","ruleset_id":25200937,"ruleset_source_type":"Repository","ruleset_source":"acme/app","parameters":{"strict_required_status_checks_policy":false,"required_status_checks":[{"context":"CI","integration_id":42}]}},` +
	`{"type":"merge_queue","ruleset_id":25200937,"ruleset_source_type":"Repository","ruleset_source":"acme/app","parameters":{"merge_method":"MERGE","grouping_strategy":"ALLGREEN"}}]`

func newMergeQueueLandFixture(t *testing.T, branch, outcome string) *landFixture {
	t.Helper()
	fixture := newLandFixture(t, branch, "go.mod", "go.sum")
	fixture.writeState(t, "rules", datatugMergeQueueRules)
	fixture.writeState(t, "queue-outcome", outcome)
	return fixture
}

// assertNoDirectLanding pins the bypass-actor guarantee. The fixture's merge
// endpoint answers like GitHub does for a ruleset bypass actor (an
// organization admin with bypass mode "always"): it would merge. WB must not
// ask it to, nor arm auto-merge, nor update the branch.
func assertNoDirectLanding(t *testing.T, fixture *landFixture) {
	t.Helper()
	log := fixture.ghLog(t)
	for _, forbidden := range []string{"pulls/7/merge", "enablePullRequestAutoMerge", "update-branch"} {
		if strings.Contains(log, forbidden) {
			t.Fatalf("a merge-queue landing called %q:\n%s", forbidden, log)
		}
	}
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestLandOnAMergeQueueTargetEnqueuesAndNeverMergesDirectlyEvenAsABypassActor(t *testing.T) {
	fixture := newMergeQueueLandFixture(t, "bump/queued", "merge")
	// Another landing reaches main while this candidate's checks run. Without
	// the queue that triggers an update-branch and a fresh CI cycle; the queue
	// builds its merge group on the current main instead.
	fixture.writeState(t, "advance-on-checks", "1")

	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess || !result.LandingOnBase || result.MergeSHA == "" {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	assertNoDirectLanding(t, fixture)
	if args := fixture.readState(t, "enqueue-args"); !strings.Contains(args, "head="+fixture.headSHA) || !strings.Contains(args, "id=PR_kwDOtest7") {
		t.Fatalf("enqueue was not pinned to the observed head: %q", args)
	}
	if result.MergeQueue == nil || !result.MergeQueue.Enqueued || result.MergeQueue.MergeMethod != "merge" || result.MergeQueue.RulesetID != 25200937 {
		t.Fatalf("merge queue receipt = %+v", result.MergeQueue)
	}
	if result.Evidence["merged_by"] != "github merge queue" || !strings.Contains(result.Evidence["auto_merge"], "merges through its merge queue") ||
		!strings.Contains(result.Evidence["merge_queue"], "never merges it directly") || result.Subject != "" {
		t.Fatalf("evidence = %v, subject = %q", result.Evidence, result.Subject)
	}
	if !strings.Contains(result.Checks.Reason, "delegated to GitHub's merge queue") {
		t.Fatalf("candidate receipt = %q", result.Checks.Reason)
	}
	if !strings.Contains(strings.Join(result.ManualEquivalent, "\n"), "enqueuePullRequest") {
		t.Fatalf("manual equivalent still names a direct merge: %v", result.ManualEquivalent)
	}
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestLandOnAMergeQueueTargetReportsAFailedMergeGroupWithAResumeCommand(t *testing.T) {
	fixture := newMergeQueueLandFixture(t, "bump/queue-fails", "fail")

	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandFindings || result.RefusalCode != LandRefusalMergeGroupFailed || result.ExitCode() != 1 {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if !strings.Contains(result.Reason, "(failed_checks)") || result.SanctionedCommand != "wb pr land acme/app#7 --timeout 45m --keep --requeue" {
		t.Fatalf("reason = %q, command = %q", result.Reason, result.SanctionedCommand)
	}
	if result.MergeQueue.RemovalReason != "failed_checks" || result.LandingOnBase {
		t.Fatalf("receipt = %+v", result)
	}
	assertNoDirectLanding(t, fixture)
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestLandOnAMergeQueueTargetResumesAQueuedPullRequestWithoutEnqueuingAgain(t *testing.T) {
	fixture := newMergeQueueLandFixture(t, "bump/queue-resume", "pending")
	options := landOptions(fixture)
	options.Slice, options.CheckPollInterval = 3*time.Second, 100*time.Millisecond
	// A landing that holds the lane gives it up once queued; both runs below
	// acquire it again for their own short pre-enqueue stretch.
	options.Lane = LaneGuardRequest{Owner: landinglane.Owner{WBSessionID: "wbs-queue-land", PID: os.Getpid(), Command: "wb pr land"}}

	pending, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Outcome != LandFindings || pending.RefusalCode != LandRefusalMergeQueuePending || !pending.MergeQueue.Enqueued ||
		!strings.Contains(pending.Reason, "GitHub merges it without WB") || pending.SanctionedCommand != "wb pr land acme/app#7 --timeout 45m --keep" {
		t.Fatalf("pending = %s (%s): %s; command %q", pending.Outcome, pending.RefusalCode, pending.Reason, pending.SanctionedCommand)
	}

	fixture.writeState(t, "queue-outcome", "merge")
	fixture.writeState(t, "enqueue-args", "")
	landed, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if landed.Outcome != LandSuccess || landed.MergeQueue.Enqueued || fixture.readState(t, "enqueue-args") != "" {
		t.Fatalf("resume = %s (%s): %s; receipt %+v", landed.Outcome, landed.RefusalCode, landed.Reason, landed.MergeQueue)
	}
	assertNoDirectLanding(t, fixture)
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestLandOnAMergeQueueTargetRefusesAMergeMethodTheQueueWouldOverride(t *testing.T) {
	fixture := newMergeQueueLandFixture(t, "bump/queue-method", "merge")
	options := landOptions(fixture)
	options.MergeMethod, options.MergeMethodExplicit = "rebase", true

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandRefused || result.RefusalCode != LandRefusalMergeQueueOption || result.SanctionedCommand != "wb pr land acme/app#7 --keep" {
		t.Fatalf("outcome = %s (%s): %s; command %q", result.Outcome, result.RefusalCode, result.Reason, result.SanctionedCommand)
	}
	if log := fixture.ghLog(t); strings.Contains(log, "graphql") || strings.Contains(log, "pulls/7/merge") {
		t.Fatalf("a refused landing reached GitHub's write surface:\n%s", log)
	}
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestLandOnAMergeQueueTargetFailsClosedWhenTheRulesAreUnreadable(t *testing.T) {
	fixture := newMergeQueueLandFixture(t, "bump/queue-unreadable", "merge")
	fixture.writeState(t, "rules", "{")

	_, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err == nil || !strings.Contains(err.Error(), "read active branch rules for target main") {
		t.Fatalf("err = %v", err)
	}
	assertNoDirectLanding(t, fixture)
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestLandOnAMergeQueueTargetRecordsAPlanLimitedRulesReadUnderAllowUnfenced(t *testing.T) {
	fixture := newLandFixture(t, "bump/queue-plan-limited", "go.mod", "go.sum")
	fixture.writeState(t, "rules-403", "1")
	options := landOptions(fixture)
	options.AllowUnfenced = true

	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess || !strings.Contains(result.Evidence["merge_queue"], "HTTP 403") || result.MergeQueue != nil {
		t.Fatalf("outcome = %s (%s): %s; evidence %v", result.Outcome, result.RefusalCode, result.Reason, result.Evidence)
	}
}

//nolint:paralleltest // calls a fixture helper (newLandFixture/newEngineFixture/createMergeSource) that calls t.Setenv, which Go's testing package forbids combined with t.Parallel
func TestLandOnAMergeQueueTargetKeepsAnIntegrationHeadBranch(t *testing.T) {
	fixture := newMergeQueueLandFixture(t, "public-demo", "merge")
	fixture.writeState(t, "open-onto", `[{"number":9}]`)
	result, err := LandPullRequest(context.Background(), landOptions(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess || result.MergeQueue == nil || result.BranchDeleted || !strings.Contains(result.HeadRetained, "pull requests have targeted it") {
		t.Fatalf("outcome = %s (%s: %s) deleted=%v retained=%q", result.Outcome, result.RefusalCode, result.Reason, result.BranchDeleted, result.HeadRetained)
	}
	if output, lsErr := runGitAllowFail(defaultRunner, fixture.canonical, "ls-remote", "origin", "refs/heads/public-demo"); lsErr != nil {
		t.Fatal(lsErr)
	} else if strings.TrimSpace(output) == "" {
		t.Fatal("remote branch public-demo was deleted")
	}
}
