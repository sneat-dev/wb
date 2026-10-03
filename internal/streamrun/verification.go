package streamrun

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/ciaudit"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/streamsync"
)

type batchVerifier struct {
	timeout time.Duration
	verify  func(context.Context, string, string, []quality.Check, quality.RunOptions) quality.VerificationReport
}

func (verifier batchVerifier) Verify(ctx context.Context, dir string) (streamsync.VerificationRun, error) {
	started := time.Now()
	verify := verifier.verify
	if verify == nil {
		verify = quality.VerifyWithOptions
	}
	report := verify(ctx, dir, dir, []quality.Check{
		quality.CheckLint, quality.CheckBuild, quality.CheckTest,
	}, quality.RunOptions{
		Timeout: verifier.timeout, SingleWorker: true,
		Env: append(quality.SingleWorkerNodeEnv(), "CI=1"),
	})
	run := streamsync.VerificationRun{
		Passed: report.Status != quality.StatusFailed, Duration: time.Since(started),
		// Single-worker verification runs Go without -race by design, so this
		// claim is printed routinely — and only ever alongside evidence that
		// CI actually carries it.
		Skipped: []string{"-race"},
	}
	commands := make([]string, 0, len(report.Results))
	for _, entry := range report.Results {
		if entry.Status == quality.StatusSkipped {
			continue
		}
		commands = append(commands, entry.Command)
		if entry.Status == quality.StatusFailed {
			run.Details = append(run.Details, fmt.Sprintf("%s %s: %s", entry.Module, entry.Check, entry.Detail))
		}
	}
	run.Command = strings.Join(commands, "; ")
	return run, nil
}

// workflowMechanisms reads which CI mechanisms a member's stream-PR workflows
// actually carry, so "CI owns it" is evidence rather than an assumption.
type workflowMechanisms struct {
	concurrency func(string) ([]ciaudit.Concurrency, error)
	mechanisms  func(string, string) (map[string]bool, bool, error)
}

func (adapter workflowMechanisms) Present(dir string) (map[string]bool, bool, error) {
	concurrency := adapter.concurrency
	if concurrency == nil {
		concurrency = ciaudit.StreamConcurrency
	}
	mechanismsForWorkflow := adapter.mechanisms
	if mechanismsForWorkflow == nil {
		mechanismsForWorkflow = ciaudit.WorkflowMechanismsWithReuse
	}
	workflows, err := concurrency(dir)
	if err != nil {
		return nil, false, err
	}
	present := map[string]bool{}
	opaque := false
	for _, workflow := range workflows {
		if !workflow.PullRequest {
			continue
		}
		mechanisms, reusable, err := mechanismsForWorkflow(dir, workflow.Workflow)
		if err != nil {
			return nil, false, err
		}
		if reusable {
			// A reusable workflow's body is in another repository, so WB
			// cannot prove what it runs.
			opaque = true
		}
		for mechanism := range mechanisms {
			present[mechanism] = true
		}
	}
	return present, opaque, nil
}

// streamEventSink adapts the stream event log to the sync engine.
type streamEventSink struct{ log *streams.FileEventLog }

func (sink streamEventSink) Append(event streamsync.Event) error {
	return sink.log.Append(streams.Event{
		Stream: event.Stream, Verb: event.Verb, Phase: event.Phase,
		Repository: event.Repository, Outcome: event.Outcome,
		Detail: event.Detail, Evidence: event.Evidence,
	})
}
