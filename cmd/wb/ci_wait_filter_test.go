package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file covers sneat-dev/wb#627: --workflow/--check filters on
// `wb ci wait` / `wb wait checks`. Fixtures reuse the same fake-`gh`
// convention as cmd/wb/ci_wait_test.go.

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
// selects neither can be told, on the very first observation, that it will
// never match rather than polling out the whole slice.
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
// (path.Match "*", not regex) and confirms a pending check outside the
// selection ("lint") never blocks the pass.
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
// matching nothing does not pass" behaviour and its explicit not-found
// reason once the head's observed checks are all terminal.
func TestCIWaitFilterMatchingNothingDoesNotPass(t *testing.T) {
	writeCIWaitFilterExecutable(t, ciWaitTwoWorkflowTerminalScript)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"ci", "wait", "--repo", "acme/app", "--target", "main", "--head", ciWaitHead,
		"--workflow", "Nonexistent Workflow", "--slice", ciWaitSliceBudget.String(), "--interval", ciWaitRereadInterval.String(), "--json",
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
	if !strings.Contains(output.Reason, "not found") {
		t.Fatalf("filter-matches-nothing reason lacks an explicit not-found diagnostic: %q", output.Reason)
	}
	if output.Filter == nil || output.Filter.MatchedChecks != 0 {
		t.Fatalf("filter block should report zero matched checks: %+v", output.Filter)
	}
}

// TestCIWaitFilterFailureInsideSubsetFails covers the required "a failure
// inside the subset fails" behaviour.
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
