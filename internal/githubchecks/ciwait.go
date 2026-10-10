package githubchecks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/worktreebranches"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

// MaxForegroundCheckWaitSlice keeps a single agent-tool call under the common
// ten-minute harness ceiling. Longer CI is observed by explicit re-invocation,
// never a detached worker or a hidden thirty-minute loop.
const MaxForegroundCheckWaitSlice = 9 * time.Minute

// DefaultCheckPollInterval deliberately leaves room for other WB operations
// sharing the authenticated GitHub user budget. A PR receipt still reads every
// dynamic fact on each observation; only static branch policy is cached within
// the bounded slice and is fetched again before a pass is returned.
const DefaultCheckPollInterval = 30 * time.Second

// DefaultStableRereadDelay bounds the wait before the confirming reread of a
// checks-bearing terminal observation. The stability fingerprint exists to
// catch a check set that is still registering, not to space out load: once
// every observed and required check is terminal, only this one confirming
// observation (plus the fresh authority and identity receipts) stands between
// the campaign and its receipt, so waiting a full quota-aware poll interval
// there adds DefaultCheckPollInterval of pure latency to every passing PR
// merge, PR validation, and direct-target receipt. Fifteen seconds sits
// outside GitHub's usual push-to-registration envelope for lazily created
// non-required checks (matrix expansion, workflow_run chains) while still
// cutting half the default cadence. It is a variable, and
// PullRequestWaitOptions.StableRereadDelay overrides it per wait, so tests
// and unusual deployments can tune it without recompiling callers.
//
// Two terminal receipts never shorten this wait: the no-applicable-checks
// receipt (an empty observed set with an enumerated empty policy), whose only
// time-based guard against a repository whose CI simply has not registered
// yet IS this gap, and any reread after the previous observation was already
// terminal — a churning terminal fingerprint (for example a moving target
// head) falls back to the full poll cadence instead of re-observing on the
// short delay without bound.
var DefaultStableRereadDelay = 15 * time.Second

// stableRereadDelay returns the wait before a confirming reread of a
// checks-bearing terminal observation: the configured confirmation delay
// (DefaultStableRereadDelay when unset), never longer than the caller's own
// poll interval.
func stableRereadDelay(pollInterval, configured time.Duration) time.Duration {
	delay := configured
	if delay <= 0 {
		delay = DefaultStableRereadDelay
	}
	if pollInterval < delay {
		return pollInterval
	}
	return delay
}

// WaitForCommitChecks observes checks for one exact target commit. PullRequest
// is optional: when present it corroborates that exact PR head and target and
// augments the exact-head check-run/status receipt with GitHub's PR view.
// Every mode observes the exact commit through producer-aware APIs. Pending is
// an intermediate terminal result that callers resume with the same identity,
// not successful completion.
func WaitForCommitChecks(ctx context.Context, options PullRequestWaitOptions) (PullRequestWaitResult, error) {
	telemetry := &githubobserver.RetryTelemetry{}
	ctx = githubobserver.WithRetryTelemetry(ctx, telemetry)
	result, err := waitForCommitChecks(ctx, options)
	if telemetry.Count > 0 {
		if result.Evidence == nil {
			result.Evidence = map[string]string{}
		}
		result.Evidence["github_read_retries"] = fmt.Sprintf("%d (last: %s)", telemetry.Count, telemetry.LastReason)
	}
	return result, err
}

// waitForCommitChecks is the exact-commit observation loop wrapped by the
// exported WaitForCommitChecks above so every call site — direct-target
// waits, PR waits, and the recursive call from WaitForPullRequestChecks —
// gets the same github_read_retries evidence without threading telemetry
// through every early return in the loop below.
func waitForCommitChecks(ctx context.Context, options PullRequestWaitOptions) (PullRequestWaitResult, error) {
	return waitForCommitChecksWith(ctx, options, commitChecksWaitOps{
		pullRequestIdentity: PullRequestIdentity,
		targetHead:          TargetHead,
		containsTarget:      ContainsTarget,
		checks:              commitChecks,
		required:            requiredChecksReceipt,
		failureDetails:      failedCheckDetails,
	})
}

type commitChecksWaitOps struct {
	pullRequestIdentity func(context.Context, string, string) (string, string, string)
	targetHead          func(context.Context, string, string) (string, string)
	containsTarget      func(context.Context, string, string, string) (bool, string)
	checks              func(context.Context, PullRequestWaitOptions) ([]RemoteCheck, bool, string)
	required            func(context.Context, PullRequestWaitOptions, *requiredChecksCache) ([]RequiredRemoteCheck, string, string, string, string)
	failureDetails      func(context.Context, string, []RemoteCheck) []CIFailureDetail
}

