// Package prwatch is the daemon's watcher for herdr-session-transport
// (spec/plans/herdr-session-transport.md, Task 6): it discovers pull
// requests worth watching only through
// worktrees.ListRegisteredPullRequestBindings — the first reader of the
// binding `wb pr create` already records via
// worktrees.RecordClaimPullRequestBinding — and evaluates each one's outcome
// through WB's existing renamed-required-check-aware check-verdict logic,
// orchestrate.WaitForPullRequestChecks, the same evaluation `wb ci wait` and
// `wb pr land` already use. It never scans GitHub for pull requests WB has no
// recorded binding for, and it never reimplements check-state interpretation.
//
// This package produces Outcomes only; it does not decide what to do with
// one. Resolving the task's current session and transport identity, the
// at-most-once delivery-intent coalescing, and the record-only/advisory/
// submit decision belong to herdr-session-transport's Task 7, which consumes
// the Outcomes this package produces.
package prwatch

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// DefaultEvaluateSlice bounds one watcher evaluation pass over a single
// registered pull request. It is deliberately far shorter than `wb ci
// wait`'s own eight-minute default: that command is one foreground,
// human-invoked wait for one exact head, while the watcher polls every
// registered pull request unattended on its own outer cadence (owned by the
// daemon loop that calls Poll repeatedly, not by this package) and must not
// hold one binding's evaluation open while every other registered pull
// request waits behind it.
const DefaultEvaluateSlice = 20 * time.Second

// DefaultEvaluateCheckPollInterval is the poll cadence within one evaluation
// pass — see DefaultEvaluateSlice for why it is shorter than `wb ci wait`'s
// own default interval.
const DefaultEvaluateCheckPollInterval = 5 * time.Second

// EvaluateOptions configures one evaluation pass over a single registered
// pull request. It is deliberately a thin subset of
// orchestrate.PullRequestWaitOptions: Repository, PullRequest, Target, and
// Head are filled in by Evaluate itself, from the binding and a fresh
// orchestrate.ReadPullRequest, never by the caller — the watcher always
// evaluates the pull request's current identity, not a stale one a caller
// might otherwise supply.
type EvaluateOptions struct {
	// Slice bounds one evaluation pass. Zero uses DefaultEvaluateSlice.
	Slice time.Duration
	// CheckPollInterval is the poll cadence within Slice. Zero uses
	// DefaultEvaluateCheckPollInterval.
	CheckPollInterval time.Duration
	// StableRereadDelay overrides orchestrate's shortened confirming-reread
	// wait. Zero uses orchestrate's own default (see
	// orchestrate.DefaultStableRereadDelay).
	StableRereadDelay time.Duration
	// AllowUnfenced permits a validation-only receipt when the pull
	// request's target branch has no server-enforced strict freshness
	// fence, exactly as orchestrate.PullRequestWaitOptions.AllowUnfenced
	// does. The watcher never merges or lands anything on Task 6's own
	// behalf, so this only widens which pull requests can reach a terminal
	// verdict rather than polling to their deadline every time.
	AllowUnfenced bool
}

// Outcome is one evaluated registered pull request: the identifiers Task 7's
// delivery resolution needs to key its at-most-once delivery intent — task,
// pull request, and head SHA — plus the check-verdict receipt that produced
// Status. It carries no session, pane, or transport identity: Task 7 resolves
// those separately, at delivery time, from the task's current Work Log claim,
// never from this Outcome.
type Outcome struct {
	Task        string
	ClaimID     string
	Repository  string
	PullRequest int
	URL         string
	// Head is the pull request's exact head SHA observed at evaluation
	// time — part of Task 7's at-most-once delivery-intent key, alongside
	// Task, PullRequest, and Status.
	Head string
	// Target is the pull request's exact base branch observed at
	// evaluation time.
	Target string
	// Status mirrors orchestrate.PullRequestWaitStatus exactly: "passed",
	// "pending", or "failed". A watcher loop that calls Poll repeatedly
	// simply re-evaluates a "pending" outcome on its next pass.
	Status orchestrate.PullRequestWaitStatus
	Reason string
	// Checks and RequiredChecks are copied from the check-verdict receipt
	// unchanged, so Task 8's facts-only template can name a required check
	// (or a count) without re-deriving it from GitHub.
	Checks         []orchestrate.RemoteCheck
	RequiredChecks []orchestrate.RequiredRemoteCheck
	EvaluatedAt    time.Time
}

