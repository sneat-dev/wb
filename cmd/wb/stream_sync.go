package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/ciaudit"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/streamsync"
	"github.com/spf13/cobra"
)

func newStreamSyncCmd() *cobra.Command {
	var (
		format, base, reason string
		libraries            []string
		verify, push         bool
		allowMidReview       bool
		timeout              time.Duration
	)
	command := &cobra.Command{
		Use:   "sync <name>",
		Short: "Rebase the stream onto its base and apply the batch, locally",
		Long: `Bring a stream current and apply its batch — without pushing.

The order is the mechanism, not an implementation detail:

  1. fetch, so the base is live rather than a session-start snapshot
  2. rebase stream/<name> onto origin/<base> — never a merge
  3. rebase every open agent branch onto the new stream head, per branch
  4. THEN compare each library's required version against the target

Step 4 after step 2 is what makes sync idempotent against Renovate: a bump
Renovate already landed is present in the tree after the rebase, so the required
version is already at target and no commit is written. Running sync twice with
nothing else changed produces no new commits the second time.

Bumps are LOCAL commits, one per library, on the stream branch — a bump never
gets its own worktree, its own pull request or an agent. A conflict is reported
per agent branch, naming the branch, its claiming agent and the conflicting
paths; a conflict in one branch never aborts the others.

--verify applies the whole batch and runs the suite ONCE over the resulting
tree. On failure it reverts, then re-applies cumulative prefixes on a local
scratch branch that is never pushed, and names the first failing prefix's last
element as the culprit with the elements already proven good. That costs one run
when the batch passes and 1+k when the culprit is element k — a linear prefix
scan, not a bisection. If every prefix passes the failure came from the base or
a rebased change, and it is reported as an interaction failure.

SYNC NEVER PUSHES. A push costs agent time, tokens, CI minutes and money, so it
happens only on one of four named triggers: landing after a green batch, the
draft stream pull request being made ready for review, park, or an explicit
--push --reason "<text>". A dependency bump is not a trigger. Without one, the
remote is left untouched and the report says so, and 'wb stream status' shows
the local commits accumulating. With a trigger the push is real: --force-with-lease
against the head WB recorded, then the ref is re-read to prove the intended
commit landed.

A failed bump fails the run and the worktree is restored to the state sync found
it in; a version WB cannot compare is reported as version-unreadable rather than
as already-at-target.

Refusals (exit 2):
  review-in-progress  a branch under review would be rebased, invalidating it —
                      --allow-mid-review proceeds with a warning
  dirty-worktree      sync rebases and commits; it will not run over uncommitted work
  unjustified-push    --push without --reason, or a trigger WB does not recognise`,
		Example: `# Bring the stream current and verify the batch once — no push
wb stream sync checkout-rewrite --verify

# Apply specific library targets
wb stream sync checkout-rewrite --library github.com/acme/library/backend@v0.6.0 --verify

# The only escape hatch, and it must say what it is for
wb stream sync checkout-rewrite --push --reason "handing off to the release lane"`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			parsed, err := parseLibraryTargets(libraries)
			if err != nil {
				return &exitError{code: exitUsage, message: err.Error()}
			}
			store, err := streams.Open(projectsRoot)
			if err != nil {
				return err
			}
			stream, err := store.Load(args[0])
			if err != nil {
				return err
			}
			trigger := streamsync.PushTrigger("")
			if push {
				trigger = streamsync.TriggerExplicit
			}

			engine := &streamsync.Engine{
				Git:      streamsync.ExecGit{Timeout: timeout},
				Bumper:   streamsync.ExecBumper{Timeout: timeout},
				Verifier: batchVerifier{timeout: timeout},
				CI:       workflowMechanisms{},
				Events:   streamEventSink{log: store.EventLog(args[0])},
			}

			results := make([]streamsync.Result, 0, len(stream.Members))
			// Every MEMBER, not only the consumers: the library has its own
			// stream/<name> branch and its base moves too. The bumps are
			// consumer-only, but the rebase is not — an empty Libraries set
			// makes the library's bump phase a no-op by itself.
			for _, member := range stream.Members {
				if member.Worktree == "" {
					continue
				}
				memberBase := member.Base
				if base != "" {
					memberBase = base
				}
				memberLibraries := parsed
				if member.Role == streams.RoleLibrary {
					// A library does not bump itself to its own version.
					memberLibraries = nil
				}
				result, syncErr := engine.Sync(command.Context(), streamsync.Options{
					Stream: stream.Name, Worktree: member.Worktree, Repository: member.Repository,
					Branch: member.Branch, Base: memberBase, Libraries: memberLibraries,
					RecordedRemoteHead: member.Lease.RecordedHead,
					Verify:             verify, AllowMidReview: allowMidReview,
					PushTrigger: trigger, PushReason: reason, Timeout: timeout,
				})
				if syncErr != nil {
					var refusal *streamsync.Refusal
					if errors.As(syncErr, &refusal) {
						return &exitError{code: exitUsage, message: member.Repository + ": " + refusal.Error()}
					}
					return syncErr
				}
				results = append(results, result)
			}
			return printStreamSync(command, format, results)
		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().StringVar(&base, "base", "", "branch to rebase onto (default: each member's own base)")
	command.Flags().StringArrayVar(&libraries, "library", nil, "library target as <name>@<version> (repeatable)")
	command.Flags().BoolVar(&verify, "verify", false, "apply the whole batch and run the suite once over the result")
	command.Flags().BoolVar(&allowMidReview, "allow-mid-review", false, "rebase a branch under review anyway, invalidating that review")
	command.Flags().BoolVar(&push, "push", false, "push the stream branch; requires --reason")
	command.Flags().StringVar(&reason, "reason", "", "why this push is justified (required by --push)")
	command.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "maximum duration per external command")
	setDiscoveryTerms(command, "stream sync rebase bump batch verify prefix re-apply local commits push trigger idempotent renovate")
	return command
}