func waitForCommitChecksWith(ctx context.Context, options PullRequestWaitOptions, ops commitChecksWaitOps) (PullRequestWaitResult, error) {
	if strings.TrimSpace(options.Repository) == "" || strings.TrimSpace(options.Target) == "" || strings.TrimSpace(options.Head) == "" {
		return PullRequestWaitResult{}, fmt.Errorf("repository, target, and exact head are required")
	}
	if options.Slice <= 0 || options.Slice > MaxForegroundCheckWaitSlice {
		return PullRequestWaitResult{}, fmt.Errorf("check wait slice must be positive and at most %s", MaxForegroundCheckWaitSlice)
	}
	if options.CheckPollInterval <= 0 {
		return PullRequestWaitResult{}, fmt.Errorf("check poll interval must be positive")
	}
	if options.CheckPollInterval >= options.Slice {
		return PullRequestWaitResult{}, fmt.Errorf("check poll interval must be shorter than the foreground slice so a terminal snapshot can be reread")
	}
	if expected := options.ExpectedActionChecks; expected != nil && (expected.WorkflowID <= 0 || expected.Event == "" || expected.PullRequestNumber <= 0 || expected.PullRequestBase == "" || len(expected.Names) == 0) {
		return PullRequestWaitResult{}, fmt.Errorf("expected Actions checks require a workflow ID, event, and at least one job")
	}
	result := PullRequestWaitResult{
		Repository:         options.Repository,
		PullRequest:        options.PullRequest,
		Target:             options.Target,
		Head:               options.Head,
		UnfencedValidation: options.AllowUnfenced,
	}
	deadline := time.Now().Add(options.Slice)
	sliceCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	sliceCtx = githubobserver.WithProgress(sliceCtx, options.OperationProgress)
	stableFingerprint := ""
	stableObservations := 0
	observations := 0
	previousObservationTerminal := false
	policyCache := requiredChecksCache{}
	for {
		if err := sliceCtx.Err(); err != nil {
			if err == context.DeadlineExceeded {
				return PendingResult(result), nil
			}
			return failedCommitWaitResult(result, err.Error()), nil
		}
		observedTargetHead := ""
		if options.PullRequest != "" {
			observedHead, observedTarget, reason := ops.pullRequestIdentity(sliceCtx, options.Repository, options.PullRequest)
			result.ObservedHead = observedHead
			if reason != "" {
				if sliceCtx.Err() == context.DeadlineExceeded || githubobserver.IsTransientReadReason(reason) {
					return PendingResult(result), nil
				}
				return failedCommitWaitResult(result, reason), nil
			}
			if observedHead != options.Head {
				return failedCommitWaitResult(result, fmt.Sprintf("pull request head drifted from %s to %s; start a new exact wait", options.Head, observedHead)), nil
			}
			if observedTarget != options.Target {
				return failedCommitWaitResult(result, fmt.Sprintf("pull request target drifted from %s to %s; start a new exact wait", options.Target, observedTarget)), nil
			}
			observedTargetHead, reason = ops.targetHead(sliceCtx, options.Repository, options.Target)
			result.ObservedTargetHead = observedTargetHead
			if reason != "" {
				if sliceCtx.Err() == context.DeadlineExceeded || githubobserver.IsTransientReadReason(reason) {
					return PendingResult(result), nil
				}
				return failedCommitWaitResult(result, "read exact pull-request target head: "+reason), nil
			}
			containsTarget, reason := ops.containsTarget(sliceCtx, options.Repository, observedTargetHead, options.Head)
			if reason != "" {
				if sliceCtx.Err() == context.DeadlineExceeded || githubobserver.IsTransientReadReason(reason) {
					return PendingResult(result), nil
				}
				return failedCommitWaitResult(result, reason), nil
			}
			result.CandidateContainsTarget = containsTarget
			if !containsTarget {
				return failedCommitWaitResult(result, fmt.Sprintf("pull request head %s does not contain current target %s at %s; rebase or reintegrate before waiting or merging", options.Head, options.Target, observedTargetHead)), nil
			}
		} else {
			observedHead, reason := ops.targetHead(sliceCtx, options.Repository, options.Target)
			result.ObservedHead = observedHead
			if reason != "" {
				if sliceCtx.Err() == context.DeadlineExceeded || githubobserver.IsTransientReadReason(reason) {
					return PendingResult(result), nil
				}
				return failedCommitWaitResult(result, reason), nil
			}
			if observedHead != options.Head {
				if !options.AllowTargetDescendant {
					return failedCommitWaitResult(result, fmt.Sprintf("target %s advanced from exact head %s to %s; start a new exact wait", options.Target, options.Head, observedHead)), nil
				}
				containsHead, ancestryReason := ops.containsTarget(sliceCtx, options.Repository, options.Head, observedHead)
				if ancestryReason != "" {
					return failedCommitWaitResult(result, ancestryReason), nil
				}
				result.TargetContainsHead = containsHead
				if !containsHead {
					return failedCommitWaitResult(result, fmt.Sprintf("current target %s at %s does not contain exact landed head %s", options.Target, observedHead, options.Head)), nil
				}
			} else {
				result.TargetContainsHead = true
			}
			observedTargetHead = observedHead
			result.ObservedTargetHead = observedHead
			result.CandidateContainsTarget = true
		}

		checks, pending, reason := ops.checks(sliceCtx, options)
		if reason != "" {
			if sliceCtx.Err() == context.DeadlineExceeded || githubobserver.IsTransientReadReason(reason) {
				return PendingResult(result), nil
			}
			return failedCommitWaitResult(result, reason), nil
		}
		result.Checks = checks
		observations++
		// Report as soon as the exact-head check receipt is available. Later in
		// this observation WB may still need branch-policy or freshness receipts;
		// publishing this event keeps a slow authority lookup from looking hung.
		reportPullRequestWaitProgress(options, observations, result, 0)
		if options.ExpectedActionChecks == nil && failedObservedChecks(checks, &pending, options.WaiveChecks...) {
			failedResult := failedCommitWaitResult(result, "observed GitHub checks failed or were cancelled")
			failedResult.FailureDetails = ops.failureDetails(sliceCtx, options.Repository, checks)
			reportPullRequestWaitProgress(options, observations, failedResult, 0)
			return failedResult, nil
		}
		missingExpected, rejectedExpected := expectedActionCheckState(checks, options.ExpectedActionChecks)
		if len(rejectedExpected) > 0 {
			return failedCommitWaitResult(result, "expected CI jobs did not execute successfully: "+strings.Join(rejectedExpected, ", ")), nil
		}
		if len(missingExpected) > 0 {
			pending = true
		}

		requiredChecks, authority, freshnessAuthority, policyUnavailable, authorityReason := ops.required(sliceCtx, options, &policyCache)
		if !adoptCommitCheckPolicy(sliceCtx, &result, requiredChecks, authority, freshnessAuthority, policyUnavailable, authorityReason) {
			return PendingResult(result), nil
		}
		if options.ExpectedActionChecks != nil {
			checks = relevantExpectedActionChecks(checks, options.ExpectedActionChecks, requiredChecks)
			result.Checks = checks
			pending = false
			if failedObservedChecks(checks, &pending, options.WaiveChecks...) {
				failedResult := failedCommitWaitResult(result, "observed required or expected GitHub checks failed or were cancelled")
				failedResult.FailureDetails = ops.failureDetails(sliceCtx, options.Repository, checks)
				reportPullRequestWaitProgress(options, observations, failedResult, 0)
				return failedResult, nil
			}
		}
		if options.PullRequest != "" && freshnessAuthority == "" && !options.AllowUnfenced {
			return failedCommitWaitResult(result, "target policy has no nonempty server-enforced strict up-to-date fence; check observations cannot authorize an automatic merge"), nil
		}
		missingRequired := missingRequiredChecks(checks, requiredChecks)
		// A direct target can truthfully have no applicable CI at all (for
		// example, a docs-only repository or a path-filtered workflow). GitHub's
		// complete check-run/status APIs plus the enumerated empty policy are an
		// authoritative receipt in that case. PR mode deliberately never takes
		// this route on its own: without a server-enforced strict freshness fence
		// an empty check set cannot be told apart from CI that has not registered
		// yet, so it demands a nonempty observed set. An explicit
		// --allow-unfenced gives up that fence, and then the empty receipt is
		// authoritative for a candidate too. Without this a repository that has
		// no CI at all can only poll until its slice deadline and report
		// checks_pending on every landing.
		//
		// Rejected alternative, 2026-09-18 (#544): make the pull-request route
		// fail fast here instead, refusing an authoritatively empty policy and
		// telling the operator to rerun with --route direct. It was written to
		// stop a PR route spending repeated full timeout slices in a permanently
		// pending state, which is a real problem — but the branch above already
		// solves it, by terminating rather than polling.
		//
		// The deciding argument was cost to the agents that run this command,
		// not correctness: both behaviours are defensible. Failing fast costs a
		// refused landing, a diagnostic to interpret and a re-run under a
		// different route — two or more further turns, each re-reading the whole
		// session context. Accepting the receipt finishes in one. On a fleet
		// where that context is the dominant line on the bill, an extra round
		// trip per landing is the expensive option.
		//
		// What makes accepting safe is that --allow-unfenced is explicit. The
		// operator asked for a candidate to be judged without a server-enforced
		// fence, and this is that judgement, recorded in a receipt that states
		// the policy was enumerated as empty and reread unchanged. It is not a
		// default, and it is not silent. Note the distinction the flag draws: it
		// waives a fence that could not be read, and separately permits an empty
		// one that was read. A policy WB failed to fetch and a policy WB fetched
		// and found empty are different receipts.
		noApplicableChecks := len(checks) == 0 && len(requiredChecks) == 0 &&
			(options.PullRequest == "" || options.AllowUnfenced)
		terminal := !pending && len(missingRequired) == 0 && len(missingExpected) == 0 && (len(checks) > 0 || noApplicableChecks)
		if terminal {
			fingerprint := terminalChecksFingerprint(checks, requiredChecks, authority, observedTargetHead, freshnessAuthority)
			if fingerprint == stableFingerprint {
				stableObservations++
			} else {
				stableFingerprint = fingerprint
				stableObservations = 1
			}
			result.StableObservations = stableObservations
		} else {
			stableFingerprint = ""
			stableObservations = 0
			result.StableObservations = 0
			switch {
			case len(missingRequired) > 0:
				result.Reason = "required GitHub checks have not registered for the exact head: " + strings.Join(missingRequired, ", ")
			case len(missingExpected) > 0:
				result.Reason = "expected Actions jobs have not registered for the exact head: " + strings.Join(missingExpected, ", ")
			case len(checks) == 0:
				result.Reason = "no GitHub checks have registered for the exact head"
			default:
				result.Reason = "observed GitHub checks are still pending"
			}
		}
		if terminal && stableObservations >= 2 {
			// Branch protection and active rules are configuration, not a CI
			// observation. Reusing their first receipt eliminates three REST
			// requests from every pending poll, but a pass must still be based on
			// a fresh authority receipt in case policy changed during the slice.
			requiredChecks, authority, freshnessAuthority, policyUnavailable, authorityReason = ops.required(sliceCtx, options, nil)
			if !adoptCommitCheckPolicy(sliceCtx, &result, requiredChecks, authority, freshnessAuthority, policyUnavailable, authorityReason) {
				return PendingResult(result), nil
			}
			if options.PullRequest != "" && freshnessAuthority == "" && !options.AllowUnfenced {
				return failedCommitWaitResult(result, "target policy has no nonempty server-enforced strict up-to-date fence; check observations cannot authorize an automatic merge"), nil
			}
			if missingRequired = missingRequiredChecks(checks, requiredChecks); len(missingRequired) > 0 {
				result.Reason = "required GitHub checks have not registered for the exact head: " + strings.Join(missingRequired, ", ")
				return PendingResult(result), nil
			}
			// A final identity receipt closes the race between the stable check
			// observation and the reported terminal pass.
			if options.PullRequest != "" {
				observedHead, observedTarget, reason := ops.pullRequestIdentity(sliceCtx, options.Repository, options.PullRequest)
				result.ObservedHead = observedHead
				if reason != "" {
					if sliceCtx.Err() == context.DeadlineExceeded || githubobserver.IsTransientReadReason(reason) {
						return PendingResult(result), nil
					}
					return failedCommitWaitResult(result, reason), nil
				}
				if observedHead != options.Head || observedTarget != options.Target {
					return failedCommitWaitResult(result, "pull request identity changed after checks passed; start a new exact wait"), nil
				}
				finalTargetHead, targetReason := ops.targetHead(sliceCtx, options.Repository, options.Target)
				if targetReason != "" {
					if sliceCtx.Err() == context.DeadlineExceeded || githubobserver.IsTransientReadReason(targetReason) {
						return PendingResult(result), nil
					}
					return failedCommitWaitResult(result, "re-read exact pull-request target head: "+targetReason), nil
				}
				if finalTargetHead != observedTargetHead {
					return failedCommitWaitResult(result, fmt.Sprintf("target %s advanced after checks passed from %s to %s; rebase or reintegrate before merging", options.Target, observedTargetHead, finalTargetHead)), nil
				}
			} else {
				observedHead, reason := ops.targetHead(sliceCtx, options.Repository, options.Target)
				result.ObservedHead = observedHead
				if reason != "" {
					if sliceCtx.Err() == context.DeadlineExceeded || githubobserver.IsTransientReadReason(reason) {
						return PendingResult(result), nil
					}
					return failedCommitWaitResult(result, reason), nil
				}
				if observedHead != options.Head {
					if !options.AllowTargetDescendant {
						return failedCommitWaitResult(result, "target advanced after checks passed; start a new exact wait"), nil
					}
					containsHead, ancestryReason := ops.containsTarget(sliceCtx, options.Repository, options.Head, observedHead)
					if ancestryReason != "" {
						return failedCommitWaitResult(result, ancestryReason), nil
					}
					result.TargetContainsHead = containsHead
					if !containsHead {
						return failedCommitWaitResult(result, fmt.Sprintf("current target %s at %s does not contain exact landed head %s", options.Target, observedHead, options.Head)), nil
					}
				} else {
					result.TargetContainsHead = true
				}
				result.ObservedTargetHead = observedHead
			}
			result.Status = PullRequestWaitPassed
			if options.PullRequest != "" && !options.AllowUnfenced {
				result.Reason = "GitHub's required-check policy was enumerated, every required check was present, the candidate contained the exact target, server-side target freshness was enforced, and the observed GitHub check set stayed terminal across a bounded stable reread"
			} else if options.PullRequest != "" && result.PolicyAuthorityUnavailable != "" {
				result.Reason = "GitHub branch-policy authority was unavailable under explicit --allow-unfenced (" + result.PolicyAuthorityUnavailable + "); the pull-request base, exact candidate head, target containment, and observed GitHub check set stayed terminal across a bounded stable reread for validation-only publication"
			} else if noApplicableChecks {
				result.Reason = "GitHub's required-check policy was enumerated as empty, complete check-run and status receipts registered no checks, and that no-applicable-check receipt stayed unchanged across a bounded stable reread"
			} else if options.PullRequest != "" {
				result.Reason = "GitHub's required-check policy was enumerated, every required check was present, the candidate contained the exact target, and the observed GitHub check set stayed terminal across a bounded stable reread for validation-only publication; server-side target freshness was intentionally not required because this path does not merge"
			} else if result.PolicyAuthorityUnavailable != "" {
				result.Reason = "GitHub branch-policy authority was unavailable under explicit --allow-unfenced (" + result.PolicyAuthorityUnavailable + "); the exact remote target head and observed GitHub check set stayed terminal across a bounded stable reread"
			} else {
				result.Reason = "GitHub's required-check policy was enumerated (possibly empty), every required check was present, and the exact remote target's observed check set stayed terminal across a bounded stable reread"
			}
			reportPullRequestWaitProgress(options, observations, result, 0)
			return result, nil
		}
		nextObservation := options.CheckPollInterval
		if terminal {
			result.Reason = "terminal checks require one unchanged foreground reread before they form a bounded CI receipt"
			// The confirming reread of a checks-bearing terminal set does not
			// need the full quota-aware poll cadence: the fingerprint guard is
			// against a check set still registering, and the authority and
			// identity receipts are re-read after stability regardless. Two
			// receipts never shorten it (see DefaultStableRereadDelay): the
			// no-applicable-checks receipt, whose only time-based guard against
			// unregistered CI is this gap, and any observation whose
			// predecessor was already terminal — a churning terminal
			// fingerprint falls back to the full cadence instead of
			// re-observing on the short delay without bound.
			if !noApplicableChecks && !previousObservationTerminal {
				nextObservation = stableRereadDelay(options.CheckPollInterval, options.StableRereadDelay)
			}
		}
		previousObservationTerminal = terminal
		if !time.Now().Add(nextObservation).Before(deadline) {
			pendingResult := result
			pendingResult.Status = PullRequestWaitPending
			reportPullRequestWaitProgress(options, observations, pendingResult, 0)
			return PendingResult(result), nil
		}
		pendingResult := result
		pendingResult.Status = PullRequestWaitPending
		reportPullRequestWaitProgress(options, observations, pendingResult, nextObservation)
		timer := time.NewTimer(nextObservation)
		select {
		case <-sliceCtx.Done():
			// This timer belongs only to this observation. Cancellation returns
			// immediately, so no later receiver can need a buffered value drained.
			timer.Stop()
			if sliceCtx.Err() == context.DeadlineExceeded {
				return PendingResult(result), nil
			}
			return failedCommitWaitResult(result, sliceCtx.Err().Error()), nil
		case <-timer.C:
		}
	}
}

