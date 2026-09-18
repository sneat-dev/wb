package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/orchestrate"
)

// waitTestRun drives one bounded slice with an injected observer, so the
// decision contract is proved without a GitHub fixture.
func waitTestRun(t *testing.T, condition waitCondition, targets []waitReference, observations ...map[string]waitTarget) waitOutput {
	t.Helper()
	call := 0
	clock := time.Unix(0, 0)
	return observeWaitTargets(context.Background(), waitRun{
		Targets:   targets,
		Condition: condition,
		Slice:     time.Minute,
		Interval:  time.Second,
		observe: func(_ context.Context, reference waitReference) waitTarget {
			index := call
			if index >= len(observations) {
				index = len(observations) - 1
			}
			observed, ok := observations[index][reference.Selector]
			if !ok {
				t.Fatalf("no observation %d prepared for %s", index, reference.Selector)
			}
			observed.Selector = reference.Selector
			observed.Repository = reference.Repository
			observed.Number = reference.Number
			return observed
		},
		now: func() time.Time { return clock },
		sleep: func(context.Context, time.Duration) error {
			call++
			clock = clock.Add(time.Second)
			if call > len(observations) {
				clock = clock.Add(time.Hour)
			}
			return nil
		},
	})
}

func waitRef(selector string) waitReference {
	repository, number, _ := strings.Cut(selector, "#")
	return waitReference{Selector: selector, Repository: repository, Number: number}
}

func TestWaitPRSettlesOnlyWhenEveryTargetIsTerminal(t *testing.T) {
	targets := []waitReference{waitRef("acme/app#1"), waitRef("acme/app#2")}
	// The first target is terminal from the start. A first-past-the-post wait
	// would return here and starve the second, which is the defect this
	// contract exists to prevent.
	output := waitTestRun(t, waitUntilChecksSettled, targets,
		map[string]waitTarget{
			"acme/app#1": {State: "open", Checks: map[string]int{"pass": 3}},
			"acme/app#2": {State: "open", Checks: map[string]int{"pass": 1, "pending": 2}},
		},
		map[string]waitTarget{
			"acme/app#2": {State: "open", Checks: map[string]int{"pass": 3}},
		},
	)
	if output.Status != waitStatusSettled {
		t.Fatalf("status = %q, want settled; targets = %+v", output.Status, output.Targets)
	}
	if output.Observations < 2 {
		t.Errorf("observations = %d, want at least 2: the second target was still pending", output.Observations)
	}
	for _, target := range output.Targets {
		if target.Status != waitStatusSettled {
			t.Errorf("%s status = %q, want settled", target.Selector, target.Status)
		}
	}
}

func TestWaitPRPendingResumesOnlyTheUnsettledTargets(t *testing.T) {
	targets := []waitReference{waitRef("acme/app#1"), waitRef("acme/app#2")}
	output := waitTestRun(t, waitUntilChecksSettled, targets,
		map[string]waitTarget{
			"acme/app#1": {State: "merged", Checks: map[string]int{"pass": 2}},
			"acme/app#2": {State: "open", Checks: map[string]int{"pending": 1}},
		},
	)
	if output.Status != waitStatusPending {
		t.Fatalf("status = %q, want pending", output.Status)
	}
	args := waitResumeArgs(output, waitUntilChecksSettled, time.Minute, time.Second, false)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "acme/app#1") {
		t.Errorf("resume args re-observe a settled target: %s", joined)
	}
	if !strings.Contains(joined, "acme/app#2") {
		t.Errorf("resume args dropped the pending target: %s", joined)
	}
}

