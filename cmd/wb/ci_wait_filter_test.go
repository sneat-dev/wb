package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file covers sneat-dev/wb#627: --workflow/--check filters on
// `wb ci wait` / `wb wait checks`. Fixtures reuse the same fake-`gh`
// convention as cmd/wb/ci_wait_test.go.

// ciWaitFastFailSlice is a short foreground slice for a test whose PASSING
// path needs two real observations (terminal plus a stable reread) but whose
// failure mode, on a regressed filter, is "polls forever instead of noticing
// the pre-fix bug" (sneat-dev/wb#627 minor 2, red-team round 3 on PR #629):
// two of this file's tests took the full 5-minute ciWaitSliceBudget to fail
// on pre-fix code, which reaches the 10-minute foreground ceiling when run
// together. A short slice makes a regression fail in about two seconds
// instead.
const ciWaitFastFailSlice = 2 * time.Second

// ciWaitTwoWorkflowScript is a direct-target (no --pr) fixture on acme/app
// whose target branch requires two check contexts, "build" (produced by the
// "Go CI" Actions workflow, suite 501) and "deploy" (produced by the
// "Deploy" Actions workflow, suite 502, always left pending). "build"
// completes on every observation, so an unfiltered wait would stay pending
// forever on "deploy" while a --workflow "Go CI" wait can pass.
const ciWaitTwoWorkflowScript = `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then
  echo '{"protected":true,"protection":{"required_status_checks":{"contexts":["build","deploy"]}}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then
  echo '{"total_count":2,"workflow_runs":[{"id":9001,"name":"Go CI","workflow_id":1,"event":"push","status":"completed","conclusion":"success","check_suite_id":501,"created_at":"2026-01-01T00:00:00Z"},{"id":9002,"name":"Deploy","workflow_id":2,"event":"push","status":"in_progress","check_suite_id":502,"created_at":"2026-01-01T00:00:00Z"}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":2,"check_runs":[{"id":1,"name":"build","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":501}},{"id":2,"name":"deploy","status":"in_progress","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":502}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`

// ciWaitTwoWorkflowTerminalScript is ciWaitTwoWorkflowScript with both
// workflows' check runs already terminal (both succeeded), so a filter that
// selects neither reports a stable "no check matching the filter has
// registered yet" pending receipt from the first observation on, rather than
// looping out the whole slice budget on an unchanging empty snapshot.
const ciWaitTwoWorkflowTerminalScript = `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then
  echo '{"protected":true,"protection":{"required_status_checks":{"contexts":["build","deploy"]}}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/app/rules/branches/main?per_page=100'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then
  echo '{"total_count":2,"workflow_runs":[{"id":9001,"name":"Go CI","workflow_id":1,"event":"push","status":"completed","conclusion":"success","check_suite_id":501,"created_at":"2026-01-01T00:00:00Z"},{"id":9002,"name":"Deploy","workflow_id":2,"event":"push","status":"completed","conclusion":"success","check_suite_id":502,"created_at":"2026-01-01T00:00:00Z"}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":2,"check_runs":[{"id":1,"name":"build","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":501}},{"id":2,"name":"deploy","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":502}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`

func writeCIWaitFilterExecutable(t *testing.T, script string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin
}

// TestCIWaitWorkflowFilterSelectsOnlyThatWorkflowsCheckRuns covers two
// required behaviours together: the --workflow filter selects only that
// workflow's check runs, and required-check completeness is evaluated only
// over the required checks the filter selects, so the perpetually pending
// "deploy" check from another workflow never blocks a pass.
func TestCIWaitWorkflowFilterSelectsOnlyThatWorkflowsCheckRuns(t *testing.T) {
	writeCIWaitFilterExecutable(t, ciWaitTwoWorkflowScript)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead,
		"--workflow", "Go CI", "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitRereadInterval.String(), "--json",
	}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("output=%q: %v", stdout.String(), err)
	}
	if code != exitOK || output.Status != "passed" {
		t.Fatalf("workflow-filtered wait = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	if len(output.Checks) != 1 || output.Checks[0].Name != "check-run:build" {
		t.Fatalf("workflow filter did not select only the Go CI check run: %#v", output.Checks)
	}
	if output.Filter == nil || len(output.Filter.Workflows) != 1 || output.Filter.Workflows[0] != "Go CI" {
		t.Fatalf("filter block missing or wrong workflows: %+v", output.Filter)
	}
	if output.Filter.MatchedChecks != 1 || output.Filter.RequiredChecks != 1 {
		t.Fatalf("filter block matched counts = %+v, want 1 check and 1 required check (build only)", output.Filter)
	}
	if len(output.RequiredChecks) != 1 || output.RequiredChecks[0].Name != "build" {
		t.Fatalf("required-check evaluation was not scoped to the filtered workflow: %#v", output.RequiredChecks)
	}
}