// adoptCommitCheckPolicy applies one complete policy receipt. A failed read
// leaves the prior policy fields intact; expiry also preserves an existing
// observation reason. Freshness refusal stays with the caller because expected
// checks must report their failure before the cached receipt's freshness fence.
func adoptCommitCheckPolicy(ctx context.Context, result *PullRequestWaitResult, required []RequiredRemoteCheck, authority, freshnessAuthority, policyUnavailable, reason string) bool {
	if reason != "" {
		if ctx.Err() != context.DeadlineExceeded || strings.TrimSpace(result.Reason) == "" {
			result.Reason = "required-check authority is unavailable; terminal CI evidence is incomplete: " + reason
		}
		return false
	}
	result.RequiredChecks = required
	result.RequiredChecksAuthority = authority
	result.TargetFreshnessAuthority = freshnessAuthority
	result.PolicyAuthorityUnavailable = policyUnavailable
	return true
}

func reportPullRequestWaitProgress(options PullRequestWaitOptions, observation int, result PullRequestWaitResult, nextPoll time.Duration) {
	if options.Progress != nil {
		options.Progress(PullRequestWaitProgress{Observation: observation, Result: result, NextPoll: nextPoll})
	}
}

// WaitForPullRequestChecks retains the original internal seam for existing
// orchestrated PR flows while using the exact-commit waiter above.
func WaitForPullRequestChecks(ctx context.Context, options PullRequestWaitOptions) (PullRequestWaitResult, error) {
	if strings.TrimSpace(options.PullRequest) == "" {
		return PullRequestWaitResult{}, fmt.Errorf("pull request is required")
	}
	return WaitForCommitChecks(ctx, options)
}

func PendingResult(result PullRequestWaitResult) PullRequestWaitResult {
	result.Status = PullRequestWaitPending
	if strings.TrimSpace(result.Reason) == "" {
		result.Reason = "observed GitHub checks are still pending"
	}
	result.Reason += "; resume the same exact target identity in another foreground slice"
	return result
}

func failedCommitWaitResult(result PullRequestWaitResult, reason string) PullRequestWaitResult {
	result.Status = PullRequestWaitFailed
	result.Reason = reason
	return result
}

// CommitChecks inspects the check runs and commit statuses for an exact head.
func CommitChecks(ctx context.Context, options PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
	return commitChecks(ctx, options)
}

