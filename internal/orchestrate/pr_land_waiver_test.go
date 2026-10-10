package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

type testChecksPolicyInspector struct {
	required      []githubchecks.RequiredRemoteCheck
	reqAuthority  string
	reqReason     string
	targetSHA     string
	targetReason  string
	commitChecks  []githubchecks.RemoteCheck
	commitPending bool
	commitReason  string
}

func (t *testChecksPolicyInspector) RequiredChecks(ctx context.Context, repository, target string, requireServerFreshness bool) ([]githubchecks.RequiredRemoteCheck, string, string) {
	return t.required, t.reqAuthority, t.reqReason
}

func (t *testChecksPolicyInspector) TargetHead(ctx context.Context, repository, target string) (string, string) {
	return t.targetSHA, t.targetReason
}

func (t *testChecksPolicyInspector) CommitChecks(ctx context.Context, options githubchecks.PullRequestWaitOptions) ([]githubchecks.RemoteCheck, bool, string) {
	return t.commitChecks, t.commitPending, t.commitReason
}

func TestLandWaiverAccepted(t *testing.T) {
	fixture := newLandFixture(t, "bump/deps", "go.mod", "go.sum")

	// PR head check runs: CI passes, Workers Builds fails
	fixture.writeState(t, "check-runs", `{
		"total_count": 2,
		"check_runs": [
			{"name": "CI", "status": "completed", "conclusion": "success", "app": {"id": 42}},
			{"name": "Workers Builds: specscore-md", "status": "completed", "conclusion": "failure", "app": {"id": 100}}
		]
	}`)

	inspector := &testChecksPolicyInspector{
		required: []githubchecks.RequiredRemoteCheck{
			{Name: "CI", IntegrationID: 42},
		},
		reqAuthority: "server",
		targetSHA:    fixture.baseSHA,
		commitChecks: []githubchecks.RemoteCheck{
			{Name: "CI", Conclusion: "success", Bucket: "pass", AppID: 42},
			{Name: "Workers Builds: specscore-md", Conclusion: "failure", Bucket: "fail", AppID: 100},
		},
	}

	opts := landOptions(fixture)
	opts.Inspector = inspector
	opts.WaiveChecks = []string{"Workers Builds: specscore-md"}
	opts.WaiveReason = "Cloudflare build fails on target tip and is not required by main"

	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatalf("LandPullRequest error: %v", err)
	}
	if result.Outcome != LandSuccess {
		t.Fatalf("outcome = %s, want %s (reason: %s, refusal: %s)", result.Outcome, LandSuccess, result.Reason, result.RefusalCode)
	}
	if len(result.WaivedChecks) != 1 {
		t.Fatalf("waived checks len = %d, want 1: %#v", len(result.WaivedChecks), result.WaivedChecks)
	}
	waived := result.WaivedChecks[0]
	if waived.Name != "Workers Builds: specscore-md" {
		t.Errorf("waived name = %q, want %q", waived.Name, "Workers Builds: specscore-md")
	}
	if waived.Conclusion != "failure" {
		t.Errorf("waived conclusion = %q, want failure", waived.Conclusion)
	}
	if waived.TargetSHA != fixture.baseSHA {
		t.Errorf("waived target SHA = %q, want %q", waived.TargetSHA, fixture.baseSHA)
	}
	if waived.Reason != opts.WaiveReason {
		t.Errorf("waived reason = %q, want %q", waived.Reason, opts.WaiveReason)
	}

	// Verify JSON serialization of receipt carries waived_checks
	marshaled, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if !strings.Contains(string(marshaled), `"waived_checks"`) {
		t.Errorf("marshaled json missing waived_checks: %s", string(marshaled))
	}
	if !strings.Contains(string(marshaled), `"Workers Builds: specscore-md"`) {
		t.Errorf("marshaled json missing check name: %s", string(marshaled))
	}
}