// TestCIWaitCheckGlobFilterSelectsMatchingChecks covers the --check glob
// (WB's own hand-rolled matcher, not path.Match or regexp — see
// simpleGlobMatch) and confirms a pending check outside the selection
// ("lint") never blocks the pass.
func TestCIWaitCheckGlobFilterSelectsMatchingChecks(t *testing.T) {
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/glob/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/glob/rules/branches/main?per_page=100'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":3,"check_runs":[{"id":1,"name":"Release / macos","status":"completed","conclusion":"success"},{"id":2,"name":"Release / linux","status":"completed","conclusion":"success"},{"id":3,"name":"lint","status":"in_progress"}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitFilterExecutable(t, script)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"ci", "wait", "--repo", "acme/glob", "--target", "main", "--head", ciWaitHead,
		"--check", "Release / *", "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitRereadInterval.String(), "--json",
	}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("output=%q: %v", stdout.String(), err)
	}
	if code != exitOK || output.Status != "passed" {
		t.Fatalf("check-glob-filtered wait = code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	if len(output.Checks) != 2 {
		t.Fatalf("check glob did not select exactly the Release / * checks: %#v", output.Checks)
	}
	for _, check := range output.Checks {
		if !strings.HasPrefix(check.Name, "check-run:Release / ") {
			t.Fatalf("check glob selected an unexpected check: %#v", output.Checks)
		}
	}
}

// TestCIWaitFilterMatchingNothingDoesNotPass covers the required "a filter
// matching nothing does not pass" behaviour (sneat-dev/wb#627 M2, red-team
// finding on PR #629): it stays pending with a diagnostic that only says
// nothing has registered YET — never that it "will not match later" or is
// "not found", since WB cannot know that from an absence and a late
// workflow_run-triggered workflow can still register within the slice.
func TestCIWaitFilterMatchingNothingDoesNotPass(t *testing.T) {
	writeCIWaitFilterExecutable(t, ciWaitTwoWorkflowTerminalScript)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead,
		"--workflow", "Nonexistent Workflow", "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitSingleObservationInterval.String(), "--json",
	}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("output=%q: %v", stdout.String(), err)
	}
	if output.Status == "passed" {
		t.Fatalf("a filter matching nothing must never pass: code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	if code == exitOK {
		t.Fatalf("a filter matching nothing must exit nonzero: code %d output=%+v", code, output)
	}
	if output.Status != "pending" {
		t.Fatalf("a filter matching nothing should stay pending, not fail: %+v", output)
	}
	if !strings.HasPrefix(output.Reason, "no check matching the filter has registered yet") {
		t.Fatalf("filter-matches-nothing reason wording changed: %q", output.Reason)
	}
	if strings.Contains(output.Reason, "not found") || strings.Contains(output.Reason, "will not match later") {
		t.Fatalf("filter-matches-nothing reason must never claim the filter can never match: %q", output.Reason)
	}
	if len(output.ResumeArgs) == 0 {
		t.Fatalf("a filter matching nothing must resume, not dead-end: %+v", output)
	}
	if output.Filter == nil || output.Filter.MatchedChecks != 0 {
		t.Fatalf("filter block should report zero matched checks: %+v", output.Filter)
	}
}

// TestCIWaitFilterFailureInsideSubsetFails covers the required "a failure
// inside the subset fails" behaviour, and (sneat-dev/wb#627 M1, red-team
// finding on PR #629) that the "filter" block is present on a FAILED result
// too, not only a passed one — the filter block is set before the first
// possible return, not filled in only on the terminal path.
func TestCIWaitFilterFailureInsideSubsetFails(t *testing.T) {
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/glob/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/glob/rules/branches/main?per_page=100'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":2,"check_runs":[{"id":1,"name":"Release / macos","status":"completed","conclusion":"failure","html_url":"https://github.com/acme/glob/actions/runs/1/job/1"},{"id":2,"name":"Release / linux","status":"completed","conclusion":"success"}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -Fq '/check-runs/1/annotations'; then
  echo '[]'
  exit 0
fi
if [ "$1" = run ] && [ "$2" = view ]; then
  echo "" >&2
  exit 1
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitFilterExecutable(t, script)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"ci", "wait", "--repo", "acme/glob", "--target", "main", "--head", ciWaitHead,
		"--check", "Release / *", "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitRereadInterval.String(), "--json",
	}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("output=%q: %v", stdout.String(), err)
	}
	if code != exitFindings || output.Status != "failed" {
		t.Fatalf("failure inside the filtered subset should fail: code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	if output.Filter == nil {
		t.Fatalf("a failed filtered result must still carry the filter block: %+v", output)
	}
	if len(output.Filter.Checks) != 1 || output.Filter.Checks[0] != "Release / *" {
		t.Fatalf("filter block on a failed result has the wrong pattern: %+v", output.Filter)
	}

	// The text spelling of the same failed, filtered result must carry the
	// "filter:" line too.
	var textOut, textErr bytes.Buffer
	textCode := run([]string{
		"ci", "wait", "--repo", "acme/glob", "--target", "main", "--head", ciWaitHead,
		"--check", "Release / *", "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitRereadInterval.String(),
	}, &textOut, &textErr)
	if textCode != exitFindings {
		t.Fatalf("text-mode failed filtered wait = code %d stderr=%s", textCode, textErr.String())
	}
	if !strings.Contains(textOut.String(), "filter:") {
		t.Fatalf("a failed filtered text result must still carry the filter: line: %s", textOut.String())
	}
}