func TestWaitPRChangedComparesOnlyActionableFacts(t *testing.T) {
	targets := []waitReference{waitRef("acme/app#7")}
	// An identical re-observation is not a change; a new head is.
	unchanged := waitTestRun(t, waitUntilChanged, targets,
		map[string]waitTarget{"acme/app#7": {State: "open", Head: "aaa", Checks: map[string]int{"pass": 1}}},
	)
	if unchanged.Status != waitStatusPending {
		t.Errorf("re-reporting the same state settled --until changed: %+v", unchanged.Targets)
	}
	moved := waitTestRun(t, waitUntilChanged, targets,
		map[string]waitTarget{"acme/app#7": {State: "open", Head: "aaa", Checks: map[string]int{"pending": 1}}},
		map[string]waitTarget{"acme/app#7": {State: "open", Head: "bbb", Checks: map[string]int{"pending": 1}}},
	)
	if moved.Status != waitStatusSettled {
		t.Errorf("a new head did not settle --until changed: %+v", moved.Targets)
	}
}

func TestWaitPRClosedIgnoresChecks(t *testing.T) {
	output := waitTestRun(t, waitUntilClosed, []waitReference{waitRef("acme/app#9")},
		map[string]waitTarget{"acme/app#9": {State: "merged", Checks: map[string]int{"pending": 4}}},
	)
	if output.Status != waitStatusSettled {
		t.Fatalf("a merged pull request with pending checks did not settle --until closed: %+v", output.Targets)
	}
}

func TestWaitPRTransientReadStaysPendingRatherThanErroring(t *testing.T) {
	transient := waitReadFailure(waitTarget{Selector: "acme/app#3"}, githubobserver.ErrTransientRetriesExhausted)
	if transient.Status != waitStatusPending {
		t.Errorf("transient read status = %q, want pending so the wait resumes", transient.Status)
	}
	wrapped := waitReadFailure(waitTarget{Selector: "acme/app#3"}, errors.New("read pull request: "+githubobserver.ErrTransientRetriesExhausted.Error()))
	if wrapped.Status != waitStatusPending {
		t.Errorf("wrapped transient read status = %q, want pending", wrapped.Status)
	}
	authoritative := waitReadFailure(waitTarget{Selector: "acme/app#3"}, errors.New("HTTP 404: Not Found"))
	if authoritative.Status != waitStatusError {
		t.Errorf("authoritative failure status = %q, want error", authoritative.Status)
	}
}

func TestWaitPRErroredTargetNeverSettles(t *testing.T) {
	output := waitTestRun(t, waitUntilClosed, []waitReference{waitRef("acme/app#4")},
		map[string]waitTarget{"acme/app#4": {Status: waitStatusError, Reason: "HTTP 404"}},
	)
	if output.Status == waitStatusSettled {
		t.Fatal("a target WB could not read was reported as settled")
	}
}

func TestWaitPRDeduplicatesRepeatedTargets(t *testing.T) {
	targets, err := parseWaitTargets([]string{"acme/app#1", "acme/app#1", "https://github.com/acme/app/pull/1"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1: an agent asking twice must not pay twice; got %+v", len(targets), targets)
	}
}

func TestWaitPRRejectsUnusableInvocations(t *testing.T) {
	for name, args := range map[string][]string{
		"unknown condition":   {"wait", "pr", "acme/app#1", "--until", "whenever"},
		"no targets":          {"wait", "pr"},
		"malformed selector":  {"wait", "pr", "not-a-selector"},
		"interval over slice": {"wait", "pr", "acme/app#1", "--slice", "10s", "--interval", "30s"},
		"zero slice":          {"wait", "pr", "acme/app#1", "--slice", "0"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(args, &stdout, &stderr); code != exitUsage {
				t.Errorf("exit = %d, want %d (usage); stderr = %s", code, exitUsage, stderr.String())
			}
		})
	}
}

