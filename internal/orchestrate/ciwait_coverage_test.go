package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// orchCovCIScript answers the two reads a failed-check report makes: the
// check-run annotations endpoint and the failed-job log.
const orchCovCIScript = `#!/bin/sh
S="$ORCHCOV_GH_STATE"
if [ "$1" = api ]; then
  if [ -f "$S/annotations" ]; then cat "$S/annotations"; fi
  exit "$(cat "$S/annotations-exit")"
fi
if [ "$1" = run ] && [ "$2" = view ]; then
  if [ -f "$S/log" ]; then cat "$S/log"; fi
  if [ -f "$S/log-stderr" ]; then cat "$S/log-stderr" >&2; fi
  exit "$(cat "$S/log-exit")"
fi
echo "unexpected gh args: $*" >&2
exit 30
`

func orchCovCIState(t *testing.T) orchCovGHState {
	t.Helper()
	state := orchCovScriptState(t, orchCovCIScript)
	state.answer(t, "annotations", "[]")
	state.answer(t, "annotations-exit", "0")
	state.answer(t, "log", "")
	state.answer(t, "log-stderr", "")
	state.answer(t, "log-exit", "0")
	return state
}

func TestOrchCovFailedCheckDetailsSkipsChecksThatDidNotFail(t *testing.T) {
	orchCovCIState(t)
	details := failedCheckDetails(context.Background(), "acme/app", []RemoteCheck{
		{Name: "check-run:CI", Bucket: "pass", Link: "https://github.com/acme/app/actions/runs/1/job/2"},
		{Name: "check-run:CI", Bucket: "skipping", Link: "https://github.com/acme/app/actions/runs/1/job/2"},
	})
	if len(details) != 0 {
		t.Fatalf("passing checks produced failure details: %+v", details)
	}
}

func TestOrchCovFailedCheckDetailsPrefersAnnotations(t *testing.T) {
	state := orchCovCIState(t)
	state.answer(t, "annotations", `[`+
		`{"path":"internal/app.go","start_line":3,"end_line":4,"message":"the build failed"},`+
		`{"path":"","start_line":5,"message":"no path"},`+
		`{"path":"a.go","start_line":0,"message":"no line"},`+
		`{"path":"a.go","start_line":1,"message":""},`+
		`{"path":"internal/app.go","start_line":3,"end_line":4,"message":"the build failed"},`+
		`{"path":"dup.go","start_line":1,"end_line":1,"message":"kept"}]`)

	details := failedCheckDetails(context.Background(), "acme/app", []RemoteCheck{{
		Name: "check-run:CI", Bucket: "fail", CheckRunID: 99,
		Link: "https://github.com/acme/app/actions/runs/1/job/2",
	}})
	if len(details) != 1 {
		t.Fatalf("failure details = %+v", details)
	}
	annotations := details[0].Annotations
	if len(annotations) != 2 || annotations[0].Path != "internal/app.go" || annotations[0].StartLine != 3 ||
		annotations[1].Path != "dup.go" {
		t.Fatalf("annotations = %+v, want invalid and duplicate entries dropped", annotations)
	}
	if details[0].Excerpt != "" || details[0].Reason != "" {
		t.Fatalf("annotated failure also fell back to the log: %+v", details[0])
	}
}

func TestOrchCovFailedCheckDetailsStopsAtTheAnnotationCeiling(t *testing.T) {
	state := orchCovCIState(t)
	entries := make([]string, 0, maxFailedCheckAnnotations+4)
	for index := 0; index < maxFailedCheckAnnotations+4; index++ {
		entries = append(entries, fmt.Sprintf(`{"path":"file%d.go","start_line":%d,"end_line":%d,"message":"failure %d"}`,
			index, index+1, index+1, index))
	}
	state.answer(t, "annotations", "["+strings.Join(entries, ",")+"]")

	details := failedCheckDetails(context.Background(), "acme/app", []RemoteCheck{{
		Name: "check-run:CI", Bucket: "cancel", CheckRunID: 99,
		Link: "https://github.com/acme/app/actions/runs/1/job/2",
	}})
	if len(details) != 1 || len(details[0].Annotations) != maxFailedCheckAnnotations {
		t.Fatalf("annotations = %+v, want exactly %d", details, maxFailedCheckAnnotations)
	}
}