func commitChecks(ctx context.Context, options PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
	checks := make([]RemoteCheck, 0)
	pending := false
	if options.PullRequest != "" {
		// The pull request contributes its identity, not a second copy of its
		// checks: `gh pr checks` reports the check runs and statuses of the
		// pull request's head commit, which the two reads below already
		// enumerate from the API for this exact head. Asking again would be
		// the same check run twice on unchanged inputs, and it would ask it
		// through `gh pr checks --json`, which the installed 2.45 does not
		// have. What the pull request must still prove is that it points at
		// the head being waited on.
		view, err := ReadPullRequest(ctx, options.Repository, options.PullRequest)
		if err != nil {
			return nil, false, err.Error()
		}
		if !strings.EqualFold(view.Head.SHA, options.Head) {
			return nil, false, fmt.Sprintf(
				"pull request %s#%s now points at %s, not the head %s being waited on",
				options.Repository, options.PullRequest, worktreebranches.ShortSHA(view.Head.SHA), worktreebranches.ShortSHA(options.Head))
		}
	}
	runChecks, runPending, reason := commitCheckRuns(ctx, options)
	if reason != "" {
		return nil, false, reason
	}
	statusChecks, statusPending, reason := commitStatuses(ctx, options)
	if reason != "" {
		return nil, false, reason
	}
	checks = append(checks, runChecks...)
	checks = append(checks, statusChecks...)
	// An empty observed set is not itself pending. The caller can treat it as a
	// terminal direct-target receipt only after it has also enumerated an empty
	// required-check policy and completed an unchanged reread. PR mode remains
	// fail-closed through its nonempty strict-freshness requirement.
	pending = pending || runPending || statusPending
	sortRemoteChecks(checks)
	return checks, pending, ""
}

func sortRemoteChecks(checks []RemoteCheck) {
	sort.Slice(checks, func(i, j int) bool {
		if checks[i].Name == checks[j].Name {
			if checks[i].Bucket == checks[j].Bucket {
				if checks[i].Link == checks[j].Link {
					return checks[i].AppID < checks[j].AppID
				}
				return checks[i].Link < checks[j].Link
			}
			return checks[i].Bucket < checks[j].Bucket
		}
		return checks[i].Name < checks[j].Name
	})
}

func terminalChecksFingerprint(checks []RemoteCheck, required []RequiredRemoteCheck, authority, targetHead, freshnessAuthority string) string {
	var builder strings.Builder
	builder.WriteString(authority)
	builder.WriteString("\ntarget:")
	builder.WriteString(targetHead)
	builder.WriteString("\nfreshness:")
	builder.WriteString(freshnessAuthority)
	for _, expectation := range required {
		builder.WriteByte('\n')
		builder.WriteString("required:")
		builder.WriteString(expectation.Name)
		builder.WriteByte('@')
		fmt.Fprintf(&builder, "%d", expectation.IntegrationID)
	}
	for _, check := range checks {
		builder.WriteByte('\n')
		builder.WriteString(check.Name)
		builder.WriteByte('\x00')
		builder.WriteString(check.Bucket)
		builder.WriteByte('\x00')
		builder.WriteString(check.Link)
		builder.WriteByte('\x00')
		fmt.Fprintf(&builder, "%d", check.AppID)
		builder.WriteByte('\x00')
		fmt.Fprintf(&builder, "%d", check.CheckRunID)
	}
	return builder.String()
}

// remoteCheckExecuted reports whether check reflects a real run rather than
// GitHub's own "skipped"/"neutral" not-really-run conclusions. It no longer
// gates the wait itself (round 3 of sneat-dev/wb#591's red-team follow-up
// removed that strict mode — see missingRequiredChecks below); it now backs
// only the non-blocking deferred-validation-check-skipped finding the
// worktree-merge PR route records on its candidate/PR phase. A
// commit-status-derived check carries no Conclusion at all and is treated
// as executed: the commit-status API has no skip concept.
func remoteCheckExecuted(check RemoteCheck) bool {
	switch check.Conclusion {
	case "skipped", "neutral":
		return false
	default:
		return true
	}
}

// missingRequiredChecks reports every required check absent from checks,
// matched by name (and GitHub App ID, when the expectation names one),
// regardless of the matching check's own conclusion — including "skipped"
// or "neutral". GitHub branch protection's own evaluation is the gate for
// what "satisfied" means, including under a deferred-validation PR-route
// candidate; WB no longer second-guesses it here (round 3 of
// sneat-dev/wb#591's red-team follow-up reverted the round-2 strict mode,
// which broke on real-world skip patterns — see remoteCheckExecuted's own
// comment for what replaced it).
func failedObservedChecks(checks []RemoteCheck, pending *bool, waived ...string) bool {
	failed := false
	for _, check := range checks {
		switch check.Bucket {
		case "pass", "skipping":
		case "fail", "cancel":
			if checkIsWaived(check.Name, waived) {
				continue
			}
			failed = true
		default:
			*pending = true
		}
	}
	return failed
}

func checkIsWaived(checkName string, waived []string) bool {
	normalized := NormalizeCheckName(checkName)
	for _, w := range waived {
		if NormalizeCheckName(w) == normalized {
			return true
		}
	}
	return false
}

// NormalizeCheckName strips leading check-run: and status: prefixes and trims whitespace.
func NormalizeCheckName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "check-run:")
	name = strings.TrimPrefix(name, "status:")
	return strings.TrimSpace(name)
}

// Explicit direct-CI deferral waits on its named PR run and checks actually
// required by target policy. Other workflows on the same SHA are diagnostic
// noise: they cannot invalidate or stall this specific validation contract.
func relevantExpectedActionChecks(checks []RemoteCheck, expected *ExpectedActionChecks, required []RequiredRemoteCheck) []RemoteCheck {
	relevant := make([]RemoteCheck, 0, len(checks))
	for _, check := range checks {
		selectedRun := check.WorkflowID == expected.WorkflowID && check.WorkflowEvent == expected.Event &&
			check.WorkflowRunID > 0 && check.PullRequestNumber == expected.PullRequestNumber && check.PullRequestBase == expected.PullRequestBase
		if selectedRun || checkMatchesRequiredPolicy(check, required) {
			relevant = append(relevant, check)
		}
	}
	return relevant
}

func checkMatchesRequiredPolicy(check RemoteCheck, required []RequiredRemoteCheck) bool {
	name := strings.TrimPrefix(strings.TrimPrefix(check.Name, "check-run:"), "status:")
	for _, expectation := range required {
		if name == expectation.Name && (expectation.IntegrationID == 0 || check.AppID == expectation.IntegrationID) {
			return true
		}
	}
	return false
}

func missingRequiredChecks(checks []RemoteCheck, required []RequiredRemoteCheck) []string {
	observed := make(map[string][]RemoteCheck, len(checks))
	for _, check := range checks {
		name := strings.TrimSpace(check.Name)
		name = strings.TrimPrefix(name, "check-run:")
		name = strings.TrimPrefix(name, "status:")
		if name != "" {
			observed[name] = append(observed[name], check)
		}
	}
	missing := make([]string, 0)
	for _, expectation := range required {
		matched := false
		for _, check := range observed[expectation.Name] {
			if expectation.IntegrationID == 0 || check.AppID == expectation.IntegrationID {
				matched = true
				break
			}
		}
		if !matched {
			label := expectation.Name
			if expectation.IntegrationID != 0 {
				label += fmt.Sprintf(" (GitHub App %d)", expectation.IntegrationID)
			}
			missing = append(missing, label)
		}
	}
	return missing
}

// expectedActionCheckState is stricter than branch protection: local-suite
// deferral needs proof that named jobs actually ran in the intended workflow.
// A status context or a same-named job from another Actions run is not proof.
func expectedActionCheckState(checks []RemoteCheck, expected *ExpectedActionChecks) (missing, rejected []string) {
	if expected == nil {
		return nil, nil
	}
	for _, name := range expected.Names {
		found := false
		for _, check := range checks {
			if check.Name != "check-run:"+name || check.WorkflowID != expected.WorkflowID || check.WorkflowEvent != expected.Event || check.WorkflowRunID <= 0 ||
				check.PullRequestNumber != expected.PullRequestNumber || check.PullRequestBase != expected.PullRequestBase {
				continue
			}
			found = true
			if check.Bucket == "pending" {
				missing = append(missing, name)
			} else if check.Bucket != "pass" || check.Conclusion != "success" {
				rejected = append(rejected, name+" ("+check.Conclusion+")")
			}
		}
		if !found {
			missing = append(missing, name)
		}
	}
	return missing, rejected
}