// TestWaitPRReadsGitHubForARealTarget proves the default observation path, the
// one that resolves base and head from a bare selector without the caller
// supplying a head SHA.
func TestWaitPRReadsGitHubForARealTarget(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/12$'; then
  echo '{"number":12,"state":"closed","draft":false,"merged":true,"merge_commit_sha":"ccc","mergeable_state":"clean","html_url":"https://example.invalid/12","head":{"ref":"feature","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"base":{"ref":"main","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'check-runs'; then
  echo '{"total_count":1,"check_runs":[{"name":"build","status":"completed","conclusion":"success","app":{"id":1,"slug":"gh"}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status'; then
  echo '{"state":"success","statuses":[]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/branches/main/protection'; then
  echo '{"required_status_checks":{"strict":true,"checks":[]}}'
  exit 0
fi
if [ "$1" = api ]; then echo '{}'; exit 0; fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	code := run([]string{"wait", "pr", "acme/app#12", "--slice", "20s", "--interval", "1s", "--json"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit = %d, want 0; stdout = %s stderr = %s", code, stdout.String(), stderr.String())
	}
	var output waitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("output = %q: %v", stdout.String(), err)
	}
	if output.SchemaVersion != 1 || output.Status != waitStatusSettled {
		t.Fatalf("schema = %d status = %q, want 1/settled", output.SchemaVersion, output.Status)
	}
	if len(output.Targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(output.Targets))
	}
	target := output.Targets[0]
	if target.State != "merged" {
		t.Errorf("state = %q, want merged", target.State)
	}
	// The caller supplied neither a base branch nor a head SHA; WB resolved
	// both. That is the ergonomics defect this verb exists to fix.
	if target.Base != "main" || !strings.HasPrefix(target.Head, "aaaa") {
		t.Errorf("base/head = %q/%q, want main and the resolved head", target.Base, target.Head)
	}
}

func TestWaitPRResumeLineQuotesSelectorsForAShell(t *testing.T) {
	output := waitOutput{Targets: []waitTarget{{Selector: "acme/app#5", Status: waitStatusPending}}}
	args := waitResumeArgs(output, waitUntilChecksSettled, time.Minute, time.Second, true)
	command := newWaitPRCmd()
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	output.ResumeArgs = args
	if err := printWaitOutput(command, output); err != nil {
		t.Fatal(err)
	}
	// '#' starts a shell comment, so an unquoted selector would truncate the
	// resume command at the first target.
	if !strings.Contains(stdout.String(), "'acme/app#5'") {
		t.Errorf("resume line did not quote the selector: %s", stdout.String())
	}
	if !strings.Contains(strings.Join(args, " "), "--json") {
		t.Errorf("resume args dropped --json: %v", args)
	}
}

func TestWaitPRPrintsTheFailingLineNotJustTheCheckName(t *testing.T) {
	target := waitTarget{
		Selector: "acme/app#3",
		Status:   waitStatusSettled,
		Failed:   []string{"Tests"},
		Failures: []orchestrate.CIFailureDetail{{
			Check: "Tests",
			Annotations: []orchestrate.CIFailureAnnotation{{
				Path: "cmd/wb/daemon_file_bridge_test.go", StartLine: 149,
				Message: "old daemon operation count = 0, <nil>",
			}},
		}},
	}
	var out bytes.Buffer
	if err := printWaitFailures(&out, target); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	// The whole reason to wake an agent for a red head is that it arrives
	// knowing the assertion, not a link to go and read one.
	for _, want := range []string{"cmd/wb/daemon_file_bridge_test.go:149", "old daemon operation count = 0"} {
		if !strings.Contains(got, want) {
			t.Errorf("failure output missing %q; got %q", want, got)
		}
	}
}

func TestWaitPRFallsBackToAnExcerptWhenGitHubAnnotatedNothing(t *testing.T) {
	target := waitTarget{
		Failures: []orchestrate.CIFailureDetail{{
			Check:   "Build",
			Excerpt: "undefined: waitForThing\nexit status 2",
		}},
	}
	var out bytes.Buffer
	if err := printWaitFailures(&out, target); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "undefined: waitForThing") {
		t.Errorf("excerpt not surfaced: %q", out.String())
	}
	if !strings.Contains(out.String(), "exit status 2") {
		t.Errorf("excerpt truncated to one line: %q", out.String())
	}
}

