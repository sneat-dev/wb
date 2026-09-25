package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/prsnapshot"
	"github.com/sneat-dev/wb/internal/waitregistry"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// Waiting is delegated to WB so an agent does not have to hold "check this
// later" in its own context. The verb is deliberately report-only: it never
// merges, so it needs no landing lane, no review evidence, and no authority
// over the target. `wb pr land` remains the verb that completes a pull request.
const (
	defaultWaitSlice    = 8 * time.Minute
	defaultWaitInterval = 60 * time.Second
)

// Each observed target costs roughly four GitHub reads per poll — the pull
// request, its head's check runs, its head's commit statuses, and the target
// branch's required checks. The default interval is therefore chosen against
// the authenticated API budget rather than against responsiveness: eight
// targets at 45s is about 2,500 reads an hour out of 5,000.
const (
	waitReadsPerTargetPerPoll = 4
	// GitHub's authenticated budget is 5,000 reads an hour and WB shares it
	// with every other command. A waiter may take up to about two fifths of it
	// before it has to say so: the motivating case is seven pull requests at
	// the default interval (~1,700 reads an hour), and a warning that fires on
	// the case the verb exists for is noise. Silently consuming the budget an
	// agent needs for its actual work would move the cost rather than remove it.
	waitHourlyReadBudget = 2000
)

// waitBudgetWarning reports the projected hourly GitHub read cost when it
// exceeds what one waiter should quietly take, so the operator can widen
// --interval instead of discovering the throttle later.
func waitBudgetWarning(targets int, interval time.Duration) string {
	if targets <= 0 || interval <= 0 {
		return ""
	}
	perHour := float64(targets*waitReadsPerTargetPerPoll) * (float64(time.Hour) / float64(interval))
	if perHour <= waitHourlyReadBudget {
		return ""
	}
	return fmt.Sprintf("%d target(s) every %s is about %.0f GitHub reads an hour out of 5000; widen --interval to spend less",
		targets, interval.Round(time.Second), perHour)
}

type waitCondition string

const (
	waitUntilChecksSettled waitCondition = "checks-settled"
	waitUntilChanged       waitCondition = "changed"
	waitUntilClosed        waitCondition = "closed"
)

func waitConditions() []string {
	return []string{string(waitUntilChecksSettled), string(waitUntilChanged), string(waitUntilClosed)}
}

type waitOutput struct {
	SchemaVersion int          `json:"schema_version"`
	ObservedAt    time.Time    `json:"observed_at"`
	Until         string       `json:"until"`
	Status        string       `json:"status"`
	Observations  int          `json:"observations"`
	Targets       []waitTarget `json:"targets"`
	ResumeArgs    []string     `json:"resume_args,omitempty"`
}

type waitTarget struct {
	Selector   string                        `json:"selector"`
	Repository string                        `json:"repository"`
	Number     string                        `json:"number"`
	Status     string                        `json:"status"`
	State      string                        `json:"state,omitempty"`
	Draft      bool                          `json:"draft,omitempty"`
	Head       string                        `json:"head,omitempty"`
	Base       string                        `json:"base,omitempty"`
	Mergeable  string                        `json:"mergeable,omitempty"`
	Checks     map[string]int                `json:"checks,omitempty"`
	Failed     []string                      `json:"failed_checks,omitempty"`
	Failures   []orchestrate.CIFailureDetail `json:"failures,omitempty"`
	Blocked    []string                      `json:"unsatisfied_required_checks,omitempty"`
	Reason     string                        `json:"reason,omitempty"`
	URL        string                        `json:"url,omitempty"`
}

// waitTargetStatus values. "settled" means the requested condition holds;
// "pending" means it does not yet. "error" is a read that failed in a way a
// retry is unlikely to fix — a transient read stays pending on purpose, so a
// GitHub blip resumes instead of ending the wait with a false verdict.
const (
	waitStatusSettled = "settled"
	waitStatusPending = "pending"
	waitStatusError   = "error"
)