// skippedOrNeutralRequiredChecks reports the names of every required check
// (matched by name and, when the expectation names one, GitHub App ID) whose
// matching observed check concluded "skipped" or "neutral" rather than
// actually running. It never affects landability — GitHub branch
// protection's own evaluation is the gate (missingRequiredChecks above) —
// it only backs the non-blocking "deferred-validation-check-skipped" finding
// the worktree-merge PR route records on its candidate/PR phase for a
// receipt whose local validation was deferred to CI (sneat-dev/wb#591 round
// 3 red-team follow-up).
func SkippedOrNeutralRequiredChecks(checks []RemoteCheck, required []RequiredRemoteCheck) []string {
	observed := make(map[string][]RemoteCheck, len(checks))
	for _, check := range checks {
		name := strings.TrimSpace(check.Name)
		name = strings.TrimPrefix(name, "check-run:")
		name = strings.TrimPrefix(name, "status:")
		if name != "" {
			observed[name] = append(observed[name], check)
		}
	}
	names := make([]string, 0)
	for _, expectation := range required {
		for _, check := range observed[expectation.Name] {
			if expectation.IntegrationID != 0 && check.AppID != expectation.IntegrationID {
				continue
			}
			if !remoteCheckExecuted(check) {
				names = append(names, expectation.Name)
			}
			break
		}
	}
	return names
}

// requiredChecksReceipt combines classic branch protection, every active
// repository/organization ruleset GitHub says applies to the target, and the
// PR's effective required-check view when one exists. A terminal result is not
// allowed when any authority cannot be read: an observed green snapshot can
// otherwise precede registration of a required workflow on the same SHA.
type requiredChecksCache struct {
	valid              bool
	branchChecks       []RequiredRemoteCheck
	freshnessAuthority string
	policyUnavailable  string
}

func requiredChecksReceipt(ctx context.Context, options PullRequestWaitOptions, cache *requiredChecksCache) ([]RequiredRemoteCheck, string, string, string, string) {
	required := map[string]RequiredRemoteCheck{}
	branchChecks := []RequiredRemoteCheck(nil)
	freshnessAuthority := ""
	policyUnavailable := ""
	if cache != nil && cache.valid {
		branchChecks = cache.branchChecks
		freshnessAuthority = cache.freshnessAuthority
		policyUnavailable = cache.policyUnavailable
	} else {
		var reason string
		branchChecks, freshnessAuthority, reason = RequiredChecks(ctx, options.Repository, options.Target, options.PullRequest != "" && !options.AllowUnfenced)
		if reason != "" {
			if !options.AllowUnfenced || !isGitHubPolicyPlan403(reason) {
				return nil, "", "", "", reason
			}
			branchChecks = nil
			freshnessAuthority = ""
			policyUnavailable = reason
		}
		if cache != nil {
			cache.valid = true
			cache.branchChecks = branchChecks
			cache.freshnessAuthority = freshnessAuthority
			cache.policyUnavailable = policyUnavailable
		}
	}
	for _, expectation := range branchChecks {
		addRequiredExpectation(required, expectation)
	}
	authority := "github-branch-protection+active-branch-rules"
	if options.PullRequest != "" {
		if reason := pullRequestTargetsBase(ctx, options.Repository, options.PullRequest, options.Target); reason != "" {
			return nil, "", "", "", reason
		}
		authority += "+pr-base-verified"
	}
	if policyUnavailable != "" {
		authority = "github-branch-policy-unavailable-under-allow-unfenced"
		if options.PullRequest != "" {
			authority += "+pr-base-verified"
		}
	}
	checks := make([]RequiredRemoteCheck, 0, len(required))
	for _, expectation := range required {
		checks = append(checks, expectation)
	}
	sortRequiredChecks(checks)
	return checks, authority, freshnessAuthority, policyUnavailable, ""
}

func isGitHubPolicyPlan403(reason string) bool {
	lower := strings.ToLower(reason)
	return strings.Contains(lower, "http 403") && (strings.Contains(lower, "branch protection") || strings.Contains(lower, "branch rules") || strings.Contains(lower, "required-status-check policy"))
}

// pullRequestTargetsBase proves the pull request is landing where the
// required-check policy was read from.
//
// It replaces `gh pr checks --required`, which the installed 2.45 does not
// support and which was in any case a second derivation of a fact WB already
// reads authoritatively: the required contexts come from the target's branch
// protection and active rulesets, enumerated above. A pull request whose base
// is some other branch is not covered by that policy at all, and that — not a
// re-listed context name — is the thing this check exists to catch.
func pullRequestTargetsBase(ctx context.Context, repository, pullRequest, target string) string {
	view, err := ReadPullRequest(ctx, repository, pullRequest)
	if err != nil {
		return fmt.Sprintf("read pull request %s#%s: %v", repository, pullRequest, err)
	}
	if !strings.EqualFold(strings.TrimSpace(view.Base.Ref), strings.TrimSpace(target)) {
		return fmt.Sprintf("pull request %s#%s targets %s, not %s, so the target's required-check policy does not govern it",
			repository, pullRequest, view.Base.Ref, target)
	}
	return ""
}

type githubBranchPolicyView struct {
	Protected  *bool `json:"protected"`
	Protection struct {
		RequiredStatusChecks *struct {
			Contexts []string `json:"contexts"`
			Checks   []struct {
				Context string `json:"context"`
				AppID   int64  `json:"app_id"`
			} `json:"checks"`
		} `json:"required_status_checks"`
	} `json:"protection"`
}

type githubRequiredStatusChecksPolicy struct {
	Strict   *bool    `json:"strict"`
	Contexts []string `json:"contexts"`
	Checks   []struct {
		Context string `json:"context"`
		AppID   int64  `json:"app_id"`
	} `json:"checks"`
}

type ActiveBranchRule struct {
	Type              string `json:"type"`
	RulesetSourceType string `json:"ruleset_source_type"`
	RulesetSource     string `json:"ruleset_source"`
	RulesetID         int64  `json:"ruleset_id"`
	Parameters        struct {
		StrictRequiredStatusChecksPolicy *bool `json:"strict_required_status_checks_policy"`
		RequiredStatusChecks             []struct {
			Context       string `json:"context"`
			IntegrationID int64  `json:"integration_id"`
		} `json:"required_status_checks"`
	} `json:"parameters"`
}