func TestOrchCovFailedCheckDetailsReportsAThirdPartyCheckHonestly(t *testing.T) {
	state := orchCovCIState(t)
	state.answer(t, "annotations", "not json")

	details := failedCheckDetails(context.Background(), "acme/app", []RemoteCheck{{
		Name: "check-run:sonar", Bucket: "fail", CheckRunID: 99, Link: "https://sonar.example.test/report",
	}})
	if len(details) != 1 {
		t.Fatalf("failure details = %+v", details)
	}
	if !strings.Contains(details[0].Reason, "identifiers were not available") ||
		!strings.Contains(details[0].Reason, "retrieve failed check annotations") {
		t.Fatalf("third-party reason = %q", details[0].Reason)
	}
	if details[0].JobURL != "https://sonar.example.test/report" {
		t.Fatalf("job url = %q", details[0].JobURL)
	}
}

func TestOrchCovFailedCheckDetailsRetrievesTheFailedJobLogTail(t *testing.T) {
	state := orchCovCIState(t)
	state.answer(t, "log", "step one\nfailing step\ntoken ghp_secretValue\n")

	details := failedCheckDetails(context.Background(), "acme/app", []RemoteCheck{{
		Name: "check-run:CI", Bucket: "fail", CheckRunID: 99,
		Link: "https://github.com/acme/app/actions/runs/1234/job/5678",
	}})
	if len(details) != 1 {
		t.Fatalf("failure details = %+v", details)
	}
	if details[0].RunURL != "https://github.com/acme/app/actions/runs/1234" {
		t.Fatalf("run url = %q", details[0].RunURL)
	}
	if !strings.Contains(details[0].Excerpt, "failing step") || !strings.Contains(details[0].Excerpt, "[REDACTED]") {
		t.Fatalf("excerpt = %q", details[0].Excerpt)
	}
	if strings.Contains(details[0].Excerpt, "ghp_secretValue") {
		t.Fatalf("excerpt leaked a token: %q", details[0].Excerpt)
	}
	if details[0].Reason != "" {
		t.Fatalf("retrieved log still carries a reason: %q", details[0].Reason)
	}
}

func TestOrchCovFailedCheckDetailsReportsAnUnretrievableLog(t *testing.T) {
	state := orchCovCIState(t)
	state.answer(t, "annotations", "not json")
	state.answer(t, "log-exit", "1")
	state.answer(t, "log-stderr", "no logs were found for this job")

	details := failedCheckDetails(context.Background(), "acme/app", []RemoteCheck{{
		Name: "check-run:CI", Bucket: "fail", CheckRunID: 99,
		Link: "https://github.com/acme/app/actions/runs/1234/job/5678",
	}})
	if len(details) != 1 {
		t.Fatalf("failure details = %+v", details)
	}
	if !strings.Contains(details[0].Reason, "retrieve failed-job log: no logs were found for this job") ||
		!strings.Contains(details[0].Reason, "retrieve failed check annotations") {
		t.Fatalf("reason = %q", details[0].Reason)
	}
}

func TestOrchCovFailedCheckDetailsReportsAnEmptyFailedStepLog(t *testing.T) {
	orchCovCIState(t)
	details := failedCheckDetails(context.Background(), "acme/app", []RemoteCheck{{
		Name: "check-run:CI", Bucket: "fail",
		Link: "https://github.com/acme/app/actions/runs/1234/job/5678",
	}})
	if len(details) != 1 || details[0].Reason != "GitHub returned no failed-step log lines" {
		t.Fatalf("failure details = %+v", details)
	}
}