func TestLandWaiverRefusedWhenNamedCheckRequired(t *testing.T) {
	fixture := newLandFixture(t, "bump/deps", "go.mod", "go.sum")

	fixture.writeState(t, "check-runs", `{
		"total_count": 1,
		"check_runs": [
			{"name": "CI", "status": "completed", "conclusion": "failure", "app": {"id": 42}}
		]
	}`)

	inspector := &testChecksPolicyInspector{
		required: []githubchecks.RequiredRemoteCheck{
			{Name: "CI", IntegrationID: 42},
		},
		reqAuthority: "server",
		targetSHA:    fixture.baseSHA,
		commitChecks: []githubchecks.RemoteCheck{
			{Name: "CI", Conclusion: "failure", Bucket: "fail", AppID: 42},
		},
	}

	opts := landOptions(fixture)
	opts.Inspector = inspector
	opts.WaiveChecks = []string{"CI"}
	opts.WaiveReason = "attempting to waive required check"

	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Outcome != LandRefused {
		t.Fatalf("outcome = %s, want %s", result.Outcome, LandRefused)
	}
	if result.RefusalCode != LandRefusalWaivedCheckRequired {
		t.Fatalf("refusal code = %s, want %s", result.RefusalCode, LandRefusalWaivedCheckRequired)
	}
	if !strings.Contains(result.Reason, `"CI" is required by target branch main policy`) {
		t.Errorf("reason %q does not mention check required", result.Reason)
	}
	if !strings.Contains(result.Reason, fixture.baseSHA[:12]) {
		t.Errorf("reason %q does not contain target SHA", result.Reason)
	}
}

func TestLandWaiverRefusedWhenGreenOnTarget(t *testing.T) {
	fixture := newLandFixture(t, "bump/deps", "go.mod", "go.sum")

	fixture.writeState(t, "check-runs", `{
		"total_count": 2,
		"check_runs": [
			{"name": "CI", "status": "completed", "conclusion": "success", "app": {"id": 42}},
			{"name": "optional-lint", "status": "completed", "conclusion": "failure", "app": {"id": 100}}
		]
	}`)

	inspector := &testChecksPolicyInspector{
		required: []githubchecks.RequiredRemoteCheck{
			{Name: "CI", IntegrationID: 42},
		},
		reqAuthority: "server",
		targetSHA:    fixture.baseSHA,
		commitChecks: []githubchecks.RemoteCheck{
			{Name: "CI", Conclusion: "success", Bucket: "pass", AppID: 42},
			{Name: "optional-lint", Conclusion: "success", Bucket: "pass", AppID: 100},
		},
	}

	opts := landOptions(fixture)
	opts.Inspector = inspector
	opts.WaiveChecks = []string{"optional-lint"}
	opts.WaiveReason = "waiving lint"

	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Outcome != LandRefused {
		t.Fatalf("outcome = %s, want %s", result.Outcome, LandRefused)
	}
	if result.RefusalCode != LandRefusalWaivedCheckGreen {
		t.Fatalf("refusal code = %s, want %s", result.RefusalCode, LandRefusalWaivedCheckGreen)
	}
	if !strings.Contains(result.Reason, "is green on target branch main tip") {
		t.Errorf("reason %q does not mention check is green on target", result.Reason)
	}
	if !strings.Contains(result.Reason, fixture.baseSHA[:12]) {
		t.Errorf("reason %q does not contain target SHA", result.Reason)
	}
}

func TestLandWaiverRefusedWhenAbsentOnTarget(t *testing.T) {
	fixture := newLandFixture(t, "bump/deps", "go.mod", "go.sum")

	fixture.writeState(t, "check-runs", `{
		"total_count": 2,
		"check_runs": [
			{"name": "CI", "status": "completed", "conclusion": "success", "app": {"id": 42}},
			{"name": "optional-lint", "status": "completed", "conclusion": "failure", "app": {"id": 100}}
		]
	}`)

	inspector := &testChecksPolicyInspector{
		required: []githubchecks.RequiredRemoteCheck{
			{Name: "CI", IntegrationID: 42},
		},
		reqAuthority: "server",
		targetSHA:    fixture.baseSHA,
		commitChecks: []githubchecks.RemoteCheck{
			{Name: "CI", Conclusion: "success", Bucket: "pass", AppID: 42},
		},
	}

	opts := landOptions(fixture)
	opts.Inspector = inspector
	opts.WaiveChecks = []string{"optional-lint"}
	opts.WaiveReason = "waiving lint"

	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Outcome != LandRefused {
		t.Fatalf("outcome = %s, want %s", result.Outcome, LandRefused)
	}
	if result.RefusalCode != LandRefusalWaivedCheckAbsent {
		t.Fatalf("refusal code = %s, want %s", result.RefusalCode, LandRefusalWaivedCheckAbsent)
	}
	if !strings.Contains(result.Reason, "was not observed on target branch main tip") {
		t.Errorf("reason %q does not mention check is absent on target", result.Reason)
	}
	if !strings.Contains(result.Reason, fixture.baseSHA[:12]) {
		t.Errorf("reason %q does not contain target SHA", result.Reason)
	}
}