func RequiredChecks(ctx context.Context, repository, target string, requireServerFreshness bool) ([]RequiredRemoteCheck, string, string) {
	branchEndpoint := "repos/" + repository + "/branches/" + url.PathEscape(target)
	branchOutputRawResponse, branchErr := githubobserver.Get(ctx, githubobserver.GetRequest{Dir: "", Repository: strings.TrimSpace(repository), Target: strings.TrimSpace(target), Head: strings.TrimSpace(""), Endpoint: branchEndpoint, FreshWindow: 0})
	branchOutputRaw := branchOutputRawResponse.Body
	if branchErr != nil {
		return nil, "", fmt.Sprintf("read target branch protection for %s: %v", target, branchErr)
	}
	var branch githubBranchPolicyView
	if err := json.Unmarshal(branchOutputRaw, &branch); err != nil {
		return nil, "", fmt.Sprintf("decode target branch protection for %s: %v", target, err)
	}
	if branch.Protected == nil {
		return nil, "", fmt.Sprintf("target branch protection for %s omitted the protected policy receipt", target)
	}
	required := map[string]RequiredRemoteCheck{}
	freshnessAuthorities := make([]string, 0)
	if branch.Protection.RequiredStatusChecks != nil {
		contexts := branch.Protection.RequiredStatusChecks.Contexts
		checks := branch.Protection.RequiredStatusChecks.Checks
		classicStrict := false
		if requireServerFreshness {
			detailOutputRawResponse, detailErr := githubobserver.Get(ctx, githubobserver.GetRequest{Dir: "", Repository: strings.TrimSpace(repository), Target: strings.TrimSpace(target), Head: strings.TrimSpace(""), Endpoint: branchEndpoint + "/protection/required_status_checks", FreshWindow: 0})
			detailOutputRaw := detailOutputRawResponse.Body
			if detailErr != nil {
				if len(contexts) != 0 || len(checks) != 0 || !strings.Contains(strings.ToLower(detailErr.Error()), "http 404") {
					return nil, "", fmt.Sprintf("read authoritative required-status-check policy for %s: %v", target, detailErr)
				}
			} else {
				var detail githubRequiredStatusChecksPolicy
				if err := json.Unmarshal(detailOutputRaw, &detail); err != nil {
					return nil, "", fmt.Sprintf("decode authoritative required-status-check policy for %s: %v", target, err)
				}
				if detail.Strict == nil {
					return nil, "", fmt.Sprintf("authoritative required-status-check policy for %s omitted strict", target)
				}
				classicStrict = *detail.Strict
				contexts = detail.Contexts
				checks = detail.Checks
			}
		}
		for _, name := range contexts {
			if reason := addRequiredCheck(required, name, 0); reason != "" {
				return nil, "", "target branch protection " + reason
			}
		}
		for _, check := range checks {
			if reason := addRequiredCheck(required, check.Context, check.AppID); reason != "" {
				return nil, "", "target branch protection " + reason
			}
		}
		if classicStrict && len(contexts)+len(checks) > 0 {
			freshnessAuthorities = append(freshnessAuthorities, "classic strict required-status-check policy")
		}
	}

	pages, rulesErr := ActiveRules(ctx, repository, target)
	if rulesErr != nil {
		return nil, "", fmt.Sprintf("read active branch rules for target %s: %v", target, rulesErr)
	}
	for _, page := range pages {
		for _, rule := range page {
			ruleType := strings.TrimSpace(rule.Type)
			if ruleType == "" {
				return nil, "", fmt.Sprintf("active branch rule for target %s omitted its type", target)
			}
			source := fmt.Sprintf("ruleset %d (%s %s)", rule.RulesetID, rule.RulesetSourceType, rule.RulesetSource)
			switch {
			case ruleType == "required_status_checks":
				for _, check := range rule.Parameters.RequiredStatusChecks {
					if reason := addRequiredCheck(required, check.Context, check.IntegrationID); reason != "" {
						return nil, "", source + " " + reason
					}
				}
				if rule.Parameters.StrictRequiredStatusChecksPolicy != nil && *rule.Parameters.StrictRequiredStatusChecksPolicy && len(rule.Parameters.RequiredStatusChecks) > 0 {
					freshnessAuthorities = append(freshnessAuthorities, fmt.Sprintf("strict required-status-check ruleset %d", rule.RulesetID))
				}
			case ruleType == "merge_queue":
				if requireServerFreshness {
					return nil, "", fmt.Sprintf("%s requires merge-group check observation, which this source-head waiter does not yet implement", source)
				}
			case strings.Contains(ruleType, "workflow"):
				return nil, "", fmt.Sprintf("%s contains %s, whose expected check names GitHub does not expose in this receipt", source, ruleType)
			}
		}
	}

	checks := make([]RequiredRemoteCheck, 0, len(required))
	for _, expectation := range required {
		checks = append(checks, expectation)
	}
	sortRequiredChecks(checks)
	sort.Strings(freshnessAuthorities)
	return checks, strings.Join(freshnessAuthorities, "+"), ""
}

func addRequiredCheck(required map[string]RequiredRemoteCheck, value string, integrationID int64) string {
	name := strings.TrimSpace(value)
	if name == "" {
		return "contains a required status check with no context"
	}
	addRequiredExpectation(required, RequiredRemoteCheck{Name: name, IntegrationID: integrationID})
	return ""
}

func addRequiredExpectation(required map[string]RequiredRemoteCheck, expectation RequiredRemoteCheck) {
	key := expectation.Name
	if expectation.IntegrationID != 0 {
		key += fmt.Sprintf("\x00%d", expectation.IntegrationID)
		// A producer-pinned rule is stronger than an unpinned duplicate from
		// the branch summary or PR effective view.
		delete(required, expectation.Name)
	} else {
		for _, existing := range required {
			if existing.Name == expectation.Name && existing.IntegrationID != 0 {
				return
			}
		}
	}
	required[key] = expectation
}

func sortRequiredChecks(checks []RequiredRemoteCheck) {
	sort.Slice(checks, func(i, j int) bool {
		if checks[i].Name == checks[j].Name {
			return checks[i].IntegrationID < checks[j].IntegrationID
		}
		return checks[i].Name < checks[j].Name
	})
}

func commitCheckRuns(ctx context.Context, options PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
	outputResponse, err := githubobserver.Get(ctx, githubobserver.GetRequest{Dir: "", Repository: strings.TrimSpace(options.Repository), Target: strings.TrimSpace(options.Target), Head: strings.TrimSpace(options.Head), Endpoint: "repos/" + options.Repository + "/commits/" + options.Head + "/check-runs?per_page=100", FreshWindow: 0})
	output := outputResponse.Body
	if err != nil {
		return nil, false, err.Error()
	}
	var response githubCheckRunsResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, false, fmt.Sprintf("decode GitHub check runs for %s: %v", options.Head, err)
	}
	if response.TotalCount > len(response.CheckRuns) {
		return nil, false, fmt.Sprintf("GitHub returned only %d of %d observed check runs for %s; refusing an incomplete CI receipt", len(response.CheckRuns), response.TotalCount, options.Head)
	}
	actionsBySuite, latestActionsRuns, reason := ActionsRunsForHead(ctx, options)
	if reason != "" {
		return nil, false, reason
	}

	// GitHub retains jobs from historical Actions workflow executions on the
	// same commit. The Actions run receipt supplies the workflow, event, suite,
	// and created_at chronology needed to distinguish a replacement execution
	// from another workflow. Third-party check apps have no equivalent workflow
	// identity here, so retain all their observations rather than guessing.
	observedActionsRuns := make(map[githubActionsRunIdentity]int, len(latestActionsRuns))
	checks := make([]RemoteCheck, 0, len(response.CheckRuns)+len(latestActionsRuns))
	pending := false
	for _, check := range response.CheckRuns {
		var actionsRun ActionsRun
		if strings.EqualFold(check.App.Slug, "github-actions") {
			if check.ID <= 0 || check.CheckSuite.ID <= 0 {
				return nil, false, fmt.Sprintf("GitHub Actions check run %q omitted a positive check-run or check-suite ID", check.Name)
			}
			run, ok := actionsBySuite[check.CheckSuite.ID]
			if !ok {
				if options.ExpectedActionChecks != nil {
					continue // unrelated PR run excluded from this explicit contract
				}
				return nil, false, fmt.Sprintf("GitHub Actions check run %q names suite %d, which the exact-head workflow-run receipt omitted", check.Name, check.CheckSuite.ID)
			}
			identity := run.identity()
			latest := latestActionsRun(latestActionsRuns, identity)
			if run.ID != latest.ID {
				continue
			}
			actionsRun = run
			observedActionsRuns[identity]++
		}
		bucket := checkRunBucket(check.Status, check.Conclusion)
		observed := RemoteCheck{Name: "check-run:" + check.Name, Bucket: bucket, Conclusion: check.Conclusion, Link: check.HTMLURL, AppID: check.App.ID, CheckRunID: check.ID}
		if options.ExpectedActionChecks != nil {
			observed.WorkflowID, observed.WorkflowRunID, observed.WorkflowEvent = actionsRun.WorkflowID, actionsRun.ID, actionsRun.Event
			if RunIncludesPullRequest(actionsRun, options.ExpectedActionChecks.PullRequestNumber, options.ExpectedActionChecks.PullRequestBase) {
				observed.PullRequestNumber, observed.PullRequestBase = options.ExpectedActionChecks.PullRequestNumber, options.ExpectedActionChecks.PullRequestBase
			}
		}
		checks = append(checks, observed)
		if bucket != "pass" && bucket != "skipping" && bucket != "fail" && bucket != "cancel" {
			pending = true
		}
	}
	for _, run := range latestActionsRuns {
		bucket := checkRunBucket(run.Status, run.Conclusion)
		if observedActionsRuns[run.identity()] > 0 && (bucket == "pass" || bucket == "skipping" || bucket == "fail" || bucket == "cancel") {
			continue
		}
		observed := RemoteCheck{
			Name:       fmt.Sprintf("workflow-run:%d:%s", run.WorkflowID, run.Event),
			Bucket:     bucket,
			Conclusion: run.Conclusion,
			Link:       run.HTMLURL,
		}
		if options.ExpectedActionChecks != nil {
			observed.WorkflowID, observed.WorkflowRunID, observed.WorkflowEvent = run.WorkflowID, run.ID, run.Event
		}
		checks = append(checks, observed)
		if bucket != "pass" && bucket != "skipping" && bucket != "fail" && bucket != "cancel" {
			pending = true
		}
	}
	return checks, pending, ""
}

