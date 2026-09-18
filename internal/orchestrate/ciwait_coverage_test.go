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