func TestLandWaiverRefusedWhenConclusionMismatch(t *testing.T) {
	fixture := newLandFixture(t, "bump/deps", "go.mod", "go.sum")

	fixture.writeState(t, "check-runs", `{
		"total_count": 2,
		"check_runs": [
			{"name": "CI", "status": "completed", "conclusion": "success", "app": {"id": 42}},
			{"name": "optional-lint", "status": "completed", "conclusion": "cancelled", "app": {"id": 100}}
		]
	}`)

	inspector := &testChecksPolicyInspector{
		required: []githubchecks.RequiredRemoteCheck{
			{Name: "CI", IntegrationID: 42},
		},
		reqAuthority: "server",
		targetSHA:    fixture.baseSHA,
		commitChecks: []githubchecks.RemoteCheck{
			{Name: "CI", Conclusion: "success", Bucket: "pass", AppID: 42},
			{Name: "optional-lint", Conclusion: "timed_out", Bucket: "fail", AppID: 100},
		},
	}

	opts := landOptions(fixture)
	opts.Inspector = inspector
	opts.WaiveChecks = []string{"optional-lint"}
	opts.WaiveReason = "waiving lint"

	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Outcome != LandRefused {
		t.Fatalf("outcome = %s, want %s", result.Outcome, LandRefused)
	}
	if result.RefusalCode != LandRefusalWaivedCheckMismatch {
		t.Fatalf("refusal code = %s, want %s", result.RefusalCode, LandRefusalWaivedCheckMismatch)
	}
	if !strings.Contains(result.Reason, "does not match conclusion") {
		t.Errorf("reason %q does not mention conclusion mismatch", result.Reason)
	}
	if !strings.Contains(result.Reason, fixture.baseSHA[:12]) {
		t.Errorf("reason %q does not contain target SHA", result.Reason)
	}
}

func TestLandWaiverRefusedWhenNotFailedOnHead(t *testing.T) {
	fixture := newLandFixture(t, "bump/deps", "go.mod", "go.sum")

	fixture.writeState(t, "check-runs", `{
		"total_count": 2,
		"check_runs": [
			{"name": "CI", "status": "completed", "conclusion": "success", "app": {"id": 42}},
			{"name": "optional-lint", "status": "completed", "conclusion": "success", "app": {"id": 100}}
		]
	}`)

	inspector := &testChecksPolicyInspector{
		required: []githubchecks.RequiredRemoteCheck{
			{Name: "CI", IntegrationID: 42},
		},
		reqAuthority: "server",
		targetSHA:    fixture.baseSHA,
		commitChecks: []githubchecks.RemoteCheck{
			{Name: "CI", Conclusion: "success", Bucket: "pass", AppID: 42},
			{Name: "optional-lint", Conclusion: "failure", Bucket: "fail", AppID: 100},
		},
	}

	opts := landOptions(fixture)
	opts.Inspector = inspector
	opts.WaiveChecks = []string{"optional-lint"}
	opts.WaiveReason = "waiving lint"

	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Outcome != LandRefused {
		t.Fatalf("outcome = %s, want %s", result.Outcome, LandRefused)
	}
	if result.RefusalCode != LandRefusalWaivedCheckNotFailed {
		t.Fatalf("refusal code = %s, want %s", result.RefusalCode, LandRefusalWaivedCheckNotFailed)
	}
	if !strings.Contains(result.Reason, "did not fail or cancel on pull request head") {
		t.Errorf("reason %q does not mention check did not fail", result.Reason)
	}
}