// TestCIWaitFilterResumeArgsCarryFiltersSafelyQuoted covers the required
// "the resume command carries the filters, and quoting is safe" behaviour.
func TestCIWaitFilterResumeArgsCarryFiltersSafelyQuoted(t *testing.T) {
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/pending/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/pending/rules/branches/main?per_page=100'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":1,"check_runs":[{"id":1,"name":"Release / macos","status":"in_progress"}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitFilterExecutable(t, script)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"ci", "wait", "--repo", "acme/pending", "--target", "main", "--head", ciWaitHead,
		"--workflow", "Go CI", "--check", "Release / *",
		"--slice", ciWaitSliceBudget.String(), "--interval", ciWaitSingleObservationInterval.String(),
	}, &stdout, &stderr)
	if code != exitFindings {
		t.Fatalf("pending filtered wait = code %d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	text := stdout.String()
	if !strings.Contains(text, "resume:") {
		t.Fatalf("pending output missing resume line: %s", text)
	}
	if !strings.Contains(text, "--workflow") || !strings.Contains(text, "'Go CI'") {
		t.Fatalf("resume line does not carry --workflow safely quoted: %s", text)
	}
	if !strings.Contains(text, "--check") || !strings.Contains(text, "'Release / *'") {
		t.Fatalf("resume line does not carry --check safely quoted: %s", text)
	}

	// The JSON spelling of the same pending wait must carry the same filters
	// as plain, unquoted array elements.
	var stdoutJSON, stderrJSON bytes.Buffer
	codeJSON := run([]string{
		"ci", "wait", "--repo", "acme/pending", "--target", "main", "--head", ciWaitHead,
		"--workflow", "Go CI", "--check", "Release / *",
		"--slice", ciWaitSliceBudget.String(), "--interval", ciWaitSingleObservationInterval.String(), "--json",
	}, &stdoutJSON, &stderrJSON)
	if codeJSON != exitFindings {
		t.Fatalf("pending filtered json wait = code %d stderr=%s", codeJSON, stderrJSON.String())
	}
	var output ciWaitOutput
	if err := json.Unmarshal(stdoutJSON.Bytes(), &output); err != nil {
		t.Fatalf("output=%q: %v", stdoutJSON.String(), err)
	}
	joined := strings.Join(output.ResumeArgs, " ")
	if !strings.Contains(joined, "--workflow Go CI") || !strings.Contains(joined, "--check Release / *") {
		t.Fatalf("json resume_args do not carry the filters unquoted: %#v", output.ResumeArgs)
	}
}

// TestCIWaitFilterJSONBlockPresentOnlyWhenFiltered covers the required "the
// JSON filter block is present only when filtered" behaviour.
func TestCIWaitFilterJSONBlockPresentOnlyWhenFiltered(t *testing.T) {
	writeCIWaitFilterExecutable(t, ciWaitTwoWorkflowScript)

	var filteredOut, filteredErr bytes.Buffer
	if code := run([]string{
		"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead,
		"--workflow", "Go CI", "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitRereadInterval.String(), "--json",
	}, &filteredOut, &filteredErr); code != exitOK {
		t.Fatalf("filtered wait = code %d stderr=%s", code, filteredErr.String())
	}
	if !strings.Contains(filteredOut.String(), `"filter"`) {
		t.Fatalf("filtered JSON output is missing the filter block: %s", filteredOut.String())
	}

	// An unfiltered wait against the same fixture must carry no filter block
	// at all, and must otherwise stay pending forever on "deploy" — the
	// no-filter path is unchanged.
	var unfilteredOut, unfilteredErr bytes.Buffer
	code := run([]string{
		"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead,
		"--slice", ciWaitSliceBudget.String(), "--interval", ciWaitSingleObservationInterval.String(), "--json",
	}, &unfilteredOut, &unfilteredErr)
	if code != exitFindings {
		t.Fatalf("unfiltered wait should stay pending on the never-terminal deploy check: code %d stderr=%s", code, unfilteredErr.String())
	}
	if strings.Contains(unfilteredOut.String(), `"filter"`) {
		t.Fatalf("unfiltered JSON output must never carry a filter block: %s", unfilteredOut.String())
	}
}

// TestCIWaitFilterKeepsPendingWhileANeedsGatedJobHasNotRegistered covers B1
// (red-team finding on PR #629): a --check filter must not pass while its
// parent Actions workflow run is still in_progress, even though the one job
// the filter selected has already passed — the classic `needs:` chain case,
// where a later job (e.g. "Release / Finalize public release tag") has no
// check-run of its own yet because it has not started. WB's only observable
// evidence that the workflow run itself is still going is the synthetic
// "workflow-run:<id>:<event>" entry, which this filter must keep even though
// its own synthetic name never matches a --check pattern.
func TestCIWaitFilterKeepsPendingWhileANeedsGatedJobHasNotRegistered(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/release/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/release/rules/branches/main?per_page=100'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":1,"check_runs":[{"id":1,"name":"Release / Build","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":601}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then
  if [ "${WB_CI_WAIT_INVOCATION:-0}" -lt 2 ]; then
    echo '{"total_count":1,"workflow_runs":[{"id":7001,"name":"Release","workflow_id":3,"event":"push","status":"in_progress","conclusion":"","check_suite_id":601,"created_at":"2026-01-01T00:00:00Z"}]}'
  else
    echo '{"total_count":1,"workflow_runs":[{"id":7001,"name":"Release","workflow_id":3,"event":"push","status":"completed","conclusion":"success","check_suite_id":601,"created_at":"2026-01-01T00:00:00Z"}]}'
  fi
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Invocation 1: "Release / Build" has passed, but the parent "Release"
	// workflow run is still in_progress (a later needs:-gated job has not
	// registered). The filtered wait must stay pending, not pass.
	t.Setenv("WB_CI_WAIT_INVOCATION", "1")
	var pendingOut, pendingErr bytes.Buffer
	pendingCode := run([]string{
		"ci", "wait", "--repo", "acme/release", "--target", "main", "--head", ciWaitHead,
		"--check", "Release / *", "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitSingleObservationInterval.String(), "--json",
	}, &pendingOut, &pendingErr)
	var pending ciWaitOutput
	if err := json.Unmarshal(pendingOut.Bytes(), &pending); err != nil {
		t.Fatalf("output=%q: %v", pendingOut.String(), err)
	}
	if pendingCode != exitFindings || pending.Status != "pending" {
		t.Fatalf("filtered wait must stay pending while the parent workflow run is in_progress: code %d output=%+v stderr=%s", pendingCode, pending, pendingErr.String())
	}
	foundSyntheticEntry := false
	for _, check := range pending.Checks {
		if strings.HasPrefix(check.Name, "workflow-run:3:") {
			foundSyntheticEntry = true
			if check.Bucket == "pass" || check.Bucket == "skipping" || check.Bucket == "fail" || check.Bucket == "cancel" {
				t.Fatalf("synthetic workflow-run entry should still be pending: %#v", check)
			}
		}
	}
	if !foundSyntheticEntry {
		t.Fatalf("filter dropped the owning workflow's still-running synthetic entry: %#v", pending.Checks)
	}

	// Invocation 2: the "Release" workflow run has now completed. The
	// filtered wait can pass.
	t.Setenv("WB_CI_WAIT_INVOCATION", "2")
	var passedOut, passedErr bytes.Buffer
	passedCode := run([]string{
		"ci", "wait", "--repo", "acme/release", "--target", "main", "--head", ciWaitHead,
		"--check", "Release / *", "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitRereadInterval.String(), "--json",
	}, &passedOut, &passedErr)
	var passed ciWaitOutput
	if err := json.Unmarshal(passedOut.Bytes(), &passed); err != nil {
		t.Fatalf("output=%q: %v", passedOut.String(), err)
	}
	if passedCode != exitOK || passed.Status != "passed" {
		t.Fatalf("filtered wait should pass once the parent workflow run completes: code %d output=%+v stderr=%s", passedCode, passed, passedErr.String())
	}
	if len(passed.Checks) != 1 || passed.Checks[0].Name != "check-run:Release / Build" {
		t.Fatalf("passed receipt should carry only the completed job, not the now-skipped synthetic entry: %#v", passed.Checks)
	}
}

// TestCIWaitExactCheckPassesDespiteASlowSiblingJobInTheSameSuite covers B1-R
// (red-team round 2 on PR #629, the regression the B1 fix introduced): an
// exact `--check "build"` selects only the "build" job. A sibling "race" job
// still in_progress in the very same Actions suite must never hold the wait
// pending once "build" itself has matched and gone terminal — retaining the
// owning workflow run's synthetic entry unconditionally would have made an
// exact selection wait for the whole workflow, the opposite of what "exact"
// promises (and the opposite of #627's own headline case, which needs the
// unrelated check EXCLUDED). Both check-runs carry a github-actions app slug
// so the suite-correlation path in commitCheckRuns is exercised, not the
// third-party fallback.
func TestCIWaitExactCheckPassesDespiteASlowSiblingJobInTheSameSuite(t *testing.T) {
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/racecheck/branches/main' ]; then
  echo '{"protected":true,"protection":{"required_status_checks":{"contexts":["build"]}}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/racecheck/rules/branches/main?per_page=100'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then
  echo '{"total_count":1,"workflow_runs":[{"id":8001,"name":"CI","workflow_id":5,"event":"push","status":"in_progress","conclusion":"","check_suite_id":701,"created_at":"2026-01-01T00:00:00Z"}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":2,"check_runs":[{"id":1,"name":"build","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":701}},{"id":2,"name":"race","status":"in_progress","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":701}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitFilterExecutable(t, script)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"ci", "wait", "--repo", "acme/racecheck", "--target", "main", "--head", ciWaitHead,
		"--check", "build", "--slice", ciWaitFastFailSlice.String(), "--interval", ciWaitRereadInterval.String(), "--json",
	}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("output=%q: %v", stdout.String(), err)
	}
	if code != exitOK || output.Status != "passed" {
		t.Fatalf("an exact --check must not wait for a slow sibling job in the same suite: code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	if len(output.Checks) != 1 || output.Checks[0].Name != "check-run:build" {
		t.Fatalf("exact --check should select only build, not the sibling race job or the run's own synthetic entry: %#v", output.Checks)
	}
}