func newWaitCmd(inv *invocation) *cobra.Command {
	command := &cobra.Command{
		Use:   "wait",
		Short: "Wait for something WB can observe, then report what changed",
		Long: `Wait for a condition WB already knows how to observe.

Waiting belongs to WB rather than to an agent's context. Run one of these as a
background command; when it exits, the harness resumes the agent with the
result, so nothing has to remember "check this later".

Every wait is bounded and terminating. Pending is a first-class result: it
exits 1 carrying exact resume arguments for the targets that are still
pending, so the next invocation observes only what is left.

The argument after ` + "`wait`" + ` names a thing, not a domain: a pull request, an
exact commit's checks, an agent run, an operation.

Three of these are the verb-first spelling of commands WB already had. They run
the identical implementation, not a copy, so a receipt produced either way is
the same receipt:

  wb wait checks     is  wb ci wait
  wb wait agent      is  wb agent await
  wb wait operation  is  wb daemon operation wait

The older spellings keep working.

These commands report. They never merge, publish, or change a target. Use
` + "`wb pr land`" + ` to wait for checks and then land a pull request.`,
	}
	command.AddCommand(newWaitPRCmd(inv))
	command.AddCommand(newWaitChecksCmd(inv))
	command.AddCommand(newWaitAgentCmd(inv))
	command.AddCommand(newWaitOperationCmd(inv))
	command.AddCommand(newWaitListCmd(inv))
	return command
}

func newWaitPRCmd(inv *invocation) *cobra.Command {
	var until string
	var slice, interval time.Duration
	var jsonOut bool
	command := &cobra.Command{
		Use:   "pr <owner/repository#number>...",
		Short: "Wait for one or more pull requests to reach a reportable state",
		Long: `Observe pull requests until every one of them reaches the requested
condition, or the bounded slice ends.

Targets are named the way an agent already holds them — owner/repository#number,
or a pull request URL. WB resolves each target's base branch and exact head
itself; no head SHA has to be discovered first.

Conditions:

  checks-settled  every check on the head has finished, whether it passed or
                  failed, or the pull request is no longer open (default)
  changed         the head commit, open/closed state, draft flag, or the set of
                  check outcomes differs from the first observation
  closed          the pull request is merged or closed

The wait ends when EVERY target satisfies the condition, not the first one. A
first-past-the-post wait would return as soon as the noisiest target moved and
starve the quiet target that actually needed attention.

A transient GitHub read keeps its target pending rather than failing the wait,
so a provider blip resumes instead of reporting a verdict WB did not observe.

This command only reports. It never merges. Landing a pull request when its
checks pass is ` + "`wb pr land <owner/repository#number>`" + `, which waits and
then lands in one call.`,
		Example: `# Wait for one pull request's checks to finish
wb wait pr sneat-dev/wb#581

# Watch several at once and report every one that moved
wb wait pr sneat-dev/wb#581 sneat-co/backstage#491 --until changed

# Run it as a background command and let the harness resume the agent
wb wait pr sneat-dev/wb#581 --slice 8m --json`,
		Args: func(command *cobra.Command, args []string) error {
			if err := cobra.MinimumNArgs(1)(command, args); err != nil {
				return err
			}
			if _, err := parseWaitCondition(until); err != nil {
				return err
			}
			if err := validateWaitBounds(slice, interval); err != nil {
				return err
			}
			_, err := parseWaitTargets(args)
			return err
		},
		RunE: func(command *cobra.Command, args []string) error {
			condition, err := parseWaitCondition(until)
			if err != nil {
				return usageError(err.Error())
			}
			targets, err := parseWaitTargets(args)
			if err != nil {
				return usageError(err.Error())
			}
			// A delegated wait that nobody can see is the same as no wait: the
			// session goes quiet and looks stopped. Record it before waiting,
			// and clear it however this call ends.
			release := registerWait(inv, "pr", targets, string(condition), slice)
			defer release()
			interactive := console.Interactive(command.ErrOrStderr(), inv.nonInteractive)
			progress := newLiveProgress(progressOutput(command.ErrOrStderr(), interactive), true)
			progress.start(fmt.Sprintf("wait pr: %d target(s) until %s", len(targets), condition))
			if warning := waitBudgetWarning(len(targets), interval); warning != "" {
				if _, err := fmt.Fprintln(command.ErrOrStderr(), "wait pr: "+warning); err != nil {
					return err
				}
			}
			output := observeWaitTargets(command.Context(), waitRun{
				Targets:   targets,
				Condition: condition,
				Slice:     slice,
				Interval:  interval,
				Progress:  progress.update,
			})
			progress.finish(fmt.Sprintf("wait pr: %s after %d observation(s)", output.Status, output.Observations))
			if output.Status == waitStatusPending {
				output.ResumeArgs = waitResumeArgs(output, condition, slice, interval, jsonOut)
			}
			if jsonOut {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(output); err != nil {
					return err
				}
			} else if err := printWaitOutput(command, output); err != nil {
				return err
			}
			if output.Status != waitStatusSettled {
				return &exitError{code: exitFindings, message: "wait pr " + output.Status}
			}
			return nil
		},
	}
	command.Flags().StringVar(&until, "until", string(waitUntilChecksSettled), "condition to wait for: "+strings.Join(waitConditions(), ", "))
	command.Flags().DurationVar(&slice, "slice", defaultWaitSlice, "maximum observation slice; pending exits 1 with resume arguments")
	command.Flags().DurationVar(&interval, "interval", defaultWaitInterval, "interval between observations")
	addJSONFormatFlags(command, &jsonOut)
	setDiscoveryTerms(command, "wait poll watch block pull request pr checks green settled changed closed merged review background resume pending harness")
	return command
}