func TestWaitPRReportsTheReasonWhenThereIsNeitherAnnotationNorExcerpt(t *testing.T) {
	target := waitTarget{
		Failures: []orchestrate.CIFailureDetail{{
			Check:  "Lint",
			Reason: "GitHub Actions run/job identifiers were not available for this check",
		}},
	}
	var out bytes.Buffer
	if err := printWaitFailures(&out, target); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "identifiers were not available") {
		t.Errorf("reason not surfaced: %q", out.String())
	}
}

func TestWaitPRWarnsBeforeItEatsTheGitHubBudget(t *testing.T) {
	// Eight targets every five seconds is ~23,000 reads an hour against a
	// 5,000 budget: the waiter would starve the work it was waiting for.
	if waitBudgetWarning(8, 5*time.Second) == "" {
		t.Error("no warning for a poll rate that exceeds the whole hourly budget")
	}
	// The motivating case is the seven unattended pull requests. If the shipped
	// default warns on that, the warning is noise rather than signal.
	if warning := waitBudgetWarning(7, defaultWaitInterval); warning != "" {
		t.Errorf("default interval warns on the motivating seven-target case: %s", warning)
	}
	if waitBudgetWarning(0, defaultWaitInterval) != "" {
		t.Error("warned with no targets")
	}
}

// TestWaitPRNamesARequiredCheckNobodyProduces covers the renamed-workflow trap.
// Branch protection requires "build"; the workflow was renamed and now reports
// "build-and-test". Nothing is pending, so counting pending checks alone would
// call this head settled and green — while it can in fact never merge.
func TestWaitPRNamesARequiredCheckNobodyProduces(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/12$'; then
  echo '{"number":12,"state":"open","draft":false,"merged":false,"mergeable_state":"blocked","html_url":"https://example.invalid/12","head":{"ref":"feature","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"base":{"ref":"main","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'check-runs'; then
  echo '{"total_count":1,"check_runs":[{"name":"build-and-test","status":"completed","conclusion":"success","app":{"id":1,"slug":"gh"}}]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status'; then
  echo '{"state":"success","statuses":[]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/rules/branches/'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/branches/main'; then
  echo '{"protected":true,"protection":{"required_status_checks":{"contexts":["build"],"checks":[{"context":"build","app_id":0}]}}}'
  exit 0
fi
if [ "$1" = api ]; then echo '{}'; exit 0; fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	code := run([]string{"wait", "pr", "acme/app#12", "--slice", "5s", "--interval", "1s", "--json"}, &stdout, &stderr)
	var output waitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("exit %d, output = %q: %v", code, stdout.String(), err)
	}
	if len(output.Targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(output.Targets))
	}
	target := output.Targets[0]
	// The wait must terminate: nothing is pending and nothing ever will be.
	if target.Checks["pending"] != 0 {
		t.Fatalf("fixture reported a pending check; the trap is that none is pending: %+v", target.Checks)
	}
	// And it must not call this ready. The required check must be named.
	if len(target.Blocked) == 0 {
		t.Fatalf("a required check nobody produces was not reported; target = %+v", target)
	}
	if target.Blocked[0] != "build" {
		t.Errorf("blocked = %v, want the unsatisfied required check \"build\"", target.Blocked)
	}
}

func TestWaitPRPrintsWhyABlockedHeadCannotMerge(t *testing.T) {
	command := newWaitPRCmd()
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	err := printWaitOutput(command, waitOutput{Targets: []waitTarget{{
		Selector: "acme/app#12", Status: waitStatusSettled, State: "open",
		Checks: map[string]int{"pass": 1}, Blocked: []string{"build"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	got := stdout.String()
	if !strings.Contains(got, "blocked=build") {
		t.Errorf("summary line did not flag the block: %q", got)
	}
	if !strings.Contains(got, "cannot merge until something produces it") {
		t.Errorf("output did not explain the block: %q", got)
	}
}