// TestCIWaitExactCheckDoesNotWaitForAnUnrelatedRunAwaitingApproval covers
// B1-R's second scenario: an Actions run stuck in "waiting" (an environment
// approval) must not hold an exact `--check` that has already matched and
// gone terminal, even though the run itself is still non-terminal.
func TestCIWaitExactCheckDoesNotWaitForAnUnrelatedRunAwaitingApproval(t *testing.T) {
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/approval/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/approval/rules/branches/main?per_page=100'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then
  echo '{"total_count":1,"workflow_runs":[{"id":8002,"name":"Deploy","workflow_id":6,"event":"push","status":"waiting","conclusion":"","check_suite_id":702,"created_at":"2026-01-01T00:00:00Z"}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":1,"check_runs":[{"id":1,"name":"build","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":702}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitFilterExecutable(t, script)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"ci", "wait", "--repo", "acme/approval", "--target", "main", "--head", ciWaitHead,
		"--check", "build", "--slice", ciWaitFastFailSlice.String(), "--interval", ciWaitRereadInterval.String(), "--json",
	}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("output=%q: %v", stdout.String(), err)
	}
	if code != exitOK || output.Status != "passed" {
		t.Fatalf("an exact --check that already matched must not wait on an unrelated run stuck in environment approval: code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	if len(output.Checks) != 1 || output.Checks[0].Name != "check-run:build" {
		t.Fatalf("the waiting run's synthetic entry must not be retained once the exact check it owns is matched: %#v", output.Checks)
	}
}