func TestLandWaiverRefusedWhenTargetUnfenced(t *testing.T) {
	fixture := newLandFixture(t, "bump/deps", "go.mod", "go.sum")

	fixture.writeState(t, "check-runs", `{
		"total_count": 1,
		"check_runs": [
			{"name": "optional-lint", "status": "completed", "conclusion": "failure", "app": {"id": 100}}
		]
	}`)

	inspector := &testChecksPolicyInspector{
		required:     nil, // No required status checks on target!
		reqAuthority: "server",
		targetSHA:    fixture.baseSHA,
		commitChecks: []githubchecks.RemoteCheck{
			{Name: "optional-lint", Conclusion: "failure", Bucket: "fail", AppID: 100},
		},
	}

	opts := landOptions(fixture)
	opts.Inspector = inspector
	opts.WaiveChecks = []string{"optional-lint"}
	opts.WaiveReason = "waiving lint"

	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Outcome != LandRefused {
		t.Fatalf("outcome = %s, want %s", result.Outcome, LandRefused)
	}
	if result.RefusalCode != LandRefusalWaivedCheckTargetUnfenced {
		t.Fatalf("refusal code = %s, want %s", result.RefusalCode, LandRefusalWaivedCheckTargetUnfenced)
	}
	if !strings.Contains(result.Reason, "has no server-enforced required status checks") {
		t.Errorf("reason %q does not mention no required checks", result.Reason)
	}
	if !strings.Contains(result.SanctionedCommand, "--allow-unfenced") {
		t.Errorf("sanctioned command %q does not contain --allow-unfenced", result.SanctionedCommand)
	}
}

func TestLandWaiverRefusedWhenSecondFailedCheckUnnamed(t *testing.T) {
	fixture := newLandFixture(t, "bump/deps", "go.mod", "go.sum")

	fixture.writeState(t, "check-runs", `{
		"total_count": 3,
		"check_runs": [
			{"name": "CI", "status": "completed", "conclusion": "success", "app": {"id": 42}},
			{"name": "Workers Builds: specscore-md", "status": "completed", "conclusion": "failure", "app": {"id": 100}},
			{"name": "codecov", "status": "completed", "conclusion": "failure", "app": {"id": 101}}
		]
	}`)

	inspector := &testChecksPolicyInspector{
		required: []githubchecks.RequiredRemoteCheck{
			{Name: "CI", IntegrationID: 42},
		},
		reqAuthority: "server",
		targetSHA:    fixture.baseSHA,
		commitChecks: []githubchecks.RemoteCheck{
			{Name: "CI", Conclusion: "success", Bucket: "pass", AppID: 42},
			{Name: "Workers Builds: specscore-md", Conclusion: "failure", Bucket: "fail", AppID: 100},
			{Name: "codecov", Conclusion: "failure", Bucket: "fail", AppID: 101},
		},
	}

	opts := landOptions(fixture)
	opts.Inspector = inspector
	// Only waive one of the two failing checks
	opts.WaiveChecks = []string{"Workers Builds: specscore-md"}
	opts.WaiveReason = "waiving only Workers Builds"

	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Outcome != LandFindings {
		t.Fatalf("outcome = %s, want %s (reason: %s)", result.Outcome, LandFindings, result.Reason)
	}
	if result.RefusalCode != LandRefusalChecksFailed {
		t.Fatalf("refusal code = %s, want %s", result.RefusalCode, LandRefusalChecksFailed)
	}
	// The reason should detail the remaining failed check
	if !strings.Contains(result.Reason, "codecov") {
		t.Errorf("reason %q does not mention codecov", result.Reason)
	}
}