func ActionsRunsForHead(ctx context.Context, options PullRequestWaitOptions) (map[int64]ActionsRun, []ActionsRun, string) {
	endpoint := "repos/" + options.Repository + "/actions/runs?head_sha=" + url.QueryEscape(options.Head) + "&per_page=100"
	outputResponse, err := githubobserver.Get(ctx, githubobserver.GetRequest{Dir: "", Repository: strings.TrimSpace(options.Repository), Target: strings.TrimSpace(options.Target), Head: strings.TrimSpace(options.Head), Endpoint: endpoint, FreshWindow: 0})
	output := outputResponse.Body
	if err != nil {
		return nil, nil, err.Error()
	}
	var response githubActionsRunsResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, nil, fmt.Sprintf("decode GitHub Actions runs for %s: %v", options.Head, err)
	}
	if response.TotalCount > len(response.WorkflowRuns) {
		return nil, nil, fmt.Sprintf("GitHub returned only %d of %d Actions runs for %s; refusing an incomplete CI receipt", len(response.WorkflowRuns), response.TotalCount, options.Head)
	}
	bySuite := make(map[int64]ActionsRun, len(response.WorkflowRuns))
	latest := make(map[githubActionsRunIdentity]ActionsRun, len(response.WorkflowRuns))
	for _, run := range response.WorkflowRuns {
		if run.ID <= 0 || run.WorkflowID <= 0 || run.CheckSuiteID <= 0 || run.CreatedAt.IsZero() || strings.TrimSpace(run.Event) == "" {
			return nil, nil, fmt.Sprintf("GitHub Actions returned a malformed exact-head workflow-run identity for %s", options.Head)
		}
		if expected := options.ExpectedActionChecks; expected != nil && run.WorkflowID == expected.WorkflowID && run.Event == expected.Event &&
			(run.HeadSHA != options.Head || run.HeadBranch != options.Target || !RunIncludesPullRequest(run, expected.PullRequestNumber, expected.PullRequestBase)) {
			continue // another PR's run on the same SHA cannot satisfy this wait
		}
		if previous, ok := bySuite[run.CheckSuiteID]; ok && previous.ID != run.ID {
			return nil, nil, fmt.Sprintf("GitHub Actions suite %d maps to conflicting workflow runs %d and %d", run.CheckSuiteID, previous.ID, run.ID)
		}
		bySuite[run.CheckSuiteID] = run
		identity := run.identity()
		previous, ok := latest[identity]
		if !ok || run.CreatedAt.After(previous.CreatedAt) {
			latest[identity] = run
			continue
		}
		if run.CreatedAt.Equal(previous.CreatedAt) && run.ID != previous.ID {
			return nil, nil, fmt.Sprintf("GitHub Actions workflow %d event %q has ambiguous same-time runs %d and %d", run.WorkflowID, run.Event, previous.ID, run.ID)
		}
	}
	latestRuns := make([]ActionsRun, 0, len(latest))
	for _, run := range latest {
		latestRuns = append(latestRuns, run)
	}
	sort.Slice(latestRuns, func(i, j int) bool {
		if latestRuns[i].WorkflowID == latestRuns[j].WorkflowID {
			return latestRuns[i].Event < latestRuns[j].Event
		}
		return latestRuns[i].WorkflowID < latestRuns[j].WorkflowID
	})
	return bySuite, latestRuns, ""
}

func latestActionsRun(runs []ActionsRun, identity githubActionsRunIdentity) ActionsRun {
	for _, run := range runs {
		if run.identity() == identity {
			return run
		}
	}
	return ActionsRun{}
}

func commitStatuses(ctx context.Context, options PullRequestWaitOptions) ([]RemoteCheck, bool, string) {
	outputResponse, err := githubobserver.Get(ctx, githubobserver.GetRequest{Dir: "", Repository: strings.TrimSpace(options.Repository), Target: strings.TrimSpace(options.Target), Head: strings.TrimSpace(options.Head), Endpoint: "repos/" + options.Repository + "/commits/" + options.Head + "/status?per_page=100", FreshWindow: 0})
	output := outputResponse.Body
	if err != nil {
		return nil, false, err.Error()
	}
	var response githubCommitStatusResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, false, fmt.Sprintf("decode GitHub commit statuses for %s: %v", options.Head, err)
	}
	if response.TotalCount > len(response.Statuses) {
		return nil, false, fmt.Sprintf("GitHub returned only %d of %d commit statuses for %s; refusing an incomplete CI receipt", len(response.Statuses), response.TotalCount, options.Head)
	}
	// GitHub returns the latest status first. Retain one exact receipt per
	// status context; an older duplicate must not overrule its replacement.
	seen := make(map[string]bool, len(response.Statuses))
	checks := make([]RemoteCheck, 0, len(response.Statuses))
	pending := false
	for _, status := range response.Statuses {
		name := "status:" + strings.TrimSpace(status.Context)
		if name == "status:" {
			return nil, false, "GitHub commit status has no context"
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		bucket := commitStatusBucket(status.State)
		checks = append(checks, RemoteCheck{Name: name, Bucket: bucket, Link: status.TargetURL})
		if bucket == "pending" {
			pending = true
		}
	}
	return checks, pending, ""
}

// pullRequestIdentity reads the exact head and target a pull request currently
// points at, through the one GitHub surface every `gh` this fleet has seen
// supports. It used to ask `gh pr view --json`, a second dialect for a fact
// ReadPullRequest already carries.
func PullRequestIdentity(ctx context.Context, repository, pullRequest string) (string, string, string) {
	view, err := ReadPullRequest(ctx, repository, pullRequest)
	if err != nil {
		return "", "", err.Error()
	}
	head := strings.TrimSpace(view.Head.SHA)
	base := strings.TrimSpace(view.Base.Ref)
	if head == "" || base == "" {
		return "", "", "GitHub pull request view returned no exact head or target"
	}
	return head, base, ""
}

type githubReference struct {
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
}

func TargetHead(ctx context.Context, repository, target string) (string, string) {
	outputResponse, err := githubobserver.Get(ctx, githubobserver.GetRequest{Dir: "", Repository: strings.TrimSpace(repository), Target: strings.TrimSpace(target), Head: strings.TrimSpace(""), Endpoint: "repos/" + repository + "/git/ref/heads/" + target, FreshWindow: 0})
	output := outputResponse.Body
	if err != nil {
		return "", err.Error()
	}
	var reference githubReference
	if err := json.Unmarshal(output, &reference); err != nil || strings.TrimSpace(reference.Object.SHA) == "" {
		if err != nil {
			return "", fmt.Sprintf("decode target ref %s: %v", target, err)
		}
		return "", "GitHub target ref returned no SHA"
	}
	return strings.TrimSpace(reference.Object.SHA), ""
}

type githubCompareView struct {
	Status     string `json:"status"`
	BaseCommit struct {
		SHA string `json:"sha"`
	} `json:"base_commit"`
	MergeBaseCommit struct {
		SHA string `json:"sha"`
	} `json:"merge_base_commit"`
}

func ContainsTarget(ctx context.Context, repository, target, candidate string) (bool, string) {
	endpoint := "repos/" + repository + "/compare/" + target + "..." + candidate
	outputResponse, err := githubobserver.Get(ctx, githubobserver.GetRequest{Dir: "", Repository: strings.TrimSpace(repository), Target: strings.TrimSpace(target), Head: strings.TrimSpace(candidate), Endpoint: endpoint, FreshWindow: 0})
	output := outputResponse.Body
	if err != nil {
		return false, fmt.Sprintf("prove candidate ancestry against target %s: %v", target, err)
	}
	var comparison githubCompareView
	if err := json.Unmarshal(output, &comparison); err != nil {
		return false, fmt.Sprintf("decode candidate ancestry against target %s: %v", target, err)
	}
	status := strings.TrimSpace(comparison.Status)
	base := strings.TrimSpace(comparison.BaseCommit.SHA)
	mergeBase := strings.TrimSpace(comparison.MergeBaseCommit.SHA)
	if base == "" || mergeBase == "" {
		return false, fmt.Sprintf("GitHub comparison omitted base or merge-base SHA for target %s", target)
	}
	if base != target {
		return false, fmt.Sprintf("GitHub comparison returned base %s, want exact target %s", base, target)
	}
	switch status {
	case "ahead", "identical":
		if mergeBase != target {
			return false, fmt.Sprintf("GitHub comparison status %s did not use target %s as merge base (got %s)", status, target, mergeBase)
		}
		return true, ""
	case "behind", "diverged":
		return false, ""
	default:
		return false, fmt.Sprintf("GitHub comparison returned unsupported status %q for target %s and candidate %s", status, target, candidate)
	}
}