// parseLibraryTargets reads <name>@<version> pairs.
func parseLibraryTargets(values []string) ([]streamsync.Library, error) {
	libraries := make([]streamsync.Library, 0, len(values))
	for _, value := range values {
		name, version, found := strings.Cut(value, "@")
		// A scoped npm package starts with @, so the SEPARATOR is the last @.
		if index := strings.LastIndex(value, "@"); index > 0 {
			name, version, found = value[:index], value[index+1:], true
		}
		if !found || strings.TrimSpace(name) == "" || strings.TrimSpace(version) == "" {
			return nil, fmt.Errorf("--library %q must be <name>@<version>", value)
		}
		ecosystem := string(streams.EcosystemGo)
		if strings.HasPrefix(name, "@") || !strings.Contains(name, "/") || !strings.Contains(name, ".") {
			ecosystem = string(streams.EcosystemNpm)
		}
		libraries = append(libraries, streamsync.Library{Name: name, Target: version, Ecosystem: ecosystem})
	}
	return libraries, nil
}

func printStreamSync(command *cobra.Command, format string, results []streamsync.Result) error {
	failed := false
	for _, result := range results {
		if result.Failed() {
			failed = true
		}
	}
	if format == "json" {
		encoder := json.NewEncoder(command.OutOrStdout())
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(results); err != nil {
			return err
		}
	} else {
		out := command.OutOrStdout()
		for _, result := range results {
			if _, err := fmt.Fprintf(out, "%s on %s\n", result.Repository, result.StreamRebase.Branch); err != nil {
				return err
			}
			for _, rebase := range result.AgentRebases {
				status := "rebased"
				if len(rebase.Conflicts) > 0 {
					status = "CONFLICT: " + strings.Join(rebase.Conflicts, ", ")
				} else if !rebase.Rebased {
					status = "failed: " + rebase.Detail
				}
				if _, err := fmt.Fprintf(out, "  agent %-22s %-10s %s\n", rebase.Branch, rebase.Agent, status); err != nil {
					return err
				}
			}
			for _, bump := range result.Bumps {
				if _, err := fmt.Fprintf(out, "  %-14s %s %s\n", bump.Action, bump.Library.Name, bump.Library.Target); err != nil {
					return err
				}
			}
			if result.Batch != nil {
				if err := printBatch(out, *result.Batch); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(out, "  %s\n", result.Unpushed.String()); err != nil {
				return err
			}
			if result.PushSkipped != "" {
				if _, err := fmt.Fprintf(out, "  %s\n", result.PushSkipped); err != nil {
					return err
				}
			}
			if result.Push != nil {
				if _, err := fmt.Fprintf(out, "  pushed %s: trigger=%s reason=%s\n", result.Push.SHA, result.Push.Trigger, result.Push.Reason); err != nil {
					return err
				}
			}
			for _, failure := range result.Errors {
				if _, err := fmt.Fprintf(out, "  ! %s\n", failure); err != nil {
					return err
				}
			}
		}
	}
	if failed {
		return &exitError{code: exitFindings, message: "stream sync reported findings; see the report above"}
	}
	return nil
}

func printBatch(out interface{ Write([]byte) (int, error) }, batch streamsync.BatchResult) error {
	verdict := "passed"
	if !batch.Passed {
		verdict = "FAILED"
	}
	if _, err := fmt.Fprintf(out, "  batch %s in %d run(s) over %d element(s)\n", verdict, batch.Runs, len(batch.Elements)); err != nil {
		return err
	}
	if batch.Culprit != nil {
		if _, err := fmt.Fprintf(out, "    culprit: %s (%s)\n", batch.Culprit.Name, batch.FailingCheck); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "    proven good: %d element(s) before it\n", len(batch.ProvenGood)); err != nil {
			return err
		}
	}
	if batch.InteractionFailure {
		if _, err := fmt.Fprintf(out, "    interaction failure: every prefix passed, so the failure is in the base or a rebased change\n"); err != nil {
			return err
		}
	}
	for _, skipped := range batch.Skipped {
		if _, err := fmt.Fprintf(out, "    skipped: %s\n", skipped); err != nil {
			return err
		}
	}
	for _, unguarded := range batch.Unguarded {
		if _, err := fmt.Fprintf(out, "    ! UNGUARDED: %s — neither this run nor CI carries it\n", unguarded); err != nil {
			return err
		}
	}
	for _, unverified := range batch.Unverified {
		if _, err := fmt.Fprintf(out, "    ? UNVERIFIED: %s — WB could not tell whether CI runs it\n", unverified); err != nil {
			return err
		}
	}
	if batch.UnexaminedElements > 0 {
		if _, err := fmt.Fprintf(out,
			"    the scan stops at the first failing prefix, so %d element(s) after the culprit were never examined\n",
			batch.UnexaminedElements); err != nil {
			return err
		}
	}
	return nil
}

// batchVerifier runs the existing wb verify profiles, single-worker, and
// names the mechanisms it did not run so they can be checked against CI.
type batchVerifier struct{ timeout time.Duration }

func (verifier batchVerifier) Verify(ctx context.Context, dir string) (streamsync.VerificationRun, error) {
	started := time.Now()
	report := quality.VerifyWithOptions(ctx, dir, dir, []quality.Check{
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
type workflowMechanisms struct{}

func (workflowMechanisms) Present(dir string) (map[string]bool, bool, error) {
	workflows, err := ciaudit.StreamConcurrency(dir)
	if err != nil {
		return nil, false, err
	}
	present := map[string]bool{}
	opaque := false
	for _, workflow := range workflows {
		if !workflow.PullRequest {
			continue
		}
		mechanisms, reusable, err := ciaudit.WorkflowMechanismsWithReuse(dir, workflow.Workflow)
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
