package orchestrate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

// waiverInspector serves required policy, a target tip, and per-SHA checks.
type waiverInspector struct {
	required  []githubchecks.RequiredRemoteCheck
	reqReason string
	tipSHA    string
	tipReason string
	checks    map[string][]githubchecks.RemoteCheck
	reasons   map[string]string
	reads     map[string]int
}

func (inspector *waiverInspector) RequiredChecks(context.Context, string, string, bool) ([]githubchecks.RequiredRemoteCheck, string, string) {
	return inspector.required, "server", inspector.reqReason
}

func (inspector *waiverInspector) TargetHead(context.Context, string, string) (string, string) {
	return inspector.tipSHA, inspector.tipReason
}

func (inspector *waiverInspector) CommitChecks(_ context.Context, options githubchecks.PullRequestWaitOptions) ([]githubchecks.RemoteCheck, bool, string) {
	if inspector.reads != nil {
		inspector.reads[options.Head]++
	}
	return inspector.checks[options.Head], false, inspector.reasons[options.Head]
}

const (
	waiverTip    = "1111111111111111111111111111111111111111"
	waiverParent = "2222222222222222222222222222222222222222"
	waiverGrand  = "3333333333333333333333333333333333333333"
	waiverHead   = "9999999999999999999999999999999999999999"
)

func waiverFailed(name string) githubchecks.RemoteCheck {
	return githubchecks.RemoteCheck{Name: name, Bucket: "fail", Conclusion: "failure"}
}

func waiverPassed(name string) githubchecks.RemoteCheck {
	return githubchecks.RemoteCheck{Name: name, Bucket: "pass", Conclusion: "success"}
}

func waiverRunning(name string) githubchecks.RemoteCheck {
	return githubchecks.RemoteCheck{Name: name, Bucket: "pending"}
}

// linearParents is a first-parent chain tip -> parent -> grand -> root.
func linearParents(chain ...string) func(context.Context, string, string) ([]string, error) {
	return func(_ context.Context, _ string, sha string) ([]string, error) {
		for i := 0; i+1 < len(chain); i++ {
			if chain[i] == sha {
				return []string{chain[i+1], "ffffffffffffffffffffffffffffffffffffffff"}, nil
			}
		}
		return nil, nil
	}
}

func waiverOptions(inspector *waiverInspector) PullRequestLandOptions {
	return PullRequestLandOptions{
		Repository: "acme/app", PullRequest: "7",
		WaiveChecks: []string{"e2e"}, WaiveReason: "known red",
		Inspector:     inspector,
		commitParents: linearParents(waiverTip, waiverParent, waiverGrand),
	}
}

func fencedInspector(checks map[string][]githubchecks.RemoteCheck) *waiverInspector {
	return &waiverInspector{
		required: []githubchecks.RequiredRemoteCheck{{Name: "preflight"}},
		tipSHA:   waiverTip,
		checks:   checks,
	}
}

func verifyWaivers(t *testing.T, options PullRequestLandOptions, head []githubchecks.RemoteCheck) ([]WaivedCheck, *landRefusal) {
	t.Helper()
	waived, refusal, err := verifyWaivedChecks(context.Background(), options, "7", "public-demo", waiverHead, head)
	if err != nil {
		t.Fatalf("verifyWaivedChecks error: %v", err)
	}
	return waived, refusal
}