func TestOrchCovFailedCheckAnnotationsRefusesWithoutAnIdentityOrADecodableBody(t *testing.T) {
	state := orchCovCIState(t)
	annotations, err := failedCheckAnnotations(context.Background(), "acme/app", 0, map[string]bool{})
	if err != nil || annotations != nil {
		t.Fatalf("anonymous check run = %+v, err %v", annotations, err)
	}
	state.answer(t, "annotations", "not json")
	if _, err := failedCheckAnnotations(context.Background(), "acme/app", 99, map[string]bool{}); err == nil ||
		!strings.Contains(err.Error(), "decode check-run annotations") {
		t.Fatalf("undecodable annotations error = %v", err)
	}
	orchCovInstallGH(t, orchCovNotFound)
	if _, err := failedCheckAnnotations(context.Background(), "acme/app", 99, map[string]bool{}); err == nil {
		t.Fatal("unreadable annotations were accepted")
	}
}

func TestOrchCovCompactFailureAnnotationTruncatesOnRuneBoundaries(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("é", 20)
	got := compactFailureAnnotation(long, 10)
	if got != strings.Repeat("é", 9)+"…" {
		t.Fatalf("truncated annotation = %q", got)
	}
	if short := compactFailureAnnotation("  a\nb\tc  ", 20); short != "a b c" {
		t.Fatalf("compacted annotation = %q", short)
	}
}

func TestOrchCovGitHubActionsRunAndJobAcceptsOnlyActionsJobLinks(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input       string
		wantRun     string
		wantJob     string
		wantMatched bool
	}{
		{input: "https://github.com/acme/app/actions/runs/123456/job/7890", wantRun: "123456", wantJob: "7890", wantMatched: true},
		{input: "https://github.com/acme/app/actions/runs/123456/job/7890#step:3:5", wantRun: "123456", wantJob: "7890", wantMatched: true},
		{input: "https://example.test/acme/app/actions/runs/1/job/2"},
		{input: "https://github.com/acme/app/actions/runs//job/2"},
		{input: "https://github.com/acme/app/actions/runs/1/job/"},
		{input: "https://github.com/acme/app/checks/1"},
		{input: "https://github.com/acme/app/actions/runs/1;curl%20x/job/2"},
		{input: "https://github.com/acme/app/actions/runs/1/job/2%60id%60"},
		{input: "https://github.com/acme/app/actions/runs/abc/job/2"},
		{input: "://missing-protocol"},
	} {
		runID, jobID, ok := githubActionsRunAndJob(test.input)
		if ok != test.wantMatched || runID != test.wantRun || jobID != test.wantJob {
			t.Fatalf("githubActionsRunAndJob(%q) = %q/%q/%t", test.input, runID, jobID, ok)
		}
	}
}

func TestOrchCovFailedJobLogExcerptKeepsTheTailAndRedacts(t *testing.T) {
	t.Parallel()
	excerpt := failedJobLogExcerpt("\n  a  \n\n b \n c \n", 2)
	if excerpt != "… earlier failed-job log lines omitted …\nb\nc" {
		t.Fatalf("excerpt = %q", excerpt)
	}
	if got := failedJobLogExcerpt("  only  ", 0); got != "only" {
		t.Fatalf("unbounded excerpt = %q", got)
	}
}

func TestOrchCovRedactFailedJobLogLineRedactsEveryToken(t *testing.T) {
	t.Parallel()
	line := "a ghp_abcDEF123 and github_pat_xyz_789 then ghp_short"
	got := redactFailedJobLogLine(line)
	if strings.Contains(got, "ghp_") || strings.Contains(got, "github_pat_") {
		t.Fatalf("token survived redaction: %q", got)
	}
	if !strings.Contains(got, "a [REDACTED] and [REDACTED] then [REDACTED]") {
		t.Fatalf("redacted line = %q", got)
	}
	if plain := redactFailedJobLogLine("no tokens here"); plain != "no tokens here" {
		t.Fatalf("plain line = %q", plain)
	}
}