type githubCheckRunsResponse struct {
	TotalCount int              `json:"total_count"`
	CheckRuns  []githubCheckRun `json:"check_runs"`
}

type githubActionsRunsResponse struct {
	TotalCount   int          `json:"total_count"`
	WorkflowRuns []ActionsRun `json:"workflow_runs"`
}

type ActionsRun struct {
	ID           int64  `json:"id"`
	WorkflowID   int64  `json:"workflow_id"`
	HeadSHA      string `json:"head_sha"`
	HeadBranch   string `json:"head_branch"`
	PullRequests []struct {
		Number int `json:"number"`
		Base   struct {
			Ref string `json:"ref"`
		} `json:"base"`
	} `json:"pull_requests"`
	RunAttempt   int       `json:"run_attempt"`
	Event        string    `json:"event"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	CreatedAt    time.Time `json:"created_at"`
	HTMLURL      string    `json:"html_url"`
	CheckSuiteID int64     `json:"check_suite_id"`
}

func (run ActionsRun) identity() githubActionsRunIdentity {
	return githubActionsRunIdentity{WorkflowID: run.WorkflowID, Event: run.Event}
}

type githubActionsRunIdentity struct {
	WorkflowID int64
	Event      string
}

type githubCommitStatusResponse struct {
	TotalCount int                  `json:"total_count"`
	Statuses   []githubCommitStatus `json:"statuses"`
}

type githubCommitStatus struct {
	Context   string `json:"context"`
	State     string `json:"state"`
	TargetURL string `json:"target_url"`
}

type githubCheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	App        struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	} `json:"app"`
	CheckSuite struct {
		ID int64 `json:"id"`
	} `json:"check_suite"`
}

const (
	maxFailedJobLogLines      = 24
	maxFailedCheckAnnotations = 12
	maxFailureAnnotationPath  = 512
	maxFailureAnnotationText  = 1024
)

type githubCheckRunAnnotation struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Message   string `json:"message"`
	Level     string `json:"annotation_level"`
}

// processCompletedAnnotationMessage matches GitHub's own generic job-level
// annotation ("Process completed with exit code 1."), which every failed
// Actions job carries regardless of what actually failed. It is never useful
// on its own: keeping it would mean a checks-failed finding never reaches the
// real cause (a lint line, a test line) that a caller needs.
var processCompletedAnnotationMessage = regexp.MustCompile(`^Process completed with exit code \d+\.$`)

// usefulCheckRunAnnotation reports whether an annotation is worth keeping.
// GitHub's terminal check-run endpoint reports every Actions job's own
// generic "the process exited nonzero" annotation at path ".github" — not a
// source location — alongside real ones (a lint finding, a failed test's
// file and line). A "notice" (e.g. the ubuntu-latest runner-image migration
// notice every job on this fleet currently carries) is never the failure
// either.
func usefulCheckRunAnnotation(value githubCheckRunAnnotation) bool {
	if value.Level == "notice" {
		return false
	}
	if strings.TrimSpace(value.Path) == ".github" {
		return false
	}
	if processCompletedAnnotationMessage.MatchString(strings.TrimSpace(value.Message)) {
		return false
	}
	return true
}

// failedCheckDetails obtains one compact failed-step tail for each failed
// Actions job. Third-party check runs retain their precise link and an honest
// explanation rather than pretending their logs are available through Actions.
func failedCheckDetails(ctx context.Context, repository string, checks []RemoteCheck) []CIFailureDetail {
	details := make([]CIFailureDetail, 0)
	seenAnnotations := map[string]bool{}
	for _, check := range checks {
		if check.Bucket != "fail" && check.Bucket != "cancel" {
			continue
		}
		detail := CIFailureDetail{Check: check.Name, JobURL: check.Link}
		annotations, annotationErr := failedCheckAnnotations(ctx, repository, check.CheckRunID, seenAnnotations)
		if len(annotations) > 0 {
			detail.Annotations = annotations
			details = append(details, detail)
			continue
		}
		runID, jobID, ok := ActionsRunAndJob(check.Link)
		if !ok {
			detail.Reason = "GitHub Actions run/job identifiers were not available for this check"
			if annotationErr != nil {
				detail.Reason = "retrieve failed check annotations: " + annotationErr.Error() + "; " + detail.Reason
			}
			details = append(details, detail)
			continue
		}
		detail.RunURL = fmt.Sprintf("https://github.com/%s/actions/runs/%s", repository, runID)
		response := githubobserver.Execute(ctx, "", "run", "view", runID, "--repo", repository, "--job", jobID, "--log-failed")
		if response.Err != nil || response.ExitCode != 0 {
			detail.Reason = "retrieve failed-job log: " + strings.TrimSpace(string(response.Stderr))
			if detail.Reason == "retrieve failed-job log: " {
				if response.Err != nil {
					detail.Reason = "retrieve failed-job log: " + response.Err.Error()
				} else {
					detail.Reason = "retrieve failed-job log: GitHub command exited non-zero"
				}
			}
			if annotationErr != nil {
				detail.Reason = "retrieve failed check annotations: " + annotationErr.Error() + "; " + detail.Reason
			}
			details = append(details, detail)
			continue
		}
		detail.Excerpt = failedJobLogExcerpt(string(response.Stdout), maxFailedJobLogLines)
		if detail.Excerpt == "" {
			detail.Reason = "GitHub returned no failed-step log lines"
		}
		details = append(details, detail)
	}
	return details
}

// failedCheckAnnotations uses the terminal check-run endpoint, which GitHub
// makes available before an Actions workflow has completed and published job
// logs. A successful nonempty response is preferred to log retrieval so the
// caller can render a precise failure immediately.
func failedCheckAnnotations(ctx context.Context, repository string, checkRunID int64, seen map[string]bool) ([]CIFailureAnnotation, error) {
	if checkRunID <= 0 {
		return nil, nil
	}
	endpoint := fmt.Sprintf("repos/%s/check-runs/%d/annotations?per_page=100", repository, checkRunID)
	bodyResponse, err := githubobserver.Get(ctx, githubobserver.GetRequest{Dir: "", Repository: strings.TrimSpace(repository), Target: strings.TrimSpace(""), Head: strings.TrimSpace(""), Endpoint: endpoint, FreshWindow: 0})
	body := bodyResponse.Body
	if err != nil {
		return nil, err
	}
	var raw []githubCheckRunAnnotation
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode check-run annotations: %w", err)
	}
	// failure-level annotations are collected before warning-level ones (a
	// stable partition, not a full sort, so annotations from the same
	// producer keep GitHub's own relative order), because a caller reading
	// only the first annotation must see the failure, not an incidental
	// warning that happened to be reported first.
	var failures, warnings []CIFailureAnnotation
	for _, value := range raw {
		if !usefulCheckRunAnnotation(value) {
			continue
		}
		path := compactFailureAnnotation(value.Path, maxFailureAnnotationPath)
		message := compactFailureAnnotation(value.Message, maxFailureAnnotationText)
		if path == "" || value.StartLine <= 0 || message == "" {
			continue
		}
		annotation := CIFailureAnnotation{Path: path, StartLine: value.StartLine, EndLine: value.EndLine, Message: message}
		key := fmt.Sprintf("%s\x00%d\x00%d\x00%s", annotation.Path, annotation.StartLine, annotation.EndLine, annotation.Message)
		if seen[key] {
			continue
		}
		seen[key] = true
		if value.Level == "warning" {
			warnings = append(warnings, annotation)
		} else {
			failures = append(failures, annotation)
		}
	}
	annotations := append(failures, warnings...)
	if len(annotations) > maxFailedCheckAnnotations {
		annotations = annotations[:maxFailedCheckAnnotations]
	}
	return annotations, nil
}

func checkRunBucket(status, conclusion string) string {
	if status != "completed" {
		return "pending"
	}
	switch conclusion {
	case "success", "neutral":
		return "pass"
	case "skipped":
		return "skipping"
	case "cancelled", "timed_out", "action_required":
		return "cancel"
	case "failure", "startup_failure", "stale":
		return "fail"
	default:
		return "pending"
	}
}

func commitStatusBucket(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "success":
		return "pass"
	case "pending":
		return "pending"
	case "failure", "error":
		return "fail"
	default:
		return "pending"
	}
}