// TestCIWaitAllExactCheckPatternsMustMatchBeforePass covers the multi-pattern
// completeness gate B1-R also requires: with several --check patterns, a
// pass needs every exact (non-glob) one to have matched at least one
// observed check, not merely one of them. Without this a mistyped or
// not-yet-registered exact job name would let an unrelated matched sibling
// wave the whole wait through.
func TestCIWaitAllExactCheckPatternsMustMatchBeforePass(t *testing.T) {
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/multiexact/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/multiexact/rules/branches/main?per_page=100'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then
  echo '{"total_count":1,"workflow_runs":[{"id":9101,"name":"CI","workflow_id":7,"event":"push","status":"completed","conclusion":"success","check_suite_id":801,"created_at":"2026-01-01T00:00:00Z"}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":1,"check_runs":[{"id":1,"name":"build","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":801}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitFilterExecutable(t, script)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"ci", "wait", "--repo", "acme/multiexact", "--target", "main", "--head", ciWaitHead,
		"--check", "build", "--check", "test", "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitSingleObservationInterval.String(), "--json",
	}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("output=%q: %v", stdout.String(), err)
	}
	if output.Status == "passed" {
		t.Fatalf("build passing must not wave through the wait while the test pattern has never matched: code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	if output.Status != "pending" {
		t.Fatalf("an unmatched exact pattern should stay pending, not fail: %+v", output)
	}
	if !strings.Contains(output.Reason, "test") {
		t.Fatalf("reason should name the unmatched exact pattern: %q", output.Reason)
	}
}

// TestCIWaitCheckGlobCrossesSlashes covers B2 (red-team finding on PR #629):
// "*" must match any run of characters including "/", so "Release / *"
// matches matrix job names like "Release / Smoke test published artifact
// (linux/amd64)". It also proves an exact name containing "[" is accepted
// and matched literally, not rejected as invalid glob syntax.
func TestCIWaitCheckGlobCrossesSlashes(t *testing.T) {
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/smoke/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/smoke/rules/branches/main?per_page=100'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":3,"check_runs":[{"id":1,"name":"Release / Smoke test published artifact (linux/amd64)","status":"completed","conclusion":"success"},{"id":2,"name":"Release / Smoke test Homebrew cask install (darwin/arm64)","status":"completed","conclusion":"success"},{"id":3,"name":"unrelated [tag]","status":"completed","conclusion":"success"}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitFilterExecutable(t, script)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"ci", "wait", "--repo", "acme/smoke", "--target", "main", "--head", ciWaitHead,
		"--check", "Release / *", "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitRereadInterval.String(), "--json",
	}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("output=%q: %v", stdout.String(), err)
	}
	if code != exitOK || output.Status != "passed" {
		t.Fatalf("glob crossing / must select both smoke-test checks: code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	if len(output.Checks) != 2 {
		t.Fatalf("glob should select exactly the two Release / Smoke test checks, not the unrelated one: %#v", output.Checks)
	}
	for _, check := range output.Checks {
		if !strings.Contains(check.Name, "Smoke test") {
			t.Fatalf("glob selected an unexpected check: %#v", output.Checks)
		}
	}
}