func TestLandWaiverRefusedWhenReasonEmptyOrMismatched(t *testing.T) {
	fixture := newLandFixture(t, "bump/deps", "go.mod", "go.sum")

	// 1. WaiveChecks provided but empty reason
	opts := landOptions(fixture)
	opts.WaiveChecks = []string{"lint"}
	opts.WaiveReason = ""

	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Outcome != LandRefused || result.RefusalCode != LandRefusalWaiveReasonEmpty {
		t.Fatalf("outcome = %s, refusal = %s, want %s", result.Outcome, result.RefusalCode, LandRefusalWaiveReasonEmpty)
	}

	// 2. WaiveReason provided but no WaiveChecks
	opts2 := landOptions(fixture)
	opts2.WaiveChecks = nil
	opts2.WaiveReason = "some reason"

	result2, err := LandPullRequest(context.Background(), opts2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result2.Outcome != LandRefused || result2.RefusalCode != LandRefusalWaiveReasonEmpty {
		t.Fatalf("outcome = %s, refusal = %s, want %s", result2.Outcome, result2.RefusalCode, LandRefusalWaiveReasonEmpty)
	}
}

func TestLandResumeCommandCarriesWaiver(t *testing.T) {
	opts := PullRequestLandOptions{
		Repository:  "sneat-dev/wb",
		PullRequest: "844",
		WaiveChecks: []string{"Workers Builds: specscore-md", "optional-check"},
		WaiveReason: "known broken on main",
		MergeMethod: "merge",
		Slice:       50 * time.Minute,
	}

	cmd := pullRequestLandResumeCommand(opts, "844", "")
	if !strings.Contains(cmd, `--waive-check 'Workers Builds: specscore-md'`) {
		t.Errorf("resume command %q missing first waive-check", cmd)
	}
	if !strings.Contains(cmd, `--waive-check 'optional-check'`) {
		t.Errorf("resume command %q missing second waive-check", cmd)
	}
	if !strings.Contains(cmd, `--waive-reason 'known broken on main'`) {
		t.Errorf("resume command %q missing waive-reason", cmd)
	}
}

func TestLandChecksFailedOffersSanctionedWaiveCommand(t *testing.T) {
	fixture := newLandFixture(t, "bump/deps", "go.mod", "go.sum")

	// PR head check runs: CI passes, Workers Builds fails
	fixture.writeState(t, "check-runs", `{
		"total_count": 2,
		"check_runs": [
			{"name": "CI", "status": "completed", "conclusion": "success", "app": {"id": 42}},
			{"name": "Workers Builds: specscore-md", "status": "completed", "conclusion": "failure", "app": {"id": 100}}
		]
	}`)

	inspector := &testChecksPolicyInspector{
		required: []githubchecks.RequiredRemoteCheck{
			{Name: "CI", IntegrationID: 42},
		},
		reqAuthority: "server",
		targetSHA:    fixture.baseSHA,
		commitChecks: []githubchecks.RemoteCheck{
			{Name: "CI", Conclusion: "success", Bucket: "pass", AppID: 42},
			{Name: "Workers Builds: specscore-md", Conclusion: "failure", Bucket: "fail", AppID: 100},
		},
	}

	opts := landOptions(fixture)
	opts.Inspector = inspector
	// No waiver passed yet!
	opts.WaiveChecks = nil
	opts.WaiveReason = ""

	result, err := LandPullRequest(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Outcome != LandFindings {
		t.Fatalf("outcome = %s, want %s (reason: %s)", result.Outcome, LandFindings, result.Reason)
	}

	// Should explain that Workers Builds is not required and fails on target tip
	if !strings.Contains(result.Reason, "not required by main") {
		t.Errorf("reason %q does not state check is not required", result.Reason)
	}
	if !strings.Contains(result.Reason, fmt.Sprintf("fails on main tip %s with conclusion \"failure\"", fixture.baseSHA[:12])) {
		t.Errorf("reason %q does not explain failure on target tip with SHA", result.Reason)
	}

	// Sanctioned command should recommend --waive-check
	wantPrefix := `wb pr land acme/app#7`
	if !strings.HasPrefix(result.SanctionedCommand, wantPrefix) {
		t.Errorf("sanctioned command = %q, want prefix %q", result.SanctionedCommand, wantPrefix)
	}
	if !strings.Contains(result.SanctionedCommand, `--waive-check 'Workers Builds: specscore-md'`) {
		t.Errorf("sanctioned command %q missing --waive-check", result.SanctionedCommand)
	}
	if !strings.Contains(result.SanctionedCommand, "--waive-reason") {
		t.Errorf("sanctioned command %q missing --waive-reason placeholder", result.SanctionedCommand)
	}
}
