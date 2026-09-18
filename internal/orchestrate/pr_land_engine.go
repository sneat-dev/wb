package orchestrate

import (
	"context"
	"fmt"

	"github.com/sneat-dev/wb/internal/progress"
)

// mergedByGitHubAutoMergeDetail is the candidate_checks phase's progress
// detail when the wait ends because GitHub's own armed auto-merge landed the
// pull request mid-wait — every observed check was green, so this is a
// success to report, never the generic wait-result status string
// ("failed"), which every one of GitHub's checks contradicts. See #600/#614.
const mergedByGitHubAutoMergeDetail = "merged by GitHub auto-merge"

// awaitLandablePullRequest is the shared wait-and-arm engine both `wb pr
// land` and the worktree-merge PR route drive to reach a green, mergeable
// head. It arms GitHub auto-merge first — unless doing so would bypass a
// guard the caller enforces (see autoMergeBypassesAGuard) — then loops
// bringing a candidate that is behind its target up to date and waiting for
// its checks, treating a GitHub merge that completes mid-wait (because
// auto-merge is armed) as the success it is rather than a moved target.
//
// evidence is mutated in place with the same keys `wb pr land` has always
// recorded ("auto_merge", "head", "updated_onto_target"); callers that do not
// want those recorded pass a throwaway map.
//
// options.headUpdated, when non-nil, is called after every successful
// update-branch and re-read with the previous and the updated head SHA. A
// non-nil error aborts the wait immediately and is returned as err — the
// caller decided the update cannot be trusted to continue on.
func awaitLandablePullRequest(
	ctx context.Context,
	options PullRequestLandOptions,
	view PullRequestView,
	number, subject, body string,
	evidence map[string]string,
) (updatedView PullRequestView, waited PullRequestWaitResult, autoMergeArmed, mergedByGitHub bool, refusal *landRefusal, err error) {
	if evidence == nil {
		evidence = map[string]string{}
	}
	updatedView = view

	// Arm auto-merge BEFORE waiting, not after — see the long comment on this
	// in the original `wb pr land` call site (pr_land.go) for why: everything
	// below can end without landing, and arming first means none of those
	// endings strand a complete change.
	if !options.NoAutoMerge {
		reportPullRequestLandProgress(options.OperationProgress, "arm_auto_merge", progress.Started, options.Repository+"#"+number, 0, 0)
		if bypassed := autoMergeBypassesAGuard(ctx, options, updatedView.Base.Ref); bypassed != "" {
			evidence["auto_merge"] = "not armed: " + bypassed
			reportPullRequestLandProgress(options.OperationProgress, "arm_auto_merge", progress.Completed, "skipped: "+bypassed, 0, 0)
		} else if armReason := enablePullRequestAutoMerge(ctx, options.Repository, number, options.MergeMethod, updatedView.Head.SHA, subject, body); armReason != "" {
			evidence["auto_merge"] = "not armed: " + armReason
			reportPullRequestLandProgress(options.OperationProgress, "arm_auto_merge", progress.Failed, armReason, 0, 0)
		} else {
			autoMergeArmed = true
			evidence["auto_merge"] = "armed before waiting"
			reportPullRequestLandProgress(options.OperationProgress, "arm_auto_merge", progress.Completed, "armed", 0, 0)
		}
	}

	updates := !options.NoUpdateBranch && len(options.KeepCommits) == 0
	pollInterval := options.CheckPollInterval
	if pollInterval <= 0 {
		pollInterval = DefaultCheckPollInterval
	}
	deadline := waitDeadline(options)
	for {
		remaining := remainingWaitBudget(options, deadline)
		// A budget no longer than one poll cannot observe anything; it is
		// spent, and spent is pending, not an error.
		if remaining <= pollInterval {
			waited = pendingCommitWaitResult(PullRequestWaitResult{
				Status: PullRequestWaitPending, Repository: options.Repository, PullRequest: number,
				Target: updatedView.Base.Ref, Head: updatedView.Head.SHA,
				Reason: "landing wait budget elapsed before checks settled",
			})
			return updatedView, waited, autoMergeArmed, false, nil, nil
		}

		if updates {
			behind, reason := candidateIsBehindTarget(ctx, options.Repository, updatedView.Base.Ref, updatedView.Head.SHA)
			if reason != "" && !isTransientReadReason(reason) {
				return updatedView, waited, autoMergeArmed, false, nil, fmt.Errorf("determine whether %s#%s is behind %s: %s", options.Repository, number, updatedView.Base.Ref, reason)
			}
			if behind {
				reportPullRequestLandProgress(options.OperationProgress, "update_branch", progress.Started, shortMergeRevision(updatedView.Head.SHA), 0, 0)
				previousHead := updatedView.Head.SHA
				updatedHead, updateReason := updatePullRequestBranch(ctx, options.Repository, number, updatedView.Head.SHA, options.OperationProgress)
				if updateReason != "" {
					if updateBranchConflict(updateReason) {
						// A conflict is the author's to resolve. Auto-merge
						// stays armed: once they resolve and push, CI runs
						// against the resolution and merges it if it passes.
						return updatedView, waited, autoMergeArmed, false, &landRefusal{
							code:    LandRefusalUpdateConflict,
							reason:  "candidate is behind " + updatedView.Base.Ref + " and updating it conflicts; resolving that is a judgement WB does not make for you: " + updateReason,
							command: "resolve the conflict on " + updatedView.Head.Ref + ", push, then: wb pr land " + options.Repository + "#" + number,
						}, nil
					}
					if !updateBranchHeadMoved(updateReason) {
						return updatedView, waited, autoMergeArmed, false, nil, fmt.Errorf("update %s#%s onto %s: %s", options.Repository, number, updatedView.Base.Ref, updateReason)
					}
					// Someone pushed between the read and the update: the
					// compare-and-swap did its job. Re-read and go round.
				}
				updatedView, err = ReadPullRequest(ctx, options.Repository, number)
				if err != nil {
					return updatedView, waited, autoMergeArmed, false, nil, err
				}
				evidence["head"] = shortMergeRevision(updatedView.Head.SHA)
				if updateReason != "" {
					continue
				}
				// Red-team finding M3: the re-read head MUST match what the
				// update-branch write itself reported advancing to. Without
				// this check, a force-push landing a crafted head [P, X] in
				// the window between the update-branch write and this re-read
				// could be recorded as a trusted update-branch advance (via
				// options.headUpdated below) even though nothing here ever
				// verified X's shape - bypassing the M-A guard the hook
				// otherwise enforces. Refuse rather than adopt an untraced
				// head; CI still gates whatever eventually lands.
				if updatedView.Head.SHA != updatedHead {
					return updatedView, waited, autoMergeArmed, false, &landRefusal{
						code: LandRefusalHeadMoved,
						reason: fmt.Sprintf(
							"update-branch reported the new head as %s but the re-read pull request head is %s; refusing to adopt an untraced advance",
							shortMergeRevision(updatedHead), shortMergeRevision(updatedView.Head.SHA)),
						command: "wb pr land " + options.Repository + "#" + number,
					}, nil
				}
				evidence["updated_onto_target"] = shortMergeRevision(updatedHead)
				if options.headUpdated != nil {
					// The worktree-merge PR route's own hook (M3's
					// persist-first ordering) owns fast-forwarding its
					// candidate worktree; calling #611/#613's local-worktree
					// sync here too would double-fast-forward the same
					// branch through two independent code paths.
					if hookErr := options.headUpdated(previousHead, updatedHead); hookErr != nil {
						return updatedView, waited, autoMergeArmed, false, nil, hookErr
					}
				} else if syncNote := syncLocalWorktreeAfterUpdateBranch(ctx, options, updatedView.Head.Ref, updatedHead); syncNote != "" {
					// The plain `wb pr land` route (red-team finding #611,
					// #613): once a server-side update-branch has advanced
					// the pull request's head, bring the local WB worktree
					// that holds this branch (if any) up to it too, so a
					// follow-up commit and push from that worktree are not
					// rejected as non-fast-forward.
					evidence["local_sync"] = syncNote
					reportPullRequestLandProgress(options.OperationProgress, "sync_worktree", progress.Completed, syncNote, 0, 0)
				}
				reportPullRequestLandProgress(options.OperationProgress, "update_branch", progress.Completed, shortMergeRevision(updatedView.Head.SHA), 0, 0)
				continue
			}
		}

		waitOptions := PullRequestWaitOptions{
			Repository:        options.Repository,
			PullRequest:       number,
			Target:            updatedView.Base.Ref,
			Head:              updatedView.Head.SHA,
			AllowUnfenced:     options.AllowUnfenced,
			Slice:             remaining,
			CheckPollInterval: options.CheckPollInterval,
			Progress:          options.Progress,
			OperationProgress: options.OperationProgress,
		}
		reportPullRequestLandProgress(options.OperationProgress, "candidate_checks", progress.Waiting, shortMergeRevision(updatedView.Head.SHA), 0, 0)
		// This wait can run the remaining budget in one call: keep the lane's
		// heartbeat fresh throughout so it never goes stale out from under
		// this still-live session. See startLandingLaneHeartbeat. A no-op
		// when options.Lane was never populated.
		stopLaneHeartbeat := startLandingLaneHeartbeat(options.ProjectsRoot, options.Repository, updatedView.Base.Ref, options.Lane.Owner.WBSessionID, 0)
		observed, waitErr := waitForPullRequestLandChecks(ctx, waitOptions)
		stopLaneHeartbeat()
		if waitErr != nil {
			return updatedView, waited, autoMergeArmed, false, nil, waitErr
		}
		waited = observed
		if waited.Status == PullRequestWaitPassed {
			reportPullRequestLandProgress(options.OperationProgress, "candidate_checks", progress.Completed, string(waited.Status), len(waited.Checks), len(waited.Checks))
			return updatedView, waited, autoMergeArmed, false, nil, nil
		}

		// With auto-merge armed, GitHub normally merges within seconds of the
		// checks going green — usually before the confirming observation —
		// and the wait then sees the target move past the head and reports
		// failure. That is the success path, not a red check: find out
		// before judging - and, crucially, before reporting the phase's
		// progress line below. Red-team finding (the #600 auto-merge label):
		// reporting "failed" here first and only discovering the GitHub
		// auto-merge afterwards is exactly what produced
		// "candidate checks: 10/10: failed" on a pull request every one of
		// whose checks was green and that GitHub had already merged.
		if autoMergeArmed {
			if merged, readErr := ReadPullRequest(ctx, options.Repository, number); readErr == nil && merged.Merged {
				reportPullRequestLandProgress(options.OperationProgress, "candidate_checks", progress.Completed, mergedByGitHubAutoMergeDetail, len(waited.Checks), len(waited.Checks))
				return updatedView, waited, autoMergeArmed, true, nil, nil
			}
		}
		reportPullRequestLandProgress(options.OperationProgress, "candidate_checks", progress.Completed, string(waited.Status), len(waited.Checks), len(waited.Checks))

		// The wait reports a target that moved under the head as a failure.
		// When updating is allowed that is not a verdict on the work: bring
		// it up to date and wait again.
		if updates && waited.Status == PullRequestWaitFailed && targetMovedUnderHead(waited.Reason) {
			if behind, reason := candidateIsBehindTarget(ctx, options.Repository, updatedView.Base.Ref, updatedView.Head.SHA); reason == "" && behind {
				reportPullRequestLandProgress(options.OperationProgress, "update_branch", progress.Started, "target advanced during the wait", 0, 0)
				continue
			}
		}
		return updatedView, waited, autoMergeArmed, false, nil, nil
	}
}