// TestCIWaitCheckExactNameWithBracketIsLiteral covers B2's validation side:
// an exact --check name containing "[" must be accepted (not rejected as
// invalid glob syntax the way path.Match would) and matched literally.
func TestCIWaitCheckExactNameWithBracketIsLiteral(t *testing.T) {
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/bracket/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/bracket/rules/branches/main?per_page=100'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":2,"check_runs":[{"id":1,"name":"deploy [staging]","status":"completed","conclusion":"success"},{"id":2,"name":"deploy [production]","status":"in_progress"}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitFilterExecutable(t, script)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"ci", "wait", "--repo", "acme/bracket", "--target", "main", "--head", ciWaitHead,
		"--check", "deploy [staging]", "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitRereadInterval.String(), "--json",
	}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("output=%q: %v", stdout.String(), err)
	}
	if code != exitOK || output.Status != "passed" {
		t.Fatalf("an exact --check name containing [ must be accepted and matched literally: code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	if len(output.Checks) != 1 || output.Checks[0].Name != "check-run:deploy [staging]" {
		t.Fatalf("[ should be a literal character, matching only the exact name: %#v", output.Checks)
	}
}

// TestCIWaitAllWorkflowNamesMustMatchBeforePass covers M1 (red-team round 3
// on PR #629): with several --workflow names, a pass requires every one of
// them to have produced at least one observed check, not merely one. "CI"
// has finished; "Release" — which this repo only starts via workflow_run
// after CI, so it does not exist in the Actions-runs receipt at all yet — has
// never appeared. The filtered wait must stay pending, naming "Release".
func TestCIWaitAllWorkflowNamesMustMatchBeforePass(t *testing.T) {
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/multiworkflow/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/multiworkflow/rules/branches/main?per_page=100'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then
  echo '{"total_count":1,"workflow_runs":[{"id":9001,"name":"CI","workflow_id":1,"event":"push","status":"completed","conclusion":"success","check_suite_id":501,"created_at":"2026-01-01T00:00:00Z"}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":1,"check_runs":[{"id":1,"name":"build","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":501}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitFilterExecutable(t, script)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"ci", "wait", "--repo", "acme/multiworkflow", "--target", "main", "--head", ciWaitHead,
		"--workflow", "CI", "--workflow", "Release", "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitSingleObservationInterval.String(), "--json",
	}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("output=%q: %v", stdout.String(), err)
	}
	if output.Status == "passed" {
		t.Fatalf("CI finishing must not pass the wait while Release has never appeared: code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	if output.Status != "pending" {
		t.Fatalf("an unmatched --workflow name should stay pending, not fail: %+v", output)
	}
	if !strings.Contains(output.Reason, "Release") || strings.Contains(output.Reason, "\"CI\"") {
		t.Fatalf("reason should name the unmatched workflow (Release), not the matched one: %q", output.Reason)
	}
}