func TestOrchCovCheckRunBucketNamesEveryConclusion(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		status     string
		conclusion string
		want       string
	}{
		{status: "queued", conclusion: "", want: "pending"},
		{status: "completed", conclusion: "success", want: "pass"},
		// Minor 11 regression (sneat-dev/wb#591 round 3 red-team follow-up):
		// round 2 moved "neutral" into the "skipping" bucket alongside
		// "skipped" (finding X2), which altered `wb ci wait` JSON output and
		// graduation's validateCIWait as an unintended global side effect.
		// Round 3 removed the strict deferral gate X2 existed for, so
		// "neutral" buckets as "pass" again, exactly as before round 2.
		{status: "completed", conclusion: "neutral", want: "pass"},
		{status: "completed", conclusion: "skipped", want: "skipping"},
		{status: "completed", conclusion: "cancelled", want: "cancel"},
		{status: "completed", conclusion: "timed_out", want: "cancel"},
		{status: "completed", conclusion: "action_required", want: "cancel"},
		{status: "completed", conclusion: "failure", want: "fail"},
		{status: "completed", conclusion: "startup_failure", want: "fail"},
		{status: "completed", conclusion: "stale", want: "fail"},
		{status: "completed", conclusion: "mystery", want: "pending"},
	} {
		if got := checkRunBucket(test.status, test.conclusion); got != test.want {
			t.Fatalf("checkRunBucket(%q, %q) = %q, want %q", test.status, test.conclusion, got, test.want)
		}
	}
}

func TestOrchCovCommitStatusBucketNamesEveryState(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		state string
		want  string
	}{
		{state: "success", want: "pass"},
		{state: "SUCCESS", want: "pass"},
		{state: " pending ", want: "pending"},
		{state: "failure", want: "fail"},
		{state: "error", want: "fail"},
		{state: "unknown", want: "pending"},
	} {
		if got := commitStatusBucket(test.state); got != test.want {
			t.Fatalf("commitStatusBucket(%q) = %q, want %q", test.state, got, test.want)
		}
	}
}

// TestOrchCovWorkflowRunConclusionFailedNamesEveryConclusion is a direct
// unit test of workflowRunConclusionFailed (sneat-dev/wb#627 M3, round 3 on
// PR #629): coverage follow-up, since the fake-gh integration tests only
// exercise "failure" and "" before this test was added.
func TestOrchCovWorkflowRunConclusionFailedNamesEveryConclusion(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		conclusion string
		want       bool
	}{
		{conclusion: "", want: false},
		{conclusion: "success", want: false},
		{conclusion: "neutral", want: false},
		{conclusion: "skipped", want: false},
		{conclusion: "mystery", want: false},
		{conclusion: "cancelled", want: true},
		{conclusion: "timed_out", want: true},
		{conclusion: "action_required", want: true},
		{conclusion: "failure", want: true},
		{conclusion: "startup_failure", want: true},
		{conclusion: "stale", want: true},
	} {
		if got := workflowRunConclusionFailed(test.conclusion); got != test.want {
			t.Fatalf("workflowRunConclusionFailed(%q) = %v, want %v", test.conclusion, got, test.want)
		}
	}
}

// TestOrchCovCheckBucketTerminalNamesEveryBucket is a direct unit test of
// checkBucketTerminal, a coverage follow-up (round 3 on PR #629): the
// fake-gh integration tests reach it only through "pending" and one
// terminal bucket per scenario.
func TestOrchCovCheckBucketTerminalNamesEveryBucket(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		bucket string
		want   bool
	}{
		{bucket: "pass", want: true},
		{bucket: "skipping", want: true},
		{bucket: "fail", want: true},
		{bucket: "cancel", want: true},
		{bucket: "pending", want: false},
		{bucket: "mystery", want: false},
		{bucket: "", want: false},
	} {
		if got := checkBucketTerminal(test.bucket); got != test.want {
			t.Fatalf("checkBucketTerminal(%q) = %v, want %v", test.bucket, got, test.want)
		}
	}
}