func TestWaiverStageJudgesOnlyATerminalWait(t *testing.T) {
	t.Parallel()
	options := PullRequestLandOptions{WaiveChecks: []string{"deploy-gate"}}
	cases := []struct {
		name   string
		waited githubchecks.PullRequestWaitResult
		merged bool
		want   waiverStage
	}{
		{"merged by GitHub", githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitPending}, true, waiverStageMoot},
		{"passed", githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitPassed}, false, waiverStageJudge},
		{"only waived running", githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitPending, Checks: []githubchecks.RemoteCheck{waiverPassed("build"), waiverRunning("deploy-gate")}}, false, waiverStagePending},
		{"non-waived running", githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitPending, Checks: []githubchecks.RemoteCheck{waiverRunning("build"), waiverRunning("deploy-gate")}}, false, waiverStageSkip},
		{"required not registered", githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitPending, Checks: []githubchecks.RemoteCheck{waiverRunning("deploy-gate")}, RequiredChecks: []githubchecks.RequiredRemoteCheck{{Name: "build"}}}, false, waiverStageSkip},
		{"pending with nothing running", githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitPending}, false, waiverStageSkip},
		{"real failure while waived runs", githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitFailed, Checks: []githubchecks.RemoteCheck{waiverFailed("test"), waiverRunning("deploy-gate")}}, false, waiverStageSkip},
	}
	for _, tc := range cases {
		if got := waiverStageFor(options, tc.waited, tc.merged); got != tc.want {
			t.Errorf("%s: stage = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestPendingWaiverResultNamesRunningChecksAndResumes(t *testing.T) {
	t.Parallel()
	options := PullRequestLandOptions{Repository: "acme/app", WaiveAggregates: []string{"deploy-gate"}, WaiveReason: "r"}
	waited := githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitPending, Checks: []githubchecks.RemoteCheck{waiverRunning("check-run:deploy-gate")}}
	result := pendingWaiverResult(PullRequestLandResult{}, options, "7", waited)
	if result.Outcome != LandFindings || result.RefusalCode != LandRefusalWaivedCheckPending {
		t.Fatalf("outcome = %s/%s", result.Outcome, result.RefusalCode)
	}
	if !strings.Contains(result.Reason, "deploy-gate") || !strings.Contains(result.SanctionedCommand, "--waive-aggregate 'deploy-gate'") {
		t.Fatalf("reason %q / command %q", result.Reason, result.SanctionedCommand)
	}
	if len(result.WaivedChecks) != 0 {
		t.Fatalf("a pending waiver recorded a verdict: %#v", result.WaivedChecks)
	}
}

func TestWaitWaivedChecksIncludesAggregates(t *testing.T) {
	t.Parallel()
	only := PullRequestLandOptions{WaiveChecks: []string{"a"}}
	if got := only.waitWaivedChecks(); strings.Join(got, ",") != "a" || !only.hasWaivers() {
		t.Fatalf("waitWaivedChecks = %v", got)
	}
	both := PullRequestLandOptions{WaiveChecks: []string{"a"}, WaiveAggregates: []string{"g"}}
	if got := both.waitWaivedChecks(); strings.Join(got, ",") != "a,g" {
		t.Fatalf("waitWaivedChecks = %v", got)
	}
	if (PullRequestLandOptions{}).hasWaivers() {
		t.Fatal("no flags reported as waivers")
	}
	if (PullRequestLandOptions{}).resolveCommitParents() == nil || (PullRequestLandOptions{}).resolveAggregateNeeds() == nil {
		t.Fatal("default seams are nil")
	}
}

func TestWaiverOnTipFailureRecordsTheTipAsBaseline(t *testing.T) {
	t.Parallel()
	options := waiverOptions(fencedInspector(map[string][]githubchecks.RemoteCheck{waiverTip: {waiverFailed("e2e")}}))
	waived, refusal := verifyWaivers(t, options, []githubchecks.RemoteCheck{waiverFailed("e2e")})
	if refusal != nil {
		t.Fatalf("refusal: %+v", refusal)
	}
	if waived[0].BaselineSHA != waiverTip || !waived[0].BaselineIsTip || waived[0].Authority != waiverAuthorityServerPolicy {
		t.Fatalf("waived = %+v", waived[0])
	}
}

func TestGreenTipStillRefusesWithABaseline(t *testing.T) {
	t.Parallel()
	options := waiverOptions(fencedInspector(map[string][]githubchecks.RemoteCheck{
		waiverTip: {waiverPassed("e2e")}, waiverParent: {waiverFailed("e2e")},
	}))
	options.WaiveBaseline = waiverParent
	_, refusal := verifyWaivers(t, options, []githubchecks.RemoteCheck{waiverFailed("e2e")})
	if refusal == nil || refusal.code != LandRefusalWaivedCheckGreen {
		t.Fatalf("refusal = %+v, want green-on-target", refusal)
	}
}

func TestTipConclusionMismatchIsRefused(t *testing.T) {
	t.Parallel()
	cancelled := githubchecks.RemoteCheck{Name: "e2e", Bucket: "cancel"}
	options := waiverOptions(fencedInspector(map[string][]githubchecks.RemoteCheck{waiverTip: {cancelled}}))
	_, refusal := verifyWaivers(t, options, []githubchecks.RemoteCheck{waiverFailed("e2e")})
	if refusal == nil || refusal.code != LandRefusalWaivedCheckMismatch {
		t.Fatalf("refusal = %+v, want conclusion mismatch", refusal)
	}
}

func TestAbsentTipRunSuggestsAndAcceptsTheParentBaseline(t *testing.T) {
	t.Parallel()
	inspector := fencedInspector(map[string][]githubchecks.RemoteCheck{waiverParent: {waiverFailed("e2e")}})
	options := waiverOptions(inspector)
	_, refusal := verifyWaivers(t, options, []githubchecks.RemoteCheck{waiverFailed("e2e")})
	if refusal == nil || refusal.code != LandRefusalWaivedCheckAbsent || !strings.Contains(refusal.command, "--waive-baseline "+waiverParent) {
		t.Fatalf("refusal = %+v, want absent with a baseline command", refusal)
	}
	options.WaiveBaseline = waiverParent[:12]
	waived, refusal := verifyWaivers(t, options, []githubchecks.RemoteCheck{waiverFailed("e2e")})
	if refusal != nil {
		t.Fatalf("refusal: %+v", refusal)
	}
	if waived[0].BaselineSHA != waiverParent || waived[0].BaselineIsTip || waived[0].TargetSHA != waiverTip {
		t.Fatalf("waived = %+v", waived[0])
	}
}

func TestHungTipRunWithoutABaselineSaysNoneFound(t *testing.T) {
	t.Parallel()
	options := waiverOptions(fencedInspector(map[string][]githubchecks.RemoteCheck{waiverTip: {waiverRunning("e2e")}}))
	_, refusal := verifyWaivers(t, options, []githubchecks.RemoteCheck{waiverFailed("e2e")})
	if refusal == nil || refusal.code != LandRefusalWaivedCheckHung || !strings.Contains(refusal.reason, "no baseline within 20 first-parent commits") {
		t.Fatalf("refusal = %+v, want hung with no baseline", refusal)
	}
}

func TestBaselineSearchStopsAtAGreenRunAndHonoursTheBound(t *testing.T) {
	t.Parallel()
	options := waiverOptions(fencedInspector(map[string][]githubchecks.RemoteCheck{
		waiverParent: {waiverPassed("e2e")}, waiverGrand: {waiverFailed("e2e")},
	}))
	_, refusal := verifyWaivers(t, options, []githubchecks.RemoteCheck{waiverFailed("e2e")})
	if refusal == nil || !strings.Contains(refusal.reason, "no baseline") {
		t.Fatalf("refusal = %+v, want the search to stop at the green parent", refusal)
	}

	chain := []string{waiverTip}
	for i := 0; i < maxWaiverBaselineDepth+5; i++ {
		chain = append(chain, strings.Repeat(string(rune('a'+i%6)), 39)+string(rune('0'+i%10)))
	}
	deep := waiverOptions(fencedInspector(map[string][]githubchecks.RemoteCheck{chain[maxWaiverBaselineDepth+1]: {waiverFailed("e2e")}}))
	deep.commitParents = linearParents(chain...)
	_, refusal = verifyWaivers(t, deep, []githubchecks.RemoteCheck{waiverFailed("e2e")})
	if refusal == nil || !strings.Contains(refusal.reason, "no baseline") {
		t.Fatalf("refusal = %+v, want a failure 21 hops back to be out of bounds", refusal)
	}
	deep.WaiveBaseline = chain[maxWaiverBaselineDepth+1]
	_, refusal = verifyWaivers(t, deep, []githubchecks.RemoteCheck{waiverFailed("e2e")})
	if refusal == nil || refusal.code != LandRefusalWaivedCheckBaselineInvalid {
		t.Fatalf("refusal = %+v, want an out-of-bound baseline refused", refusal)
	}
}

func TestBaselineOffPathPastGreenOrWithoutMatchingRunIsRefused(t *testing.T) {
	t.Parallel()
	head := []githubchecks.RemoteCheck{waiverFailed("e2e")}
	offPath := waiverOptions(fencedInspector(map[string][]githubchecks.RemoteCheck{}))
	offPath.WaiveBaseline = "ffffffffffffffffffffffffffffffffffffffff"
	if _, refusal := verifyWaivers(t, offPath, head); refusal == nil || refusal.code != LandRefusalWaivedCheckBaselineInvalid || !strings.Contains(refusal.reason, "not a first-parent ancestor") {
		t.Fatalf("second-parent baseline: refusal = %+v", refusal)
	}
	pastGreen := waiverOptions(fencedInspector(map[string][]githubchecks.RemoteCheck{
		waiverParent: {waiverPassed("e2e")}, waiverGrand: {waiverFailed("e2e")},
	}))
	pastGreen.WaiveBaseline = waiverGrand
	if _, refusal := verifyWaivers(t, pastGreen, head); refusal == nil || !strings.Contains(refusal.reason, "is green on") {
		t.Fatalf("baseline past green: refusal = %+v", refusal)
	}
	noRun := waiverOptions(fencedInspector(map[string][]githubchecks.RemoteCheck{}))
	noRun.WaiveBaseline = waiverParent
	if _, refusal := verifyWaivers(t, noRun, head); refusal == nil || !strings.Contains(refusal.reason, "no terminal") {
		t.Fatalf("baseline without a run: refusal = %+v", refusal)
	}
	short := waiverOptions(fencedInspector(map[string][]githubchecks.RemoteCheck{waiverParent: {waiverFailed("e2e")}}))
	short.WaiveBaseline = "222"
	if _, refusal := verifyWaivers(t, short, head); refusal == nil || refusal.code != LandRefusalWaivedCheckBaselineInvalid {
		t.Fatalf("too-short baseline: refusal = %+v", refusal)
	}
}

func TestBaselineReadsAreCachedAcrossWaivedChecks(t *testing.T) {
	t.Parallel()
	inspector := fencedInspector(map[string][]githubchecks.RemoteCheck{waiverParent: {waiverFailed("e2e"), waiverFailed("lint")}})
	inspector.reads = map[string]int{}
	options := waiverOptions(inspector)
	options.WaiveChecks = []string{"e2e", "lint"}
	options.WaiveBaseline = waiverParent
	waived, refusal := verifyWaivers(t, options, []githubchecks.RemoteCheck{waiverFailed("e2e"), waiverFailed("lint")})
	if refusal != nil || len(waived) != 2 {
		t.Fatalf("waived = %+v, refusal = %+v", waived, refusal)
	}
	if inspector.reads[waiverParent] != 1 {
		t.Fatalf("parent checks read %d times, want 1", inspector.reads[waiverParent])
	}
}

func TestBaselineReadErrorsAreErrorsNotVerdicts(t *testing.T) {
	t.Parallel()
	head := []githubchecks.RemoteCheck{waiverFailed("e2e")}
	parentsFail := waiverOptions(fencedInspector(map[string][]githubchecks.RemoteCheck{}))
	parentsFail.commitParents = func(context.Context, string, string) ([]string, error) { return nil, errors.New("api down") }
	for _, baseline := range []string{"", waiverParent} {
		parentsFail.WaiveBaseline = baseline
		if _, _, err := verifyWaivedChecks(context.Background(), parentsFail, "7", "public-demo", waiverHead, head); err == nil {
			t.Fatalf("baseline %q: parent read failure was not an error", baseline)
		}
	}
	checksFail := fencedInspector(map[string][]githubchecks.RemoteCheck{})
	checksFail.reasons = map[string]string{waiverParent: "rate limited"}
	options := waiverOptions(checksFail)
	for _, baseline := range []string{"", waiverParent} {
		options.WaiveBaseline = baseline
		if _, _, err := verifyWaivedChecks(context.Background(), options, "7", "public-demo", waiverHead, head); err == nil || !strings.Contains(err.Error(), "rate limited") {
			t.Fatalf("baseline %q: err = %v", baseline, err)
		}
	}
}

func TestUnfencedTargetWaivesOnlyWithAllowUnfenced(t *testing.T) {
	t.Parallel()
	inspector := &waiverInspector{tipSHA: waiverTip, checks: map[string][]githubchecks.RemoteCheck{waiverTip: {waiverFailed("e2e")}}}
	options := waiverOptions(inspector)
	if _, refusal := verifyWaivers(t, options, []githubchecks.RemoteCheck{waiverFailed("e2e")}); refusal == nil || refusal.code != LandRefusalWaivedCheckTargetUnfenced {
		t.Fatalf("refusal = %+v, want unfenced", refusal)
	}
	options.AllowUnfenced = true
	waived, refusal := verifyWaivers(t, options, []githubchecks.RemoteCheck{waiverFailed("e2e")})
	if refusal != nil || waived[0].Authority != waiverAuthorityAllowUnfenced {
		t.Fatalf("waived = %+v, refusal = %+v", waived, refusal)
	}
	inspector.reqReason = "403"
	if _, refusal := verifyWaivers(t, options, []githubchecks.RemoteCheck{waiverFailed("e2e")}); refusal == nil || refusal.code != LandRefusalWaivedCheckTargetUnfenced {
		t.Fatalf("unreadable policy: refusal = %+v", refusal)
	}
}

func TestAutoMergeIsNotArmedForWaiversOnAnUnfencedTarget(t *testing.T) {
	t.Parallel()
	unfenced := PullRequestLandOptions{AllowUnfenced: true, WaiveChecks: []string{"e2e"}, Inspector: &waiverInspector{}}
	if got := autoMergeBypassesAGuard(context.Background(), unfenced, "public-demo"); !strings.Contains(got, "judged by WB") {
		t.Fatalf("bypass = %q, want arming refused", got)
	}
	fenced := unfenced
	fenced.Inspector = &waiverInspector{required: []githubchecks.RequiredRemoteCheck{{Name: "build"}}}
	if got := autoMergeBypassesAGuard(context.Background(), fenced, "public-demo"); got != "" {
		t.Fatalf("bypass = %q, want arming allowed on a fenced target", got)
	}
}

func aggregateOptions(inspector *waiverInspector, inputs []string, needsErr error) PullRequestLandOptions {
	options := waiverOptions(inspector)
	options.WaiveAggregates = []string{"deploy-gate"}
	options.aggregateNeeds = func(context.Context, string, string, githubchecks.RemoteCheck) (string, []string, error) {
		return ".github/workflows/ci.yml", inputs, needsErr
	}
	return options
}

func TestAggregatorIsWaivedFromItsNeeds(t *testing.T) {
	t.Parallel()
	inspector := fencedInspector(map[string][]githubchecks.RemoteCheck{waiverTip: {waiverFailed("e2e")}})
	head := []githubchecks.RemoteCheck{waiverPassed("build (linux)"), waiverFailed("e2e"), waiverFailed("deploy-gate")}
	waived, refusal := verifyWaivers(t, aggregateOptions(inspector, []string{"build", "e2e"}, nil), head)
	if refusal != nil {
		t.Fatalf("refusal: %+v", refusal)
	}
	got := waived[1]
	if got.Name != "deploy-gate" || got.WaivedBy != waivedByAggregation || strings.Join(got.Inputs, ",") != "build,e2e" || got.Workflow == "" {
		t.Fatalf("aggregate = %+v", got)
	}
}

func TestAggregatorRefusals(t *testing.T) {
	t.Parallel()
	tip := map[string][]githubchecks.RemoteCheck{waiverTip: {waiverFailed("e2e")}}
	base := []githubchecks.RemoteCheck{waiverPassed("build"), waiverFailed("e2e"), waiverFailed("deploy-gate")}
	cases := []struct {
		name     string
		head     []githubchecks.RemoteCheck
		required []githubchecks.RequiredRemoteCheck
		inputs   []string
		needsErr error
		code     string
		reason   string
	}{
		{"aggregate passed", []githubchecks.RemoteCheck{waiverPassed("build"), waiverFailed("e2e"), waiverPassed("deploy-gate")}, nil, []string{"e2e"}, nil, LandRefusalWaivedCheckNotFailed, "did not fail"},
		{"aggregate absent", []githubchecks.RemoteCheck{waiverPassed("build"), waiverFailed("e2e")}, nil, []string{"e2e"}, nil, LandRefusalWaivedCheckNotFailed, "absent"},
		{"aggregate required", base, []githubchecks.RequiredRemoteCheck{{Name: "deploy-gate"}}, []string{"e2e"}, nil, LandRefusalWaivedCheckRequired, "aggregate"},
		{"input required", base, []githubchecks.RequiredRemoteCheck{{Name: "build"}}, []string{"build", "e2e"}, nil, LandRefusalWaivedCheckRequired, "input"},
		{"needs unreadable", base, nil, nil, errors.New("not a GitHub Actions job"), LandRefusalWaivedAggregateUnproven, "not a GitHub Actions job"},
		{"input missing", base, nil, []string{"e2e", "smoke"}, nil, LandRefusalWaivedAggregateUnproven, "no check run"},
		{"input failed unwaived", []githubchecks.RemoteCheck{waiverFailed("build"), waiverFailed("e2e"), waiverFailed("deploy-gate")}, nil, []string{"build", "e2e"}, nil, LandRefusalWaivedAggregateUnproven, "is not waived"},
		{"input running", []githubchecks.RemoteCheck{waiverRunning("build"), waiverFailed("e2e"), waiverFailed("deploy-gate")}, nil, []string{"build", "e2e"}, nil, LandRefusalWaivedAggregateUnproven, "not concluded"},
		{"no input waived", base, nil, []string{"build"}, nil, LandRefusalWaivedAggregateUnproven, "none of its inputs"},
	}
	for _, tc := range cases {
		inspector := fencedInspector(tip)
		if tc.required != nil {
			inspector.required = append(inspector.required, tc.required...)
		}
		_, refusal := verifyWaivers(t, aggregateOptions(inspector, tc.inputs, tc.needsErr), tc.head)
		if refusal == nil || refusal.code != tc.code || !strings.Contains(refusal.reason, tc.reason) {
			t.Errorf("%s: refusal = %+v, want %s containing %q", tc.name, refusal, tc.code, tc.reason)
		}
	}
}

func TestWaivedCheckNotFailedOnHeadReportsItsStatus(t *testing.T) {
	t.Parallel()
	options := waiverOptions(fencedInspector(map[string][]githubchecks.RemoteCheck{}))
	if _, refusal := verifyWaivers(t, options, []githubchecks.RemoteCheck{waiverPassed("e2e")}); refusal == nil || !strings.Contains(refusal.reason, "status: success") {
		t.Fatalf("refusal = %+v", refusal)
	}
}

func encodedWorkflow(body string) []byte {
	raw, _ := json.Marshal(map[string]string{"content": base64.StdEncoding.EncodeToString([]byte(body))})
	return raw
}

func TestGitHubAggregateNeedsReadsTheWorkflowAtTheRunHead(t *testing.T) {
	t.Parallel()
	check := githubchecks.RemoteCheck{Name: "deploy-gate", WorkflowRunID: 42}
	workflow := "jobs:\n  build:\n    name: Build\n  e2e: {}\n  deploy-gate:\n    needs: [build, e2e]\n"
	var endpoints []string
	get := func(_ context.Context, _, _, _, _ string, endpoint string) ([]byte, error) {
		endpoints = append(endpoints, endpoint)
		if strings.Contains(endpoint, "/actions/runs/42") {
			return []byte(`{"path":".github/workflows/ci.yml","head_sha":"abcabcabcabc"}`), nil
		}
		return encodedWorkflow(workflow), nil
	}
	path, inputs, err := githubAggregateNeedsWith(context.Background(), get, "acme/app", waiverHead, check)
	if err != nil || path != ".github/workflows/ci.yml" || strings.Join(inputs, ",") != "Build,e2e" {
		t.Fatalf("path %q inputs %v err %v", path, inputs, err)
	}
	if !strings.HasSuffix(endpoints[1], "?ref=abcabcabcabc") {
		t.Fatalf("workflow read at %q, want the run head", endpoints[1])
	}
}

func TestGitHubAggregateNeedsFailures(t *testing.T) {
	t.Parallel()
	if _, _, err := githubAggregateNeeds(context.Background(), "acme/app", waiverHead, githubchecks.RemoteCheck{Name: "status-check"}); err == nil {
		t.Fatal("a non-Actions check was accepted")
	}
	check := githubchecks.RemoteCheck{Name: "deploy-gate", WorkflowRunID: 42}
	run := []byte(`{"path":"ci.yml","head_sha":"abc"}`)
	cases := []struct {
		name    string
		run     []byte
		runErr  error
		content []byte
		fileErr error
		want    string
	}{
		{"run read fails", nil, errors.New("boom"), nil, nil, "read workflow run"},
		{"run names no file", []byte(`{}`), nil, nil, nil, "names no workflow file"},
		{"file read fails", run, nil, nil, errors.New("404"), "read ci.yml"},
		{"file json bad", run, nil, []byte(`not json`), nil, "decode ci.yml"},
		{"file base64 bad", run, nil, []byte(`{"content":"!!!"}`), nil, "decode ci.yml"},
		{"workflow has no such job", run, nil, encodedWorkflow("jobs: {}\n"), nil, "ci.yml: 0 jobs"},
	}
	for _, tc := range cases {
		get := func(_ context.Context, _, _, _, _ string, endpoint string) ([]byte, error) {
			if strings.Contains(endpoint, "/actions/runs/") {
				return tc.run, tc.runErr
			}
			return tc.content, tc.fileErr
		}
		if _, _, err := githubAggregateNeedsWith(context.Background(), get, "acme/app", waiverHead, check); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestParseWorkflowNeedsMapsJobIDsToCheckNames(t *testing.T) {
	t.Parallel()
	workflow := []byte(`jobs:
  build:
    name: Build
  test:
    name: "Test ${{ matrix.os }}"
  gate:
    name: deploy-gate
    needs: [build, test]
  single:
    needs: build
  call:
    uses: ./.github/workflows/reusable.yml
  caller:
    needs: call
  lonely: {}
  ghost:
    needs: nowhere
  weird:
    needs: {a: b}
`)
	cases := []struct {
		check string
		want  string
		err   string
	}{
		{"deploy-gate", "Build,test", ""},
		{"deploy-gate (linux)", "Build,test", ""},
		{"single", "Build", ""},
		{"call", "", "reusable-workflow call"},
		{"caller", "", "reusable-workflow call"},
		{"lonely", "", "declares no needs"},
		{"ghost", "", "unknown job"},
		{"weird", "", "declares no needs"},
		{"missing", "", "0 jobs match"},
	}
	for _, tc := range cases {
		got, err := parseWorkflowNeeds(workflow, tc.check)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: err = %v, want %q", tc.check, err, tc.err)
			}
			continue
		}
		if err != nil || strings.Join(got, ",") != tc.want {
			t.Errorf("%s: got %v err %v, want %s", tc.check, got, err, tc.want)
		}
	}
	if _, err := parseWorkflowNeeds([]byte("jobs: ["), "x"); err == nil || !strings.Contains(err.Error(), "parse workflow") {
		t.Fatalf("bad yaml err = %v", err)
	}
	if _, err := parseWorkflowNeeds([]byte("jobs:\n  g:\n    needs: [{a: b}]\n"), "g"); err == nil || !strings.Contains(err.Error(), "decode needs") {
		t.Fatalf("bad needs err = %v", err)
	}
}

func TestApplyWaiversFollowsTheWaitStage(t *testing.T) {
	t.Parallel()
	view := githubchecks.PullRequestView{}
	view.Base.Ref = "public-demo"
	view.Head.SHA = waiverHead
	passedWait := githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitPassed, Checks: []githubchecks.RemoteCheck{waiverFailed("e2e")}}
	fresh := func() PullRequestLandResult { return PullRequestLandResult{Evidence: map[string]string{}} }
	merged := func(isMerged bool, err error) func(context.Context, string, string) (githubchecks.PullRequestView, error) {
		return func(context.Context, string, string) (githubchecks.PullRequestView, error) {
			return githubchecks.PullRequestView{Merged: isMerged}, err
		}
	}

	options := waiverOptions(fencedInspector(map[string][]githubchecks.RemoteCheck{waiverTip: {waiverFailed("e2e")}}))
	result, stop, err := applyWaivers(context.Background(), options, "7", view, passedWait, false, fresh())
	if err != nil || stop || len(result.WaivedChecks) != 1 {
		t.Fatalf("judge: stop=%v err=%v waived=%v", stop, err, result.WaivedChecks)
	}

	result, stop, _ = applyWaivers(context.Background(), options, "7", view, githubchecks.PullRequestWaitResult{}, true, fresh())
	if stop || result.Evidence["waivers"] != waiversMoot {
		t.Fatalf("moot: stop=%v evidence=%v", stop, result.Evidence)
	}

	pendingWait := githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitPending, Checks: []githubchecks.RemoteCheck{waiverRunning("e2e")}}
	result, stop, _ = applyWaivers(context.Background(), options, "7", view, pendingWait, false, fresh())
	if !stop || result.RefusalCode != LandRefusalWaivedCheckPending {
		t.Fatalf("pending: stop=%v refusal=%s", stop, result.RefusalCode)
	}

	failedWait := githubchecks.PullRequestWaitResult{Status: githubchecks.PullRequestWaitFailed}
	if result, stop, _ = applyWaivers(context.Background(), options, "7", view, failedWait, false, fresh()); stop || len(result.WaivedChecks) != 0 {
		t.Fatalf("skip: stop=%v waived=%v", stop, result.WaivedChecks)
	}

	broken := waiverOptions(&waiverInspector{required: []githubchecks.RequiredRemoteCheck{{Name: "preflight"}}, tipReason: "404"})
	if _, stop, err = applyWaivers(context.Background(), broken, "7", view, passedWait, false, fresh()); !stop || err == nil {
		t.Fatalf("error: stop=%v err=%v", stop, err)
	}

	refused := waiverOptions(fencedInspector(map[string][]githubchecks.RemoteCheck{waiverTip: {waiverPassed("e2e")}}))
	if result, stop, _ = applyWaivers(context.Background(), refused, "7", view, passedWait, false, fresh()); !stop || result.RefusalCode != LandRefusalWaivedCheckGreen {
		t.Fatalf("refusal: stop=%v refusal=%s", stop, result.RefusalCode)
	}
	armed := fresh()
	armed.AutoMergeArmed = true
	refused.readPullRequest = merged(true, nil)
	if result, stop, _ = applyWaivers(context.Background(), refused, "7", view, passedWait, false, armed); stop || result.Evidence["waivers"] != waiversMoot {
		t.Fatalf("armed and merged: stop=%v evidence=%v", stop, result.Evidence)
	}
	refused.readPullRequest = merged(false, nil)
	if result, stop, _ = applyWaivers(context.Background(), refused, "7", view, passedWait, false, armed); !stop || result.RefusalCode != LandRefusalWaivedCheckGreen {
		t.Fatalf("armed, not merged: stop=%v refusal=%s", stop, result.RefusalCode)
	}
	if (PullRequestLandOptions{}).resolveReadPullRequest() == nil {
		t.Fatal("default pull request reader is nil")
	}
}
