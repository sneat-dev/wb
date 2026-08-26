package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
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
			{Name: "test", Bucket: "pending"},
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
		"poll 1; checks 1 passed, 1 pending, 0 failed; next poll in 30s",
		"poll 2; checks 2 passed, 0 pending, 0 failed; stable 2/2",
		"ci wait: passed after 2 polls; 2 checks observed",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("progress output missing %q: %q", want, rendered)
		}
	}
}