// Evaluate reads binding's pull request current identity and evaluates its
// checks through orchestrate.WaitForPullRequestChecks — WB's existing
// renamed-required-check-aware check-verdict logic — never a reimplemented
// check-state interpretation. It performs exactly the GitHub reads that
// logic already performs; it adds no new ones.
func Evaluate(ctx context.Context, binding worktrees.RegisteredPullRequestBinding, options EvaluateOptions) (Outcome, error) {
	repository := strings.TrimSpace(binding.Repository)
	if repository == "" || binding.PullRequest <= 0 {
		return Outcome{}, fmt.Errorf("registered pull-request binding for task %q claim %q is missing a repository or pull-request number", binding.Task, binding.ClaimID)
	}
	pullRequest := strconv.Itoa(binding.PullRequest)
	view, err := orchestrate.ReadPullRequest(ctx, repository, pullRequest)
	if err != nil {
		return Outcome{}, fmt.Errorf("read registered pull request %s#%s for task %s: %w", repository, pullRequest, binding.Task, err)
	}
	head := strings.TrimSpace(view.Head.SHA)
	target := strings.TrimSpace(view.Base.Ref)
	if head == "" || target == "" {
		return Outcome{}, fmt.Errorf("registered pull request %s#%s for task %s returned no head or target", repository, pullRequest, binding.Task)
	}
	slice := options.Slice
	if slice <= 0 {
		slice = DefaultEvaluateSlice
	}
	interval := options.CheckPollInterval
	if interval <= 0 {
		interval = DefaultEvaluateCheckPollInterval
	}
	result, err := orchestrate.WaitForPullRequestChecks(ctx, orchestrate.PullRequestWaitOptions{
		Repository:        repository,
		PullRequest:       pullRequest,
		Target:            target,
		Head:              head,
		Slice:             slice,
		CheckPollInterval: interval,
		StableRereadDelay: options.StableRereadDelay,
		AllowUnfenced:     options.AllowUnfenced,
	})
	if err != nil {
		return Outcome{}, fmt.Errorf("evaluate registered pull request %s#%s for task %s: %w", repository, pullRequest, binding.Task, err)
	}
	return Outcome{
		Task:           binding.Task,
		ClaimID:        binding.ClaimID,
		Repository:     repository,
		PullRequest:    binding.PullRequest,
		URL:            binding.URL,
		Head:           head,
		Target:         target,
		Status:         result.Status,
		Reason:         result.Reason,
		Checks:         result.Checks,
		RequiredChecks: result.RequiredChecks,
		EvaluatedAt:    time.Now().UTC(),
	}, nil
}

// PollResult pairs one registered binding with the Outcome its evaluation
// produced, or the error that evaluation hit. One unreadable or unreachable
// pull request never stops Poll from evaluating the rest.
type PollResult struct {
	Binding worktrees.RegisteredPullRequestBinding
	Outcome Outcome
	Err     error
}

// Poll runs one evaluation pass over every pull request WB has a recorded
// binding for — worktrees.ListRegisteredPullRequestBindings, never a
// fleet-wide GitHub scan — and evaluates each one's checks through the same
// renamed-required-check-aware verdict logic `wb ci wait` and `wb pr land`
// already use. The caller decides the outer cadence: Poll itself runs
// exactly one pass and returns.
func Poll(ctx context.Context, projectsRoot string, options EvaluateOptions) ([]PollResult, error) {
	bindings, err := worktrees.ListRegisteredPullRequestBindings(projectsRoot)
	if err != nil {
		return nil, fmt.Errorf("list registered pull-request bindings: %w", err)
	}
	results := make([]PollResult, 0, len(bindings))
	for _, binding := range bindings {
		outcome, evalErr := Evaluate(ctx, binding, options)
		results = append(results, PollResult{Binding: binding, Outcome: outcome, Err: evalErr})
	}
	return results, nil
}
