//go:build e2e

package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

func settleInspector(fixture *landFixture) *testChecksPolicyInspector {
	return &testChecksPolicyInspector{
		required:     []githubchecks.RequiredRemoteCheck{{Name: "CI", IntegrationID: 42}},
		reqAuthority: "server",
		targetSHA:    fixture.baseSHA,
		commitChecks: []githubchecks.RemoteCheck{
			{Name: "CI", Conclusion: "success", Bucket: "pass", AppID: 42},
			{Name: "deploy-gate", Conclusion: "failure", Bucket: "fail"},
		},
	}
}

//nolint:paralleltest // Native Land fixture sets WB_PROJECTS_ROOT and installs the process-wide GH/PATH shim.
func TestE2ELandWaitsForARunningWaivedCheckInsteadOfJudgingIt(t *testing.T) {
	fixture := newLandFixture(t, "bump/deps", "go.mod", "go.sum")
	fixture.writeState(t, "check-runs", `{
		"total_count": 2,
		"check_runs": [
			{"name": "CI", "status": "completed", "conclusion": "success", "app": {"id": 42}},
			{"name": "deploy-gate", "status": "in_progress", "conclusion": null, "app": {"id": 100}}
		]
	}`)
	opts := landOptions(fixture)
	opts.Inspector = settleInspector(fixture)
	opts.WaiveChecks = []string{"deploy-gate"}
	opts.WaiveReason = "aggregator red on public-demo"
	opts.NoAutoMerge = true
	opts.Slice = 300 * time.Millisecond

	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandFindings || result.RefusalCode != LandRefusalWaivedCheckPending {
		t.Fatalf("outcome = %s/%s (%s), want findings/%s", result.Outcome, result.RefusalCode, result.Reason, LandRefusalWaivedCheckPending)
	}
	if len(result.WaivedChecks) != 0 {
		t.Fatalf("a running check got a waiver verdict: %#v", result.WaivedChecks)
	}
}

//nolint:paralleltest // Native Land fixture sets WB_PROJECTS_ROOT and installs the process-wide GH/PATH shim.
func TestE2ELandReportsARealFailureOverARunningWaivedCheck(t *testing.T) {
	fixture := newLandFixture(t, "bump/deps", "go.mod", "go.sum")
	fixture.writeState(t, "check-runs", `{
		"total_count": 2,
		"check_runs": [
			{"name": "CI", "status": "completed", "conclusion": "failure", "app": {"id": 42}},
			{"name": "deploy-gate", "status": "in_progress", "conclusion": null, "app": {"id": 100}}
		]
	}`)
	opts := landOptions(fixture)
	opts.Inspector = settleInspector(fixture)
	opts.WaiveChecks = []string{"deploy-gate"}
	opts.WaiveReason = "aggregator red on public-demo"
	opts.NoAutoMerge = true

	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.RefusalCode != LandRefusalChecksFailed {
		t.Fatalf("refusal = %s (%s), want %s", result.RefusalCode, result.Reason, LandRefusalChecksFailed)
	}
	if len(result.WaivedChecks) != 0 {
		t.Fatalf("waiver verdict recorded beside a real failure: %#v", result.WaivedChecks)
	}
}

//nolint:paralleltest // Native Land fixture sets WB_PROJECTS_ROOT and installs the process-wide GH/PATH shim.
func TestE2ELandRecordsWaiversAsMootOnceGitHubMerged(t *testing.T) {
	fixture := newLandFixture(t, "feature")
	fixtureMarker(t, fixture, "github-merges-on-arm")
	opts := landOptions(fixture)
	opts.WaiveChecks = []string{"deploy-gate"}
	opts.WaiveReason = "aggregator red on public-demo"

	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s (%s): %s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if result.Evidence["waivers"] != "moot (merged by GitHub)" {
		t.Fatalf("waivers evidence = %q", result.Evidence["waivers"])
	}
}

//nolint:paralleltest // Native Land fixture sets WB_PROJECTS_ROOT and installs the process-wide GH/PATH shim.
func TestE2ELandRefusesACheckWaivedTwice(t *testing.T) {
	fixture := newLandFixture(t, "bump/deps", "go.mod", "go.sum")
	opts := landOptions(fixture)
	opts.WaiveChecks = []string{"e2e"}
	opts.WaiveAggregates = []string{"deploy-gate", "deploy-gate"}
	opts.WaiveReason = "red on public-demo"
	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != LandRefused || result.RefusalCode != LandRefusalWaivedCheckDuplicate {
		t.Fatalf("outcome = %s/%s (%s)", result.Outcome, result.RefusalCode, result.Reason)
	}
}

//nolint:paralleltest // Native Land fixture sets WB_PROJECTS_ROOT and installs the process-wide GH/PATH shim.
func TestE2ELandKeepsARequiredWaivedCheckInTheFailureReport(t *testing.T) {
	fixture := newLandFixture(t, "bump/deps", "go.mod", "go.sum")
	fixture.writeState(t, "check-runs", `{
		"total_count": 2,
		"check_runs": [
			{"name": "CI", "status": "completed", "conclusion": "failure", "app": {"id": 42}},
			{"name": "lint", "status": "completed", "conclusion": "failure", "app": {"id": 100}}
		]
	}`)
	opts := landOptions(fixture)
	opts.Inspector = settleInspector(fixture)
	opts.WaiveChecks = []string{"CI"}
	opts.WaiveReason = "attempting to waive a required check"
	opts.NoAutoMerge = true
	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.RefusalCode != LandRefusalChecksFailed || !strings.Contains(result.Reason, `CI" (required by`) {
		t.Fatalf("refusal = %s, reason = %q: a required check named in --waive-check vanished from the report", result.RefusalCode, result.Reason)
	}
}