// TestOrchCovSimpleGlobMatchHandlesEveryPatternShape is a direct unit test
// of simpleGlobMatch, a coverage follow-up (round 3 on PR #629): exercises
// the leading-star, trailing-star, middle-star, no-star, and multi-star
// branches the fake-gh integration tests only sample a couple of.
func TestOrchCovSimpleGlobMatchHandlesEveryPatternShape(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		pattern string
		value   string
		want    bool
	}{
		{name: "exact match, no star", pattern: "build", value: "build", want: true},
		{name: "exact mismatch, no star", pattern: "build", value: "test", want: false},
		{name: "leading star", pattern: "*build", value: "release / build", want: true},
		{name: "leading star, no match", pattern: "*build", value: "release / test", want: false},
		{name: "trailing star", pattern: "build*", value: "build (ubuntu)", want: true},
		{name: "trailing star, no match", pattern: "build*", value: "test (ubuntu)", want: false},
		{name: "bare star matches everything", pattern: "*", value: "anything at all", want: true},
		{name: "bare star matches empty", pattern: "*", value: "", want: true},
		{name: "middle star", pattern: "Release / * / build", value: "Release / linux amd64 / build", want: true},
		{name: "middle star, segment absent", pattern: "Release / * / build", value: "Release / build", want: false},
		{name: "multiple stars", pattern: "*build*coverage*", value: "pre build mid coverage post", want: true},
		{name: "multiple stars, missing middle segment", pattern: "*build*coverage*", value: "pre build only", want: false},
		{name: "star crosses slash", pattern: "Release / *", value: "Release / Smoke test published artifact (linux/amd64)", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := simpleGlobMatch(test.pattern, test.value); got != test.want {
				t.Fatalf("simpleGlobMatch(%q, %q) = %v, want %v", test.pattern, test.value, got, test.want)
			}
		})
	}
}

// TestOrchCovUnmatchedWorkflowNamesRequiresEveryWorkflow is a direct unit
// test of unmatchedWorkflowNames (sneat-dev/wb#627 M1, round 3 on PR #629):
// coverage follow-up for the empty-input and multi-workflow branches the
// fake-gh integration tests reach only once each.
func TestOrchCovUnmatchedWorkflowNamesRequiresEveryWorkflow(t *testing.T) {
	t.Parallel()
	if got := unmatchedWorkflowNames(nil, nil); got != nil {
		t.Fatalf("unmatchedWorkflowNames(nil, nil) = %#v, want nil", got)
	}
	checks := []RemoteCheck{
		{Name: "check-run:build", WorkflowName: "CI"},
		{Name: "check-run:lint", WorkflowName: "CI"},
	}
	if got := unmatchedWorkflowNames(checks, []string{"CI"}); len(got) != 0 {
		t.Fatalf("unmatchedWorkflowNames all matched = %#v, want empty", got)
	}
	got := unmatchedWorkflowNames(checks, []string{"CI", "Release"})
	if want := []string{"Release"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("unmatchedWorkflowNames = %#v, want %#v", got, want)
	}
	got = unmatchedWorkflowNames(checks, []string{"Deploy", "Release"})
	if want := []string{"Deploy", "Release"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("unmatchedWorkflowNames = %#v, want %#v", got, want)
	}
}