// TestCIWaitCheckGlobKeepsAnotherJoblessRunOpen covers M2(a) (red-team round
// 3 on PR #629): --check "deploy*" matches "deploy-docs" in the Docs run,
// which has finished. A second, unrelated Prod run is a concurrency-group
// wait with no job registered at all yet (jobless). The filtered wait must
// stay pending until Prod either registers a matching job or otherwise
// resolves — it must not pass just because a different run's job already
// matched the glob.
func TestCIWaitCheckGlobKeepsAnotherJoblessRunOpen(t *testing.T) {
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/globruns/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/globruns/rules/branches/main?per_page=100'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":1,"check_runs":[{"id":1,"name":"deploy-docs","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":601}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then
  echo '{"total_count":2,"workflow_runs":[{"id":7001,"name":"Docs","workflow_id":40,"event":"push","status":"completed","conclusion":"success","check_suite_id":601,"created_at":"2026-01-01T00:00:00Z"},{"id":7002,"name":"Prod","workflow_id":41,"event":"push","status":"pending","conclusion":"","check_suite_id":602,"created_at":"2026-01-01T00:00:00Z"}]}'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitFilterExecutable(t, script)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"ci", "wait", "--repo", "acme/globruns", "--target", "main", "--head", ciWaitHead,
		"--check", "deploy*", "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitSingleObservationInterval.String(), "--json",
	}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("output=%q: %v", stdout.String(), err)
	}
	if output.Status == "passed" {
		t.Fatalf("the Docs job matching must not pass the wait while Prod has registered no job at all: code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	foundJoblessEntry := false
	for _, check := range output.Checks {
		if strings.HasPrefix(check.Name, "workflow-run:41:") {
			foundJoblessEntry = true
		}
	}
	if !foundJoblessEntry {
		t.Fatalf("filter dropped the jobless Prod run's synthetic entry: %#v", output.Checks)
	}
}

// TestCIWaitCheckExactNameAcrossPushAndPullRequestEvents covers M2(b)
// (red-team round 3 on PR #629, new since round 2): an exact `--check build`
// matches the push run's "build" job, which has passed. The same workflow's
// pull_request run — same WorkflowID, different event, so a run identity
// keyed on WorkflowID alone would conflate the two — is still "queued" and
// has not registered a "build" job of its own yet. The filtered wait must
// stay pending, not pass just because the push run's job already matched.
func TestCIWaitCheckExactNameAcrossPushAndPullRequestEvents(t *testing.T) {
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/dualtrigger/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/dualtrigger/rules/branches/main?per_page=100'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":1,"check_runs":[{"id":1,"name":"build","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":701}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then
  echo '{"total_count":2,"workflow_runs":[{"id":8001,"name":"CI","workflow_id":20,"event":"push","status":"completed","conclusion":"success","check_suite_id":701,"created_at":"2026-01-01T00:00:00Z"},{"id":8002,"name":"CI","workflow_id":20,"event":"pull_request","status":"queued","conclusion":"","check_suite_id":702,"created_at":"2026-01-01T00:00:01Z"}]}'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitFilterExecutable(t, script)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"ci", "wait", "--repo", "acme/dualtrigger", "--target", "main", "--head", ciWaitHead,
		"--check", "build", "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitSingleObservationInterval.String(), "--json",
	}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("output=%q: %v", stdout.String(), err)
	}
	if output.Status == "passed" {
		t.Fatalf("the push run's build matching must not pass the wait while the pull_request run's build has not registered: code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	foundJoblessEntry := false
	for _, check := range output.Checks {
		if strings.HasPrefix(check.Name, "workflow-run:20:pull_request") {
			foundJoblessEntry = true
		}
	}
	if !foundJoblessEntry {
		t.Fatalf("filter dropped the still-queued pull_request run's synthetic entry: %#v", output.Checks)
	}
}

// TestCIWaitOwnershipKeysOnWorkflowIDAndEventNotIDAlone covers minor 1 (red-
// team round 3 on PR #629): two runs share a WorkflowID but differ by event.
// An exact `--check deploy` matches and passes in the push run. The
// pull_request run — same WorkflowID — has already registered a DIFFERENT,
// unrelated, still-running job ("verify"): it is not jobless, and it is not
// owned by the "deploy" match, because ownership keys on (WorkflowID, Event),
// not WorkflowID alone. The wait must pass once "deploy" is terminal, not
// wait on "verify".
func TestCIWaitOwnershipKeysOnWorkflowIDAndEventNotIDAlone(t *testing.T) {
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/sharedworkflowid/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/sharedworkflowid/rules/branches/main?per_page=100'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":2,"check_runs":[{"id":1,"name":"deploy","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":901}},{"id":2,"name":"verify","status":"in_progress","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":902}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then
  echo '{"total_count":2,"workflow_runs":[{"id":9101,"name":"Shared","workflow_id":50,"event":"push","status":"completed","conclusion":"success","check_suite_id":901,"created_at":"2026-01-01T00:00:00Z"},{"id":9102,"name":"Shared","workflow_id":50,"event":"pull_request","status":"in_progress","conclusion":"","check_suite_id":902,"created_at":"2026-01-01T00:00:01Z"}]}'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitFilterExecutable(t, script)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"ci", "wait", "--repo", "acme/sharedworkflowid", "--target", "main", "--head", ciWaitHead,
		"--check", "deploy", "--slice", ciWaitFastFailSlice.String(), "--interval", ciWaitRereadInterval.String(), "--json",
	}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("output=%q: %v", stdout.String(), err)
	}
	if code != exitOK || output.Status != "passed" {
		t.Fatalf("an unrelated in_progress job under the same WorkflowID but a different event must not hold the wait: code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	if len(output.Checks) != 1 || output.Checks[0].Name != "check-run:deploy" {
		t.Fatalf("passed receipt should carry only the matched deploy job: %#v", output.Checks)
	}
}