// mergeOrAdoptAutoMerge performs the merge write, or — when armed auto-merge
// already won the race to the same green head — adopts that as the landing
// instead of treating GitHub's refusal as an error.
//
// mergedByGitHub short-circuits straight to adopting: it means the caller's
// own wait already observed the pull request merged, so issuing a second
// merge write would only be refused for a reason that is not a finding.
func mergeOrAdoptAutoMerge(
	ctx context.Context,
	options PullRequestLandOptions,
	number, head, mergeMethod, subject, body string,
	autoMergeArmed, mergedByGitHub bool,
	evidence map[string]string,
) (mergeSHA string, refusal *landRefusal, err error) {
	if options.beforeMerge != nil {
		options.beforeMerge()
	}
	if mergedByGitHub {
		if evidence != nil {
			evidence["merged_by"] = "github auto-merge"
		}
		return "", nil, nil
	}
	reportPullRequestLandProgress(options.OperationProgress, "merge_pull_request", progress.Started, shortMergeRevision(head), 0, 0)
	merge, mergeRefused, mergeErr := mergePullRequest(ctx, options.Repository, number, head, mergeMethod, subject, body)
	if mergeErr != nil {
		return "", nil, mergeErr
	}
	if mergeRefused != nil {
		// Armed auto-merge can win the race to the same green head; a
		// refusal then means GitHub merged it, which is a landing.
		merged, readErr := ReadPullRequest(ctx, options.Repository, number)
		if !autoMergeArmed || readErr != nil || !merged.Merged {
			return "", mergeRefused, nil
		}
		if evidence != nil {
			evidence["merged_by"] = "github auto-merge"
		}
		return "", nil, nil
	}
	reportPullRequestLandProgress(options.OperationProgress, "merge_pull_request", progress.Completed, shortMergeRevision(merge), 0, 0)
	return merge, nil, nil
}
