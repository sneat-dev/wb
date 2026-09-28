package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
	progresspkg "github.com/sneat-dev/wb/internal/progress"
)

func TestCIWaitProgressShowsPollAndCheckState(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newCIWaitProgress(&out, true)
	progress.start("acme/app", "42", "main", ciWaitHead)
	progress.report(orchestrate.PullRequestWaitProgress{
		Observation: 1,
		NextPoll:    30 * time.Second,
		Result: orchestrate.PullRequestWaitResult{Checks: []orchestrate.RemoteCheck{
			{Name: "lint", Bucket: "pass"},
			{Name: "check-run:test", Bucket: "pending"},
			{Name: "check-run:integration", Bucket: "pending"},
			{Name: "check-run:package", Bucket: "pending"},
			{Name: "check-run:release", Bucket: "pending"},
		}},
	})
	progress.report(orchestrate.PullRequestWaitProgress{
		Observation: 2,
		Result: orchestrate.PullRequestWaitResult{StableObservations: 2, Checks: []orchestrate.RemoteCheck{
			{Name: "lint", Bucket: "pass"},
			{Name: "test", Bucket: "pass"},
		}},
	})
	progress.finish(orchestrate.PullRequestWaitResult{Status: orchestrate.PullRequestWaitPassed, Checks: make([]orchestrate.RemoteCheck, 2)})

	rendered := out.String()
	for _, want := range []string{
		"ci wait: observing acme/app PR 42 → main@012345678901",
		"poll 1; checks 1/5 completed; running: test, integration, package, +1 more; 4 pending; next poll in 30s",
		"poll 2; checks 2/2 completed; stable 2/2",
		"ci wait: passed after 2 polls; 2 checks observed",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("progress output missing %q: %q", want, rendered)
		}
	}
}

// TestCIWaitProgressFailReportsTheUnderlyingError proves fail's message
// formatting: the only way `ci wait` ever produces a non-nil error, from
// waitForCommitChecks's own upfront usage checks (an empty repository/target/
// head, or a bad slice/interval), is guarded identically by Args' own
// validateCIWaitInputs before RunE ever runs, so the CLI itself cannot drive
// this branch — it is exercised here directly, the way it would need to be
// reached if that duplicated Args guard were ever narrowed.
func TestCIWaitProgressFailReportsTheUnderlyingError(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newCIWaitProgress(&out, true)
	progress.fail(errors.New("check wait slice must be positive and at most 9m0s"))
	rendered := out.String()
	if !strings.Contains(rendered, "ci wait: failed: check wait slice must be positive and at most 9m0s") {
		t.Fatalf("fail did not report the underlying error: %q", rendered)
	}
}

// TestCIWaitProgressFailOmitsTrailingColonForABlankError proves fail never
// prints a dangling ": " when the error carries no message.
func TestCIWaitProgressFailOmitsTrailingColonForABlankError(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newCIWaitProgress(&out, true)
	progress.fail(errors.New(""))
	rendered := out.String()
	if !strings.Contains(rendered, "ci wait: failed") {
		t.Fatalf("fail did not report anything: %q", rendered)
	}
	if strings.Contains(rendered, "ci wait: failed:") {
		t.Fatalf("fail printed a dangling colon for a blank error: %q", rendered)
	}
}

func TestCIWaitOperationProgressUsesCallerLabel(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newCIWaitProgress(&out, true)
	progress.operationReporter("ci wait")(progresspkg.Event{
		Phase: "retry", Detail: "attempt 1/4 failed: HTTP 502; retrying in 250ms", State: progresspkg.Waiting,
	})
	progress.finishOperation("ci wait: complete")

	rendered := out.String()
	if !strings.Contains(rendered, "ci wait: retry: attempt 1/4 failed: HTTP 502; retrying in 250ms: waiting") {
		t.Fatalf("operation progress used the wrong label: %q", rendered)
	}
	if strings.Contains(rendered, "pr land") {
		t.Fatalf("CI wait progress leaked PR-land label: %q", rendered)
	}
}