// TestOrchCovNearbyObservedCheckNamesFindsSubstringHints and
// TestOrchCovDescribeUnmatchedExactCheckPatternsFormatsHints are direct
// unit tests of the two minor-4 hint helpers (sneat-dev/wb#627, round 3 on
// PR #629): coverage follow-up for branches (empty pattern, exact-match
// exclusion, dedup, the three-item cap, and the no-hint fallback) the
// fake-gh integration tests do not each exercise.
func TestOrchCovNearbyObservedCheckNamesFindsSubstringHints(t *testing.T) {
	t.Parallel()
	if got := nearbyObservedCheckNames(nil, ""); got != nil {
		t.Fatalf("nearbyObservedCheckNames with empty pattern = %#v, want nil", got)
	}
	observed := []RemoteCheck{
		{Name: "check-run:build (ubuntu)"},
		{Name: "check-run:build (macos)"},
		{Name: "check-run:build (ubuntu)"}, // duplicate name, must be deduped
		{Name: "check-run:build"},          // exact match, must be excluded
		{Name: "check-run:lint"},           // no substring match
	}
	got := nearbyObservedCheckNames(observed, "build")
	want := []string{"build (ubuntu)", "build (macos)"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("nearbyObservedCheckNames = %#v, want %#v", got, want)
	}

	// The cap is three, even when more than three distinct names match.
	many := []RemoteCheck{
		{Name: "check-run:build (a)"},
		{Name: "check-run:build (b)"},
		{Name: "check-run:build (c)"},
		{Name: "check-run:build (d)"},
	}
	if got := nearbyObservedCheckNames(many, "build"); len(got) != 3 {
		t.Fatalf("nearbyObservedCheckNames capped = %#v, want 3 entries", got)
	}

	if got := nearbyObservedCheckNames(observed, "no-such-substring"); len(got) != 0 {
		t.Fatalf("nearbyObservedCheckNames with no match = %#v, want empty", got)
	}
}

func TestOrchCovDescribeUnmatchedExactCheckPatternsFormatsHints(t *testing.T) {
	t.Parallel()
	if got := describeUnmatchedExactCheckPatterns(nil, nil); got != "" {
		t.Fatalf("describeUnmatchedExactCheckPatterns with no unmatched = %q, want empty", got)
	}
	observed := []RemoteCheck{{Name: "check-run:build (ubuntu)"}}
	got := describeUnmatchedExactCheckPatterns(observed, []string{"build"})
	if want := "build (observed: build (ubuntu))"; got != want {
		t.Fatalf("describeUnmatchedExactCheckPatterns = %q, want %q", got, want)
	}
	got = describeUnmatchedExactCheckPatterns(observed, []string{"deploy"})
	if want := "deploy"; got != want {
		t.Fatalf("describeUnmatchedExactCheckPatterns with no hint = %q, want %q", got, want)
	}
	got = describeUnmatchedExactCheckPatterns(observed, []string{"build", "deploy"})
	if want := "build (observed: build (ubuntu)), deploy"; got != want {
		t.Fatalf("describeUnmatchedExactCheckPatterns multi = %q, want %q", got, want)
	}
}

// TestOrchCovPendingWorkflowRunHintNamesTheFirstStillRegisteringRun is a
// direct unit test of pendingWorkflowRunHint (sneat-dev/wb#627 minor 2,
// round 4 on PR #629): the empty-input, no-hint, first-match, and
// missing-name/missing-event fallback branches.
func TestOrchCovPendingWorkflowRunHintNamesTheFirstStillRegisteringRun(t *testing.T) {
	t.Parallel()
	if got := pendingWorkflowRunHint(nil); got != "" {
		t.Fatalf("pendingWorkflowRunHint(nil) = %q, want empty", got)
	}
	if got := pendingWorkflowRunHint([]RemoteCheck{
		{Name: "check-run:build", Bucket: "pass"},
		{Name: "workflow-run:1:push", Bucket: "pass"},
	}); got != "" {
		t.Fatalf("pendingWorkflowRunHint with nothing pending = %q, want empty", got)
	}
	got := pendingWorkflowRunHint([]RemoteCheck{
		{Name: "check-run:build", Bucket: "pending"},
		{Name: "workflow-run:2:pull_request", Bucket: "pending", WorkflowName: "Deploy", WorkflowEvent: "pull_request"},
	})
	if want := `run "Deploy" (pull_request) has not registered a job yet`; got != want {
		t.Fatalf("pendingWorkflowRunHint = %q, want %q", got, want)
	}
	got = pendingWorkflowRunHint([]RemoteCheck{
		{Name: "workflow-run:3:", Bucket: "pending"},
	})
	if want := `run "unnamed workflow" (unknown event) has not registered a job yet`; got != want {
		t.Fatalf("pendingWorkflowRunHint with no name/event = %q, want %q", got, want)
	}
}