func parseWaitCondition(value string) (waitCondition, error) {
	switch condition := waitCondition(strings.TrimSpace(value)); condition {
	case waitUntilChecksSettled, waitUntilChanged, waitUntilClosed:
		return condition, nil
	default:
		return "", fmt.Errorf("unsupported --until %q; use %s", value, strings.Join(waitConditions(), ", "))
	}
}

func validateWaitBounds(slice, interval time.Duration) error {
	if slice <= 0 {
		return fmt.Errorf("--slice must be positive")
	}
	if interval <= 0 {
		return fmt.Errorf("--interval must be positive")
	}
	if interval > slice {
		return fmt.Errorf("--interval must not exceed --slice, or WB could never observe twice")
	}
	return nil
}

type waitReference struct {
	Selector   string
	Repository string
	Number     string
}

func parseWaitTargets(args []string) ([]waitReference, error) {
	seen := map[string]bool{}
	targets := make([]waitReference, 0, len(args))
	for _, argument := range args {
		repository, number, err := splitPullRequestSelector(argument)
		if err != nil {
			return nil, err
		}
		key := repository + "#" + number
		// A duplicate target is the failure this verb exists to remove: an
		// agent that asks twice should not pay twice.
		if seen[key] {
			continue
		}
		seen[key] = true
		targets = append(targets, waitReference{Selector: key, Repository: repository, Number: number})
	}
	return targets, nil
}

type waitRun struct {
	Targets   []waitReference
	Condition waitCondition
	Slice     time.Duration
	Interval  time.Duration
	Progress  func(string)
	observe   func(context.Context, waitReference) waitTarget
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
}

func (run waitRun) observation() func(context.Context, waitReference) waitTarget {
	if run.observe != nil {
		return run.observe
	}
	return observePullRequest
}

func (run waitRun) clock() func() time.Time {
	if run.now != nil {
		return run.now
	}
	return time.Now
}

