// Package waitrun observes bounded PR conditions and composes wait metadata effects.
// It does not import CLI packages or change pull requests.
package waitrun

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/prsnapshot"
	"sort"
	"strings"
	"time"
)

type Condition string

const (
	ChecksSettled Condition = "checks-settled"
	Changed       Condition = "changed"
	Closed        Condition = "closed"
)

type Output struct {
	SchemaVersion int       `json:"schema_version"`
	ObservedAt    time.Time `json:"observed_at"`
	Until         string    `json:"until"`
	Status        string    `json:"status"`
	Observations  int       `json:"observations"`
	Targets       []Target  `json:"targets"`
	ResumeArgs    []string  `json:"resume_args,omitempty"`
}

type Target struct {
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

// TargetStatus values. "settled" means the requested condition holds;
// "pending" means it does not yet. "error" is a read that failed in a way a
// retry is unlikely to fix — a transient read stays pending on purpose, so a
// GitHub blip resumes instead of ending the wait with a false verdict.
const (
	Settled   = "settled"
	Pending   = "pending"
	ReadError = "error"
)

type Reference struct {
	Selector   string
	Repository string
	Number     string
}

type Update struct {
	Observations, Settled, Total int
	Interval                     time.Duration
}
type Request struct {
	Targets         []Reference
	Condition       Condition
	Slice, Interval time.Duration
	Progress        func(Update)
}

// Observer retains the existing per-instance effects; nil uses real observations,
// the monotonic system clock and a context-cancelable timer.
type Observer struct {
	Read  func(context.Context, Reference) Target
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
}

func (observer Observer) Wait(ctx context.Context, request Request) Output {
	return observeWaitTargets(ctx, waitRun{Targets: request.Targets, Condition: request.Condition, Slice: request.Slice, Interval: request.Interval, Progress: request.Progress, observe: observer.Read, now: observer.Now, sleep: observer.Sleep})
}

type waitRun struct {
	Targets   []Reference
	Condition Condition
	Slice     time.Duration
	Interval  time.Duration
	Progress  func(Update)
	observe   func(context.Context, Reference) Target
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
}

func (run waitRun) observation() func(context.Context, Reference) Target {
	if run.observe != nil {
		return run.observe
	}
	return ObservePullRequest
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
func observeWaitTargets(ctx context.Context, run waitRun) Output {
	observe, now, pause := run.observation(), run.clock(), run.pause()
	deadline := now().Add(run.Slice)
	first := map[string]Target{}
	latest := map[string]Target{}
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
		if output.Status == Settled {
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
			run.Progress(Update{Observations: observations, Settled: countSettled(latest), Total: len(run.Targets), Interval: run.Interval})
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

func settled(target Target) bool { return target.Status == Settled }

func countSettled(latest map[string]Target) int {
	count := 0
	for _, target := range latest {
		if settled(target) {
			count++
		}
	}
	return count
}

func collectWaitOutput(run waitRun, latest map[string]Target, observations int, at time.Time) Output {
	output := Output{
		SchemaVersion: 1,
		ObservedAt:    at.UTC(),
		Until:         string(run.Condition),
		Status:        Settled,
		Observations:  observations,
		Targets:       make([]Target, 0, len(run.Targets)),
	}
	for _, target := range run.Targets {
		observed := latest[target.Selector]
		if observed.Selector == "" {
			observed = Target{Selector: target.Selector, Repository: target.Repository, Number: target.Number, Status: Pending}
		}
		if observed.Status != Settled {
			output.Status = Pending
		}
		output.Targets = append(output.Targets, observed)
	}
	return output
}

// decideWaitTarget turns one observation into a verdict for the requested
// condition. An errored read is reported but never settles the target: WB does
// not claim an outcome it could not observe.
func decideWaitTarget(observed, first Target, condition Condition) Target {
	if observed.Status == ReadError {
		return observed
	}
	closed := observed.State != "" && !strings.EqualFold(observed.State, "open")
	switch condition {
	case Closed:
		observed.Status = Pending
		if closed {
			observed.Status = Settled
		}
	case Changed:
		observed.Status = Pending
		if targetMoved(first, observed) {
			observed.Status = Settled
		}
	default:
		observed.Status = Pending
		if closed || (observed.Checks != nil && observed.Checks["pending"] == 0) {
			observed.Status = Settled
		}
	}
	if observed.Status == Pending && observed.Reason == "" {
		observed.Reason = waitPendingReason(observed, condition)
	}
	return observed
}

func waitPendingReason(observed Target, condition Condition) string {
	switch condition {
	case Closed:
		return "still open"
	case Changed:
		return "unchanged since first observation"
	default:
		if observed.Checks == nil {
			return "no checks observed yet"
		}
		return fmt.Sprintf("%d check(s) still running", observed.Checks["pending"])
	}
}

// targetMoved compares only facts an agent would act on. Check counts are
// compared as a whole so a run flipping from pending to failed counts as a
// change, while a re-reported identical set does not.
func targetMoved(first, latest Target) bool {
	if first.Head != latest.Head || first.State != latest.State || first.Draft != latest.Draft {
		return true
	}
	if first.Mergeable != latest.Mergeable {
		return true
	}
	return ChecksKey(first.Checks) != ChecksKey(latest.Checks)
}

func ChecksKey(checks map[string]int) string {
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

// ObservePullRequest reads one pull request and the checks on its exact head.
// It delegates to prsnapshot.Observe — one shared implementation of both
// facts, reused by the herdr-session-transport daemon watcher
// (internal/prwatch) rather than a second dialect of the same GitHub reads —
// and adapts the result onto Target so `wb wait pr`'s own behavior is
// unchanged by the move (see reader_test.go).
func ObservePullRequest(ctx context.Context, reference Reference) Target {
	target := Target{Selector: reference.Selector, Repository: reference.Repository, Number: reference.Number}
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
func waitReadFailure(target Target, err error) Target {
	target.Reason = err.Error()
	target.Status = ReadError
	if orchestrate.IsTransientReadFailure(err) {
		target.Status = Pending
	}
	return target
}
