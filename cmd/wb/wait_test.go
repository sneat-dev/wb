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
	"github.com/sneat-dev/wb/internal/waitregistry"
	"github.com/sneat-dev/wb/internal/wbhome"
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

// TestWaitPRShortensTheFinalDelayToFitWithinTheRemainingSlice pins
// observeWaitTargets' own delay-shrinking branch directly: when the
// configured poll Interval is longer than the time left before the bounded
// slice's deadline, the pause before the next observation must be cut down
// to the remaining time, not the full interval, so the loop can still end
// (settled or not) at the deadline it was given.
func TestWaitPRShortensTheFinalDelayToFitWithinTheRemainingSlice(t *testing.T) {
	targets := []waitReference{waitRef("acme/app#1")}
	clock := time.Unix(0, 0)
	var observedDelay time.Duration
	output := observeWaitTargets(context.Background(), waitRun{
		Targets:   targets,
		Condition: waitUntilChecksSettled,
		Slice:     2 * time.Second,
		Interval:  10 * time.Second, // longer than the remaining slice below
		observe: func(_ context.Context, reference waitReference) waitTarget {
			return waitTarget{Selector: reference.Selector, State: "open", Checks: map[string]int{"pending": 1}}
		},
		now: func() time.Time { return clock },
		sleep: func(_ context.Context, delay time.Duration) error {
			observedDelay = delay
			clock = clock.Add(time.Hour) // ends the loop on the next iteration
			return nil
		},
	})
	if output.Status != waitStatusPending {
		t.Fatalf("status = %q, want pending", output.Status)
	}
	if observedDelay != 2*time.Second {
		t.Fatalf("pause delay = %s, want it shortened to the remaining slice (2s), not the full interval (10s)", observedDelay)
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
	command := newWaitPRCmd(&invocation{})
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
	command := newWaitPRCmd(&invocation{})
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

// TestWaitPRKeepsIdentityFieldsWhenOnlyChecksReadFails pins the round-3
// regression: State/Head/Base/URL/Mergeable/Draft come from a successful
// pull-request read, a fact independent of whether a later checks read on
// that same open pull request fails. Before prsnapshot.Observe, `wait.go`
// carried those fields into waitReadFailure's target; the first cut of the
// prsnapshot move built a fresh, empty target and returned it straight from
// waitReadFailure without ever copying them over. This proves the `--json`
// output for that case still reports the pull request's own identity, not a
// blank one.
func TestWaitPRKeepsIdentityFieldsWhenOnlyChecksReadFails(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/13$'; then
  echo '{"number":13,"state":"open","draft":true,"merged":false,"mergeable_state":"clean","html_url":"https://example.invalid/13","head":{"ref":"feature","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"base":{"ref":"main","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'check-runs'; then
  echo 'gh: not found (HTTP 404)' >&2
  exit 1
fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	code := run([]string{"wait", "pr", "acme/app#13", "--slice", "300ms", "--interval", "100ms", "--json"}, &stdout, &stderr)
	if code == exitOK {
		t.Fatalf("exit = %d, want a non-settled exit: the checks read never succeeds; stdout = %s", code, stdout.String())
	}
	var output waitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("output = %q: %v", stdout.String(), err)
	}
	if len(output.Targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(output.Targets))
	}
	target := output.Targets[0]
	if target.Status != waitStatusError {
		t.Fatalf("status = %q, want error (a 404 is not transient): %+v", target.Status, target)
	}
	if target.State != "open" || !target.Draft {
		t.Errorf("State/Draft = %q/%t, want open/true: identity fields must survive a checks-read failure", target.State, target.Draft)
	}
	if target.Head != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || target.Base != "main" {
		t.Errorf("Head/Base = %q/%q, want the real observed values, not blank", target.Head, target.Base)
	}
	if target.URL != "https://example.invalid/13" || target.Mergeable != "clean" {
		t.Errorf("URL/Mergeable = %q/%q, want the real observed values, not blank", target.URL, target.Mergeable)
	}
}

// TestWaitPRChangedIgnoresAHeadThatWasNeverReallyBlank is the --until
// changed half of the round-3 regression: a first observation that hits a
// checks-read failure on an open pull request must still carry the real
// head, so a later, successful observation of the identical head is not
// mistaken for a change. Before the fix, the first observation's blank Head
// field would differ from the second's real one and stop the wait on a
// change that never actually happened.
func TestWaitPRChangedIgnoresAHeadThatWasNeverReallyBlank(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "check-runs-calls")
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/pulls/14$'; then
  echo '{"number":14,"state":"open","draft":false,"merged":false,"mergeable_state":"clean","html_url":"https://example.invalid/14","head":{"ref":"feature","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"base":{"ref":"main","sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'check-runs'; then
  count=0
  if [ -f "` + state + `" ]; then count=$(cat "` + state + `"); fi
  count=$((count + 1))
  printf '%s' "$count" > "` + state + `"
  if [ "$count" = "1" ]; then
    echo 'gh: not found (HTTP 404)' >&2
    exit 1
  fi
  echo '{"total_count":0,"check_runs":[]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/status'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/branches/main' ]; then
  echo '{"protected":false,"protection":{}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q '/rules/branches/'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ]; then echo '{}'; exit 0; fi
echo "unexpected gh args: $*" >&2
exit 30
`
	writeCIWaitExecutable(t, filepath.Join(bin, "gh"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	code := run([]string{"wait", "pr", "acme/app#14", "--until", "changed", "--slice", "600ms", "--interval", "150ms", "--json"}, &stdout, &stderr)
	if code != exitFindings {
		t.Fatalf("exit = %d, want %d (pending): the pull request's own facts never actually changed; stdout = %s stderr = %s", code, exitFindings, stdout.String(), stderr.String())
	}
	var output waitOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("output = %q: %v", stdout.String(), err)
	}
	if output.Status != waitStatusPending {
		t.Fatalf("status = %q, want pending: a resolved checks read on an unchanged head is not a change", output.Status)
	}
	if len(output.Targets) != 1 || output.Targets[0].Status != waitStatusPending {
		t.Fatalf("targets = %+v, want the single target still pending", output.Targets)
	}
}

func TestWaitListTellsAQuietSessionApartFromAStoppedOne(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WB_PROJECTS_ROOT", home)
	previous := waitregistry.Alive
	waitregistry.Alive = func(int) bool { return true }
	t.Cleanup(func() { waitregistry.Alive = previous })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"wait", "list"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no outstanding waits") {
		t.Errorf("empty registry did not say so: %q", stdout.String())
	}
}

func TestWaitListReportsAStaleWaiterInJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WB_PROJECTS_ROOT", home)
	root, err := wbhome.EnsureRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waitregistry.Register(root, waitregistry.Record{
		ID: "x", PID: 4242, Kind: "pr", Targets: []string{"acme/app#9"},
		Until: "checks-settled", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	previous := waitregistry.Alive
	waitregistry.Alive = func(int) bool { return false }
	t.Cleanup(func() { waitregistry.Alive = previous })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"wait", "list", "--json"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	var payload struct {
		Waits []waitregistry.Record `json:"waits"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("output = %q: %v", stdout.String(), err)
	}
	if len(payload.Waits) != 1 || !payload.Waits[0].Stale {
		t.Fatalf("a dead waiter was not surfaced: %+v", payload.Waits)
	}
}