func (run waitRun) pause() func(context.Context, time.Duration) error {
	if run.sleep != nil {
		return run.sleep
	}
	return func(ctx context.Context, delay time.Duration) error {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
}

// observeWaitTargets runs one bounded slice. It returns settled only when every
// target satisfies the condition, so a burst of activity on one target cannot
// end the wait while another is still unobserved.
func observeWaitTargets(ctx context.Context, run waitRun) waitOutput {
	observe, now, pause := run.observation(), run.clock(), run.pause()
	deadline := now().Add(run.Slice)
	first := map[string]waitTarget{}
	latest := map[string]waitTarget{}
	observations := 0
	for {
		observations++
		for _, target := range run.Targets {
			if settled(latest[target.Selector]) {
				continue
			}
			observed := observe(ctx, target)
			if _, ok := first[target.Selector]; !ok {
				first[target.Selector] = observed
			}
			latest[target.Selector] = decideWaitTarget(observed, first[target.Selector], run.Condition)
		}
		output := collectWaitOutput(run, latest, observations, now())
		if output.Status == waitStatusSettled {
			return output
		}
		remaining := time.Until(deadline)
		if run.now != nil {
			remaining = deadline.Sub(now())
		}
		if remaining <= 0 {
			return output
		}
		if run.Progress != nil {
			run.Progress(fmt.Sprintf("wait pr: observation %d; %d/%d settled; next in %s",
				observations, countSettled(latest), len(run.Targets), run.Interval.Round(time.Second)))
		}
		delay := run.Interval
		if remaining < delay {
			delay = remaining
		}
		if err := pause(ctx, delay); err != nil {
			return collectWaitOutput(run, latest, observations, now())
		}
	}
}

func settled(target waitTarget) bool { return target.Status == waitStatusSettled }

func countSettled(latest map[string]waitTarget) int {
	count := 0
	for _, target := range latest {
		if settled(target) {
			count++
		}
	}
	return count
}

func collectWaitOutput(run waitRun, latest map[string]waitTarget, observations int, at time.Time) waitOutput {
	output := waitOutput{
		SchemaVersion: 1,
		ObservedAt:    at.UTC(),
		Until:         string(run.Condition),
		Status:        waitStatusSettled,
		Observations:  observations,
		Targets:       make([]waitTarget, 0, len(run.Targets)),
	}
	for _, target := range run.Targets {
		observed := latest[target.Selector]
		if observed.Selector == "" {
			observed = waitTarget{Selector: target.Selector, Repository: target.Repository, Number: target.Number, Status: waitStatusPending}
		}
		if observed.Status != waitStatusSettled {
			output.Status = waitStatusPending
		}
		output.Targets = append(output.Targets, observed)
	}
	return output
}

// decideWaitTarget turns one observation into a verdict for the requested
// condition. An errored read is reported but never settles the target: WB does
// not claim an outcome it could not observe.
func decideWaitTarget(observed, first waitTarget, condition waitCondition) waitTarget {
	if observed.Status == waitStatusError {
		return observed
	}
	closed := observed.State != "" && !strings.EqualFold(observed.State, "open")
	switch condition {
	case waitUntilClosed:
		observed.Status = waitStatusPending
		if closed {
			observed.Status = waitStatusSettled
		}
	case waitUntilChanged:
		observed.Status = waitStatusPending
		if waitTargetMoved(first, observed) {
			observed.Status = waitStatusSettled
		}
	default:
		observed.Status = waitStatusPending
		if closed || (observed.Checks != nil && observed.Checks["pending"] == 0) {
			observed.Status = waitStatusSettled
		}
	}
	if observed.Status == waitStatusPending && observed.Reason == "" {
		observed.Reason = waitPendingReason(observed, condition)
	}
	return observed
}

func waitPendingReason(observed waitTarget, condition waitCondition) string {
	switch condition {
	case waitUntilClosed:
		return "still open"
	case waitUntilChanged:
		return "unchanged since first observation"
	default:
		if observed.Checks == nil {
			return "no checks observed yet"
		}
		return fmt.Sprintf("%d check(s) still running", observed.Checks["pending"])
	}
}

// waitTargetMoved compares only facts an agent would act on. Check counts are
// compared as a whole so a run flipping from pending to failed counts as a
// change, while a re-reported identical set does not.
func waitTargetMoved(first, latest waitTarget) bool {
	if first.Head != latest.Head || first.State != latest.State || first.Draft != latest.Draft {
		return true
	}
	if first.Mergeable != latest.Mergeable {
		return true
	}
	return waitChecksKey(first.Checks) != waitChecksKey(latest.Checks)
}

func waitChecksKey(checks map[string]int) string {
	if len(checks) == 0 {
		return ""
	}
	buckets := make([]string, 0, len(checks))
	for bucket, count := range checks {
		buckets = append(buckets, fmt.Sprintf("%s=%d", bucket, count))
	}
	sort.Strings(buckets)
	return strings.Join(buckets, ",")
}

// observePullRequest reads one pull request and the checks on its exact head.
// It delegates to prsnapshot.Observe — one shared implementation of both
// facts, reused by the herdr-session-transport daemon watcher
// (internal/prwatch) rather than a second dialect of the same GitHub reads —
// and adapts the result onto waitTarget so `wb wait pr`'s own behavior is
// unchanged by the move (see cmd/wb/wait_test.go).
func observePullRequest(ctx context.Context, reference waitReference) waitTarget {
	target := waitTarget{Selector: reference.Selector, Repository: reference.Repository, Number: reference.Number}
	snapshot := prsnapshot.Observe(ctx, reference.Repository, reference.Number)
	// prsnapshot.Observe fills in State/Draft/Head/Base/URL/Mergeable
	// whenever the pull-request read itself succeeded, even when a later
	// checks read failed on an open pull request and Err is set: the pull
	// request's own identity is real, known information, not something a
	// caller should lose because a different, later read failed. Copying
	// these fields before checking Err — not after — is exactly what
	// restores this verb's pre-prsnapshot behavior: `--json` keeps every
	// field it always reported for this case, and the first observation
	// `--until changed` compares later ticks against carries the real head
	// rather than an empty one that would otherwise register as a false
	// "changed" the moment a following, successful read reports it (round 3
	// review of herdr-session-transport PR #657: a serious regression).
	target.State = snapshot.State
	if snapshot.Merged {
		target.State = "merged"
	}
	target.Draft = snapshot.Draft
	target.Head = snapshot.Head
	target.Base = snapshot.Base
	target.URL = snapshot.URL
	target.Mergeable = snapshot.Mergeable
	if snapshot.Err != nil {
		return waitReadFailure(target, snapshot.Err)
	}
	target.Checks = snapshot.Checks
	target.Failed = snapshot.Failed
	target.Failures = snapshot.Failures
	target.Blocked = snapshot.Blocked
	return target
}

// waitReadFailure keeps a transient provider failure pending. Only a failure
// WB cannot classify as transient ends the target as an error.
func waitReadFailure(target waitTarget, err error) waitTarget {
	target.Reason = err.Error()
	target.Status = waitStatusError
	if orchestrate.IsTransientReadFailure(err) {
		target.Status = waitStatusPending
	}
	return target
}

func waitResumeArgs(output waitOutput, condition waitCondition, slice, interval time.Duration, jsonOut bool) []string {
	args := []string{"wb", "wait", "pr"}
	for _, target := range output.Targets {
		if target.Status != waitStatusSettled {
			args = append(args, target.Selector)
		}
	}
	args = append(args, "--until", string(condition), "--slice", slice.String(), "--interval", interval.String())
	if jsonOut {
		args = append(args, "--json")
	}
	return args
}

// printWaitFailures prints why a target is red, closest-to-the-cause first: a
// file and line if GitHub annotated one, otherwise a bounded log excerpt. This
// is the whole point of waking an agent for a failure rather than a link.
func printWaitFailures(out io.Writer, target waitTarget) error {
	for _, failure := range target.Failures {
		for _, annotation := range failure.Annotations {
			location := annotation.Path
			if annotation.StartLine > 0 {
				location = fmt.Sprintf("%s:%d", annotation.Path, annotation.StartLine)
			}
			if _, err := fmt.Fprintf(out, "  %s: %s: %s\n", failure.Check, location, annotation.Message); err != nil {
				return err
			}
		}
		if len(failure.Annotations) == 0 {
			detail := failure.Excerpt
			if strings.TrimSpace(detail) == "" {
				detail = failure.Reason
			}
			if strings.TrimSpace(detail) == "" {
				continue
			}
			for _, excerptLine := range strings.Split(strings.TrimRight(detail, "\n"), "\n") {
				if _, err := fmt.Fprintf(out, "  %s: %s\n", failure.Check, excerptLine); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func printWaitOutput(command *cobra.Command, output waitOutput) error {
	out := command.OutOrStdout()
	for _, target := range output.Targets {
		line := fmt.Sprintf("%s %s", target.Selector, target.Status)
		if target.State != "" {
			line += " state=" + target.State
		}
		if len(target.Checks) > 0 {
			line += " checks=" + waitChecksKey(target.Checks)
		}
		if len(target.Failed) > 0 {
			line += " failed=" + strings.Join(target.Failed, ",")
		}
		if len(target.Blocked) > 0 {
			line += " blocked=" + strings.Join(target.Blocked, ",")
		}
		if target.Reason != "" {
			line += " (" + target.Reason + ")"
		}
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
		if err := printWaitFailures(out, target); err != nil {
			return err
		}
		for _, missing := range target.Blocked {
			if _, err := fmt.Fprintf(out, "  required check %q has no passing result on this head; it cannot merge until something produces it\n", missing); err != nil {
				return err
			}
		}
	}
	if len(output.ResumeArgs) == 0 {
		return nil
	}
	quoted := make([]string, 0, len(output.ResumeArgs))
	for _, argument := range output.ResumeArgs {
		quoted = append(quoted, shellQuoteCIWaitArg(argument))
	}
	_, err := fmt.Fprintf(out, "resume: %s\n", strings.Join(quoted, " "))
	return err
}

// registerWait records an outstanding wait and returns its release. Every
// failure here is non-fatal by design: losing visibility of a wait must never
// stop the wait, which is the thing the caller actually asked for.
func registerWait(inv *invocation, kind string, targets []waitReference, until string, slice time.Duration) func() {
	home, err := wbhome.EnsureRoot(inv.projectsRoot)
	if err != nil {
		return func() {}
	}
	selectors := make([]string, 0, len(targets))
	for _, target := range targets {
		selectors = append(selectors, target.Selector)
	}
	record := waitregistry.Record{
		ID:         fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano()),
		PID:        os.Getpid(),
		Kind:       kind,
		Targets:    selectors,
		Until:      until,
		StartedAt:  time.Now().UTC(),
		Deadline:   time.Now().UTC().Add(slice),
		ResumeArgs: append([]string{"wb", "wait", kind}, append(selectors, "--until", until)...),
	}
	if identity, ok := worktrees.RegisteredIdentity(); ok {
		record.WBSessionID = identity.WBSessionID
	}
	release, err := waitregistry.Register(home, record)
	if err != nil {
		return func() {}
	}
	return release
}

func newWaitListCmd(inv *invocation) *cobra.Command {
	var jsonOut bool
	var prune bool
	command := &cobra.Command{
		Use:   "list",
		Short: "Show what WB is currently waiting for on this machine",
		Long: `List every outstanding wait recorded on this machine.

This exists so a quiet session can be told apart from a stopped one. An agent
that has correctly delegated its waiting produces no output until the wait ends;
without this, that is indistinguishable from a crash.

A wait whose process is gone is reported as stale rather than hidden, because a
waiter that died is the thing most worth knowing about. Listing never deletes;
pass --prune to remove stale records.`,
		Example: `wb wait list
wb wait list --json`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			home, err := wbhome.EnsureRoot(inv.projectsRoot)
			if err != nil {
				return err
			}
			if prune {
				removed, pruneErr := waitregistry.Prune(home, waitregistry.Options{})
				if pruneErr != nil {
					return pruneErr
				}
				if _, err := fmt.Fprintf(command.OutOrStdout(), "pruned %d stale wait(s)\n", removed); err != nil {
					return err
				}
				return nil
			}
			records, err := waitregistry.List(home, waitregistry.Options{})
			if err != nil {
				return err
			}
			if jsonOut {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(map[string]any{"schema_version": 1, "waits": records})
			}
			if len(records) == 0 {
				_, err := fmt.Fprintln(command.OutOrStdout(), "no outstanding waits")
				return err
			}
			for _, record := range records {
				state := "waiting"
				if record.Stale {
					state = "stale"
				}
				line := fmt.Sprintf("%s %s %s until %s for %s since %s",
					state, record.Kind, strings.Join(record.Targets, " "), record.Until,
					record.WBSessionID, record.StartedAt.Format(time.RFC3339))
				if _, err := fmt.Fprintln(command.OutOrStdout(), line); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&prune, "prune", false, "remove records whose waiting process is gone")
	addJSONFormatFlags(command, &jsonOut)
	setDiscoveryTerms(command, "wait list outstanding waiting pending stale session visible stopped quiet")
	return command
}

// The three commands below are the verb-first spelling of waits WB already had,
// scattered under `ci`, `agent` and `daemon operation`. Nothing listed them
// together, which is part of why the measured adoption of `wb ci wait` was what
// it was: an agent cannot choose a verb it never sees.
//
// Each builds from the SAME constructor as the original, so the two spellings
// are one implementation and cannot drift. A command belongs to one parent, so
// the constructor is called again here rather than the instance being shared.

// newWaitChecksCmd is `wb ci wait` under the verb. The object is an exact
// commit's checks — "ci" names a domain, not a thing a caller can point at.
// This remains the authoritative merge-evidence waiter; `wb wait pr` does not.
func newWaitChecksCmd(inv *invocation) *cobra.Command {
	command := newCIWaitCmd(inv)
	command.Use = strings.Replace(command.Use, "wait ", "checks ", 1)
	command.Short = "Wait one bounded slice for checks on an exact head (was: wb ci wait)"
	command.Aliases = append(command.Aliases, "ci")
	return command
}

// newWaitAgentCmd is `wb agent await` under the verb. `await` is kept as an
// alias here because that is the spelling agents already know.
func newWaitAgentCmd(inv *invocation) *cobra.Command {
	command := newAgentAwaitCmd(inv)
	command.Use = strings.Replace(command.Use, "await ", "agent ", 1)
	command.Short = "Block until a dispatched agent run is terminal (was: wb agent await)"
	command.Aliases = append(command.Aliases, "await")
	return command
}

// newWaitOperationCmd is `wb daemon operation wait` under the verb.
func newWaitOperationCmd(inv *invocation) *cobra.Command {
	command := newDaemonOperationWaitCmd(inv, defaultDaemonDependencies())
	command.Use = strings.Replace(command.Use, "wait ", "operation ", 1)
	command.Short = "Wait for a durable operation to reach a terminal state (was: wb daemon operation wait)"
	return command
}