// TestCIWaitCombinedWorkflowAndCheckWaitsForWholeRun covers minor 1 (red-team
// round 3 on PR #629): with --workflow AND --check combined, an exact
// `--check publish` under `--workflow Release` matches and is terminal, but
// a sibling job in the same Release run ("verify", gated by `needs: publish`)
// has not registered yet. Because --workflow means "wait for the whole run"
// even when combined with an exact --check, the wait must stay pending — this
// specifically exercises the workflowOwnedRuns retention path, not
// globOwnedRuns or anyUnmatchedExact (both false here, since the sole exact
// pattern "publish" has already matched).
func TestCIWaitCombinedWorkflowAndCheckWaitsForWholeRun(t *testing.T) {
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/combined/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/combined/rules/branches/main?per_page=100'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":1,"check_runs":[{"id":1,"name":"publish","status":"completed","conclusion":"success","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":1001}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then
  echo '{"total_count":1,"workflow_runs":[{"id":11001,"name":"Release","workflow_id":60,"event":"push","status":"in_progress","conclusion":"","check_suite_id":1001,"created_at":"2026-01-01T00:00:00Z"}]}'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitFilterExecutable(t, script)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"ci", "wait", "--repo", "acme/combined", "--target", "main", "--head", ciWaitHead,
		"--workflow", "Release", "--check", "publish", "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitSingleObservationInterval.String(), "--json",
	}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("output=%q: %v", stdout.String(), err)
	}
	if output.Status == "passed" {
		t.Fatalf("publish matching must not pass the wait while --workflow Release's run is still in_progress: code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	foundRunEntry := false
	for _, check := range output.Checks {
		if strings.HasPrefix(check.Name, "workflow-run:60:") {
			foundRunEntry = true
		}
	}
	if !foundRunEntry {
		t.Fatalf("--workflow combined with --check should still retain the owning run's synthetic entry: %#v", output.Checks)
	}
}

// TestCIWaitExactCheckSkippedByAnEarlierFailureIsNotAPass covers M3
// (red-team round 3 on PR #629): "build" failed, so "publish" (which
// `needs: build`) concluded "skipped", and the run itself concluded
// "failure". An exact `--check publish` must not report passed — that would
// answer "is the release built?", #627's own headline use, with a false yes.
func TestCIWaitExactCheckSkippedByAnEarlierFailureIsNotAPass(t *testing.T) {
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/git/ref/heads/main'; then
  echo '{"object":{"sha":"0123456789012345678901234567890123456789"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/skipchain/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'
  exit 0
fi
if [ "$1" = api ] && echo "$*" | grep -Fq 'repos/acme/skipchain/rules/branches/main?per_page=100'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/check-runs?per_page=100'; then
  echo '{"total_count":2,"check_runs":[{"id":1,"name":"build","status":"completed","conclusion":"failure","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":801}},{"id":2,"name":"publish","status":"completed","conclusion":"skipped","app":{"id":15368,"slug":"github-actions"},"check_suite":{"id":801}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then
  echo '{"total_count":1,"workflow_runs":[{"id":12001,"name":"Release","workflow_id":30,"event":"push","status":"completed","conclusion":"failure","check_suite_id":801,"created_at":"2026-01-01T00:00:00Z"}]}'
  exit 0
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitFilterExecutable(t, script)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"ci", "wait", "--repo", "acme/skipchain", "--target", "main", "--head", ciWaitHead,
		"--check", "publish", "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitSingleObservationInterval.String(), "--json",
	}, &stdout, &stderr)
	var output ciWaitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("output=%q: %v", stdout.String(), err)
	}
	if output.Status == "passed" {
		t.Fatalf("a --check on a job skipped because an earlier job failed must never report passed: code %d output=%+v stderr=%s", code, output, stderr.String())
	}
	if output.Status != "failed" || code == exitOK {
		t.Fatalf("skipped-by-upstream-failure should fail, matching what an unfiltered wait would see: %+v", output)
	}
}

// TestCIWaitBothSpellingsAcceptFilterFlags covers the required "both
// spellings (`wb ci wait` and `wb wait checks`) accept the flags" behaviour.
func TestCIWaitBothSpellingsAcceptFilterFlags(t *testing.T) {
	for _, spelling := range [][]string{
		{"ci", "wait"},
		{"wait", "checks"},
	} {
		t.Run(strings.Join(spelling, " "), func(t *testing.T) {
			writeCIWaitFilterExecutable(t, ciWaitTwoWorkflowScript)
			var stdout, stderr bytes.Buffer
			args := append(append([]string(nil), spelling...),
				"--repo", "acme/app", "--target", "main", "--head", ciWaitHead,
				"--workflow", "Go CI", "--check", "build",
				"--slice", ciWaitSliceBudget.String(), "--interval", ciWaitRereadInterval.String(), "--json")
			code := run(args, &stdout, &stderr)
			var output ciWaitOutput
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
				t.Fatalf("%s output=%q: %v", strings.Join(spelling, " "), stdout.String(), err)
			}
			if code != exitOK || output.Status != "passed" || output.Filter == nil {
				t.Fatalf("%s did not accept --workflow/--check: code %d output=%+v stderr=%s", strings.Join(spelling, " "), code, output, stderr.String())
			}
		})
	}
}
