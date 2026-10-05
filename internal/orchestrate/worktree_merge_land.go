package orchestrate

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// LandWorktreeMerge resumes a prepared receipt from its first incomplete
// boundary. It never reconstructs identity from a branch name and never
// force-pushes either the candidate or target branch.
func LandWorktreeMerge(ctx context.Context, options WorktreeMergeLandOptions) (WorktreeMergeReceipt, error) {
	return landWorktreeMerge(ctx, options, persistWorktreeMergeReceipt)
}

func landWorktreeMerge(ctx context.Context, options WorktreeMergeLandOptions, save func(WorktreeMergeReceipt) error) (WorktreeMergeReceipt, error) {
	reportWorktreeMergeProgress(options.Progress, "read_receipt", progress.Started, options.Receipt)
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	receipt, err := readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	if options.HostLoadAdmission != nil {
		receipt.HostLoadAdmission = options.HostLoadAdmission
	}
	// Captured before any phase transition below, since receipt.Phase is
	// unconditionally reassigned to "land" further down: this distinguishes
	// a true resume of an already-published land-phase receipt (the
	// land-phase re-validation gap this function closes) from an ordinary
	// first-ever transition from prepare into land, which must keep refusing
	// on a bare identity mismatch rather than silently re-validating it away.
	resumedFromLandPhase := receipt.Phase == WorktreeMergePhaseLand
	if options.StopBeforeMerge && options.Route != WorktreeMergeRoutePullRequest {
		return receipt, fmt.Errorf("stop-before-merge requires the pull-request route")
	}
	if options.StopBeforeMerge && options.Cleanup {
		return receipt, fmt.Errorf("stop-before-merge cannot clean managed assets before a landing receipt exists")
	}
	if acknowledged, ackErr := hasLandedFailureAcknowledgement(receipt); ackErr != nil {
		return receipt, ackErr
	} else if acknowledged {
		return receipt, fmt.Errorf("merge receipt %s was acknowledged as a historical landed failure; it cannot be replayed", receiptPath)
	}
	if superseded, supersessionErr := hasValidationFailureSupersession(ctx, options.ProjectsRoot, receipt); supersessionErr != nil {
		return receipt, supersessionErr
	} else if superseded {
		return receipt, fmt.Errorf("merge receipt %s was superseded by an audited replacement candidate; it cannot be replayed", receiptPath)
	}
	if rebatched, rebatchErr := hasPreparedWorktreeMergeRebatch(receipt); rebatchErr != nil {
		return receipt, rebatchErr
	} else if rebatched {
		return receipt, fmt.Errorf("merge receipt %s was rebatched into an audited replacement candidate; it cannot be replayed", receiptPath)
	}
	reportWorktreeMergeProgress(options.Progress, "read_receipt", progress.Completed, string(receipt.Status)+" at "+receiptPath)
	if receipt.Status == WorktreeMergeComplete {
		if err := normalizeCompletedWorktreeMergeReceipt(&receipt); err != nil {
			return receipt, err
		}
		return receipt, nil
	}
	// The landing-lane guard runs before any push, merge, or check
	// observation: a different live session already driving this
	// (repository, target) lane must be refused before this call does more
	// work it would otherwise have to strand. See LaneGuardRequest.
	if laneRecord, laneErr := acquireLandingLane(options.ProjectsRoot, receipt.Repository, receipt.Target, options.Lane); laneErr != nil {
		return receipt, laneErr
	} else if laneRecord.Owner.WBSessionID != "" {
		record := laneRecord
		receipt.LaneOwner = &record
	}
	if receipt.Candidate.Worktree == "" {
		return receipt, fmt.Errorf("receipt %s has no prepared candidate", receiptPath)
	}
	lockID := receipt.Lane
	if lockID == "" {
		lockID = worktreeMergeLaneID(receipt.Repository, receipt.Target)
	}
	lock, err := AcquireOperationLock(options.ProjectsRoot, lockID, true)
	if err != nil {
		return receipt, err
	}
	locked := true
	defer func() {
		if locked {
			_ = lock.Release()
		}
	}()
	if receipt.Status == WorktreeMergeLanded && receipt.LandingSHA != "" && receipt.Cleanup && options.Cleanup {
		ackPath := receipt.ReceiptPath + worktreeMergeMissingCleanupAcknowledgementSuffix
		if _, statErr := os.Stat(ackPath); statErr == nil {
			terminalized, recoveryErr := recoverAlreadyTerminalizedWorktreeMergeCleanup(ctx, options.ProjectsRoot, &receipt, options.Timeout, options.Retry)
			if recoveryErr != nil {
				return receipt, recoveryErr
			}
			if !terminalized {
				return receipt, fmt.Errorf("missing-cleanup acknowledgement %s did not prove every cleanup asset terminal", ackPath)
			}
			receipt.Status = WorktreeMergeComplete
			receipt.Failure = ""
			receipt.UpdatedAt = time.Now().UTC()
			if err := save(receipt); err != nil {
				return receipt, err
			}
			return receipt, nil
		} else if !os.IsNotExist(statErr) {
			return receipt, fmt.Errorf("inspect missing-cleanup acknowledgement %s: %w", ackPath, statErr)
		}
	}
	if receipt.PullRequest != "" && receipt.LandingSHA == "" {
		// Persist approved landing intent before remote-only recovery. If the
		// recovery succeeds, the recursive pass continues through the ordinary
		// post-target CI, canonical sync, and cleanup path with the same fence
		// approval instead of forgetting it after the PR has merged.
		if retainWorktreeMergeLandIntent(&receipt, &options) {
			receipt.UpdatedAt = time.Now().UTC()
			if err := save(receipt); err != nil {
				return receipt, err
			}
		}
		recovered, recoveryErr := recoverAlreadyMergedPublishedWorktreeMerge(ctx, &receipt)
		if recoveryErr != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, recoveryErr, save)
		}
		if recovered {
			if err := lock.Release(); err != nil {
				return receipt, err
			}
			locked = false
			return landWorktreeMerge(ctx, options, save)
		}
	}
	// Resolve the route, and whether local validation may be deferred to it,
	// exactly ONCE for this call — but LAZILY, on first actual use below,
	// rather than eagerly here. A network round trip (and its conservative
	// fallback for an explicit --route direct against an unreadable policy,
	// which is a hard refusal — see conservativeWorktreeMergePRRoute) must
	// never fire on a path that itself fails before reaching any validation
	// site or the publish guard (e.g. a target-drift rebase conflict caught
	// below): that would refuse work for a reason this call never needed to
	// reach. Every validation site below and the publish/landing guard that
	// follows them share planHolder, so whichever of them runs first performs
	// the one resolution and every later one reuses it (M7, B1:
	// sneat-dev/wb#591).
	applyRecordedWorktreeMergeRouteBeforeFirstResolve(&receipt, &options)
	planHolder := &worktreeMergeValidationPlanHolder{}
	if receipt.Candidate.SHA == "" {
		recovered, recoverErr := recoverResolvedWorktreeMergeCandidate(ctx, options.ProjectsRoot, &receipt, options.Timeout, options.Retry)
		if recoverErr != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, recoverErr, save)
		}
		if recovered {
			reportWorktreeMergeProgress(options.Progress, "recover_candidate", progress.Started, shortMergeRevision(receipt.Candidate.SHA))
			checkTimeout, shardAttemptTimeout := receiptWorktreeMergeValidationTimeouts(receipt)
			plan, planErr := planHolder.resolve(ctx, receipt.Repository, receipt.Target, options.Route, options.ValidateLocally, options.AllowUnfenced || receipt.AllowUnfenced, options.DirectCIPullRequest)
			if planErr != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, planErr, save)
			}
			if validationErr := applyOrDeferWorktreeMergeValidation(ctx, &receipt, plan, options.Timeout, options.Retry, checkTimeout, shardAttemptTimeout, options.Progress); validationErr != nil {
				// Keep retry truthful: the resolved head is present in the
				// validation report, but it is not a prepared candidate until
				// that validation succeeds. A later resume must recover and
				// validate the exact head again.
				receipt.Candidate.SHA = ""
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeValidationFailed, fmt.Errorf("recovered candidate validation failed: %w", validationErr), save)
			}
			receipt.Status = WorktreeMergePrepared
			receipt.Failure = ""
			receipt.UpdatedAt = time.Now().UTC()
			if err := save(receipt); err != nil {
				return receipt, err
			}
			reportWorktreeMergeProgress(options.Progress, "recover_candidate", progress.Completed, shortMergeRevision(receipt.Candidate.SHA))
		}
	}
	if receipt.Status == WorktreeMergePreparing {
		if err := applyWorktreeMergeValidationLimits(&receipt, options, save); err != nil {
			return receipt, err
		}
		if err := validatePreparingWorktreeMergeCandidateWithRunner(ctx, options.resolveRunner(), receipt); err != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
		}
		reportWorktreeMergeProgress(options.Progress, "validate_candidate", progress.Started, shortMergeRevision(receipt.Candidate.SHA))
		checkTimeout, shardAttemptTimeout := receiptWorktreeMergeValidationTimeouts(receipt)
		plan, planErr := planHolder.resolve(ctx, receipt.Repository, receipt.Target, options.Route, options.ValidateLocally, options.AllowUnfenced || receipt.AllowUnfenced, options.DirectCIPullRequest)
		if planErr != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, planErr, save)
		}
		validationContext := ctx
		cancelValidation := func() {}
		if options.PrepareTimeout > 0 {
			validationContext, cancelValidation = context.WithTimeout(ctx, options.PrepareTimeout)
		}
		validationErr := applyOrDeferWorktreeMergeValidation(validationContext, &receipt, plan, options.Timeout, options.Retry, checkTimeout, shardAttemptTimeout, options.Progress)
		cancelValidation()
		if validationErr != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeValidationFailed, fmt.Errorf("interrupted candidate validation failed: %w", validationErr), save)
		}
		receipt.Status = WorktreeMergePrepared
		receipt.Failure = ""
		receipt.UpdatedAt = time.Now().UTC()
		if err := save(receipt); err != nil {
			return receipt, err
		}
		reportWorktreeMergeProgress(options.Progress, "validate_candidate", progress.Completed, string(receipt.Validation.Status))
	}
	if receipt.Phase == WorktreeMergePhasePrepare && receipt.Status == WorktreeMergeValidationFailed && receipt.Candidate.SHA != "" {
		// A prepare/validation_failed receipt must never reach publish without
		// proving the exact candidate SHA again. Earlier code only re-ran
		// validation for a "preparing" receipt; a receipt that had already
		// recorded a failed validation fell through untouched and could reach
		// the push/PR logic below with validation.status still "failed"
		// (observed for receipts merge-sneat-dev-wb-main-1cbbf49dd60f-40222b81bf14
		// and merge-sneat-dev-wb-main-...-35e45d0d254e). Resume closes that gap
		// by re-validating here, before any conflict-advance or publish step.
		if err := applyWorktreeMergeValidationLimits(&receipt, options, save); err != nil {
			return receipt, err
		}
		if err := requireCleanMergeWorktreeWithRunner(ctx, options.resolveRunner(), receipt.Candidate.Worktree); err != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, fmt.Errorf("validation_failed candidate is not safely resumable: %w", err), save)
		}
		head, headErr := mergeRevision(ctx, options.resolveRunner(), receipt.Candidate.Worktree, "HEAD")
		if headErr != nil || head != receipt.Candidate.SHA {
			if headErr == nil {
				headErr = fmt.Errorf("candidate head drifted from %s to %s", receipt.Candidate.SHA, head)
			}
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, headErr, save)
		}
		reportWorktreeMergeProgress(options.Progress, "revalidate_candidate", progress.Started, shortMergeRevision(receipt.Candidate.SHA))
		checkTimeout, shardAttemptTimeout := receiptWorktreeMergeValidationTimeouts(receipt)
		plan, planErr := planHolder.resolve(ctx, receipt.Repository, receipt.Target, options.Route, options.ValidateLocally, options.AllowUnfenced || receipt.AllowUnfenced, options.DirectCIPullRequest)
		if planErr != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, planErr, save)
		}
		validationContext := ctx
		cancelValidation := func() {}
		if options.PrepareTimeout > 0 {
			validationContext, cancelValidation = context.WithTimeout(ctx, options.PrepareTimeout)
		}
		validationErr := applyOrDeferWorktreeMergeValidation(validationContext, &receipt, plan, options.Timeout, options.Retry, checkTimeout, shardAttemptTimeout, options.Progress)
		cancelValidation()
		if validationErr != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeValidationFailed, fmt.Errorf("resumed validation_failed candidate re-validation failed: %w", validationErr), save)
		}
		receipt.Status = WorktreeMergePrepared
		receipt.Failure = ""
		receipt.UpdatedAt = time.Now().UTC()
		if err := save(receipt); err != nil {
			return receipt, err
		}
		reportWorktreeMergeProgress(options.Progress, "revalidate_candidate", progress.Completed, string(receipt.Validation.Status))
	}
	advanced, advanceErr := advanceResolvedConflictWorktreeMergeCandidate(ctx, options.ProjectsRoot, &receipt, options.Timeout, options.Retry)
	if advanceErr != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, advanceErr, save)
	}
	if advanced {
		reportWorktreeMergeProgress(options.Progress, "recover_candidate", progress.Started, shortMergeRevision(receipt.Candidate.SHA))
		if err := save(receipt); err != nil {
			return receipt, err
		}
	}
	advancedNeedsValidation, advanceValidationErr := conflictCandidateAdvanceNeedsValidation(receipt)
	if advanceValidationErr != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, advanceValidationErr, save)
	}
	if advanced || advancedNeedsValidation {
		checkTimeout, shardAttemptTimeout := receiptWorktreeMergeValidationTimeouts(receipt)
		plan, planErr := planHolder.resolve(ctx, receipt.Repository, receipt.Target, options.Route, options.ValidateLocally, options.AllowUnfenced || receipt.AllowUnfenced, options.DirectCIPullRequest)
		if planErr != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, planErr, save)
		}
		if validationErr := applyOrDeferWorktreeMergeValidation(ctx, &receipt, plan, options.Timeout, options.Retry, checkTimeout, shardAttemptTimeout, options.Progress); validationErr != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeValidationFailed, fmt.Errorf("advanced conflict candidate validation failed: %w", validationErr), save)
		}
		receipt.Status = WorktreeMergePrepared
		receipt.Failure = ""
		receipt.UpdatedAt = time.Now().UTC()
		if err := save(receipt); err != nil {
			return receipt, err
		}
		reportWorktreeMergeProgress(options.Progress, "recover_candidate", progress.Completed, shortMergeRevision(receipt.Candidate.SHA))
	}
	if retainWorktreeMergeLandIntent(&receipt, &options) {
		receipt.UpdatedAt = time.Now().UTC()
		if err := save(receipt); err != nil {
			return receipt, err
		}
	}
	if receipt.PullRequest != "" && receipt.LandingSHA == "" {
		// Adopt an unrecorded server-side update-branch advance BEFORE the
		// local-advance detection below: that detection reasons about the
		// local candidate worktree's own HEAD, which a server-side update
		// never touches, and would otherwise never see this advance at all.
		// See adoptServerUpdatedWorktreeMergeHead (M5) and
		// adoptWorktreeMergeUpdateBranchAdvance's M3 persist-first ordering,
		// whose crash window this closes.
		adopted, _ := adoptServerUpdatedWorktreeMergeHead(ctx, options.resolveGit(), options.resolveRunner(), &receipt)
		if adopted {
			receipt.UpdatedAt = time.Now().UTC()
			if err := save(receipt); err != nil {
				return receipt, err
			}
			reportWorktreeMergeProgress(options.Progress, "recover_candidate", progress.Completed, shortMergeRevision(receipt.Candidate.SHA))
		}
	}
	if receipt.PullRequest != "" && receipt.LandingSHA == "" {
		advanced, advanceErr := advancePublishedWorktreeMergeCandidate(ctx, options.resolveGit(), options.resolveRunner(), &receipt)
		if advanceErr != nil {
			if IsTransientGitHubFailure(advanceErr) {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeChecksPending,
					fmt.Errorf("%w; resume with wb worktree merge resume %s", advanceErr, receipt.ReceiptPath), save)
			}
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, advanceErr, save)
		}
		if advanced {
			receipt.UpdatedAt = time.Now().UTC()
			if err := save(receipt); err != nil {
				return receipt, err
			}
			reportWorktreeMergeProgress(options.Progress, "recover_candidate", progress.Completed, shortMergeRevision(receipt.Candidate.SHA))
		}
	}
	if receipt.PullRequest != "" && receipt.LandingSHA == "" {
		serverLanding, merged, observeErr := pullRequestLandingReceipt(ctx, receipt, options)
		if observeErr != nil {
			if IsTransientGitHubFailure(observeErr) {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeChecksPending,
					fmt.Errorf("%w; resume with wb worktree merge resume %s", observeErr, receipt.ReceiptPath), save)
			}
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, observeErr, save)
		}
		if merged {
			remoteTarget, fetchErr := fetchExactMergeTargetWithRunner(ctx, options.resolveRunner(), receipt.Candidate.Worktree, receipt.Target)
			if fetchErr != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, fetchErr, save)
			}
			containsLanding, ancestorErr := isMergeAncestorWithRunner(ctx, options.resolveRunner(), receipt.Candidate.Worktree, serverLanding, remoteTarget)
			if ancestorErr != nil || !containsLanding {
				if ancestorErr == nil {
					ancestorErr = fmt.Errorf("exact remote target %s does not contain already-merged pull-request result %s", remoteTarget, serverLanding)
				}
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, ancestorErr, save)
			}
			receipt.LandingSHA = remoteTarget
			// Candidate PR checks prove the pre-merge head only. Force the
			// recursive landed pass to obtain fresh target-check evidence.
			receipt.Checks = PullRequestWaitResult{}
			receipt.Status = WorktreeMergeLanded
			receipt.UpdatedAt = time.Now().UTC()
			if persistErr := save(receipt); persistErr != nil {
				return receipt, persistErr
			}
			if releaseErr := lock.Release(); releaseErr != nil {
				return receipt, releaseErr
			}
			locked = false
			return landWorktreeMerge(ctx, options, save)
		}
	}
	if receipt.LandingSHA != "" {
		if receipt.Checks.Status != PullRequestWaitPassed || receipt.ValidationDeferral != nil && receipt.ValidationDeferral.Route == WorktreeMergeRouteDirect {
			reportWorktreeMergeProgress(options.Progress, "target_checks", progress.Waiting, shortMergeRevision(receipt.LandingSHA))
			postChecks, postErr := waitForWorktreeMergeChecks(ctx, receipt, options, "", receipt.LandingSHA, true)
			receipt.Checks = postChecks
			if postErr != nil {
				status := WorktreeMergePostTargetCIFailed
				if postChecks.Status == PullRequestWaitPending {
					status = WorktreeMergeChecksPending
				}
				return failWorktreeMergeReceiptWithSave(receipt, status, postErr, save)
			}
		}
		if receipt.CanonicalSync != "fast_forwarded" && receipt.CanonicalSync != "not_checked_out" {
			reportWorktreeMergeProgress(options.Progress, "sync_canonical", progress.Started, receipt.Target+"@"+shortMergeRevision(receipt.LandingSHA))
			canonical, canonicalErr := worktrees.CanonicalRepositoryPath(options.ProjectsRoot, receipt.Repository)
			if canonicalErr != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeCanonicalSyncBlocked, canonicalErr, save)
			}
			receipt.CanonicalSync, err = syncCanonicalMergeTarget(ctx, canonical, receipt.Target, receipt.LandingSHA, options.Timeout, options.Retry, options.CheckoutUpdated)
			if err != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeCanonicalSyncBlocked, err, save)
			}
		}
		reportWorktreeMergeProgress(options.Progress, "sync_canonical", progress.Completed, receipt.CanonicalSync)
		reportWorktreeMergeProgress(options.Progress, "reconcile_source_prs", progress.Started, "discovering exact absorbed heads")
		if err := reconcileAbsorbedSourcePullRequests(ctx, options.ProjectsRoot, &receipt, options.Timeout, options.Retry, options.Progress); err != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeLanded, err, save)
		}
		reportWorktreeMergeProgress(options.Progress, "reconcile_source_prs", progress.Completed, fmt.Sprintf("%d pull requests", len(receipt.SourcePullRequests)))
		receipt.Status = WorktreeMergeLanded
		receipt.Cleanup = receipt.Cleanup || options.Cleanup
		receipt.UpdatedAt = time.Now().UTC()
		if err := save(receipt); err != nil {
			return receipt, err
		}
		if !receipt.Cleanup {
			reportWorktreeMergeProgress(options.Progress, "landed", progress.Completed, receipt.ReceiptPath)
			return receipt, nil
		}
		reportWorktreeMergeProgress(options.Progress, "cleanup", progress.Started, strings.Join(sortedUniqueMergeTasks(receipt), ", "))
		if terminalized, terminalErr := recoverAlreadyTerminalizedWorktreeMergeCleanup(ctx, options.ProjectsRoot, &receipt, options.Timeout, options.Retry); terminalErr != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeLanded, terminalErr, save)
		} else if terminalized {
			reportWorktreeMergeProgress(options.Progress, "cleanup", progress.Completed, strings.Join(receipt.CleanedTasks, ", "))
			receipt.Status = WorktreeMergeComplete
			receipt.Failure = ""
			receipt.UpdatedAt = time.Now().UTC()
			if err := save(receipt); err != nil {
				return receipt, err
			}
			return receipt, nil
		}
		if err := lock.Release(); err != nil {
			return receipt, err
		}
		locked = false
		if err := cleanupWorktreeMergeAssets(ctx, options.ProjectsRoot, &receipt); err != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeLanded, err, save)
		}
		reportWorktreeMergeProgress(options.Progress, "cleanup", progress.Completed, strings.Join(receipt.CleanedTasks, ", "))
		receipt.Status = WorktreeMergeComplete
		receipt.Failure = ""
		receipt.UpdatedAt = time.Now().UTC()
		if err := save(receipt); err != nil {
			return receipt, err
		}
		return receipt, nil
	}

	if err := requireCleanMergeWorktreeWithRunner(ctx, options.resolveRunner(), receipt.Candidate.Worktree); err != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
	}
	head, err := mergeRevision(ctx, options.resolveRunner(), receipt.Candidate.Worktree, "HEAD")
	if err != nil || head != receipt.Candidate.SHA {
		if err == nil {
			err = fmt.Errorf("candidate head drifted from %s to %s", receipt.Candidate.SHA, head)
		}
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
	}
	if err := recheckWorktreeMergeSourcesWithRunner(ctx, options.resolveRunner(), receipt.Sources); err != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
	}

	remoteTarget, err := fetchExactMergeTargetWithRunner(ctx, options.resolveRunner(), receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
	}
	reportWorktreeMergeProgress(options.Progress, "refresh_target", progress.Completed, receipt.Target+"@"+shortMergeRevision(remoteTarget))
	if options.StopBeforeMerge && remoteTarget != receipt.TargetSHA {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, fmt.Errorf("target drifted from recorded %s to %s; refusing to republish preserved candidate %s", receipt.TargetSHA, remoteTarget, receipt.Candidate.SHA), save)
	}
	containsTarget, err := isMergeAncestorWithRunner(ctx, options.resolveRunner(), receipt.Candidate.Worktree, remoteTarget, receipt.Candidate.SHA)
	if err != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
	}
	if !containsTarget {
		if receipt.PullRequest != "" {
			reportWorktreeMergeProgress(options.Progress, "refresh_published_candidate", progress.Started, receipt.Target+"@"+shortMergeRevision(remoteTarget))
			if err := refreshPublishedWorktreeMergeCandidateTarget(ctx, &receipt, remoteTarget, options.Timeout, options.Retry); err != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
			}
			receipt.UpdatedAt = time.Now().UTC()
			if err := save(receipt); err != nil {
				return receipt, err
			}
			reportWorktreeMergeProgress(options.Progress, "refresh_published_candidate", progress.Completed, shortMergeRevision(receipt.Candidate.SHA))
			if err := requireCleanMergeWorktreeWithRunner(ctx, options.resolveRunner(), receipt.Candidate.Worktree); err != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
			}
			reportWorktreeMergeProgress(options.Progress, "validate_refreshed_candidate", progress.Started, shortMergeRevision(receipt.Candidate.SHA))
			checkTimeout, shardAttemptTimeout := receiptWorktreeMergeValidationTimeouts(receipt)
			plan, planErr := planHolder.resolve(ctx, receipt.Repository, receipt.Target, options.Route, options.ValidateLocally, options.AllowUnfenced || receipt.AllowUnfenced, options.DirectCIPullRequest)
			if planErr != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, planErr, save)
			}
			if validationErr := applyOrDeferWorktreeMergeValidation(ctx, &receipt, plan, options.Timeout, options.Retry, checkTimeout, shardAttemptTimeout, options.Progress); validationErr != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeValidationFailed, fmt.Errorf("candidate validation failed after refreshing published candidate to target %s: %w", remoteTarget, validationErr), save)
			}
			reportWorktreeMergeProgress(options.Progress, "validate_refreshed_candidate", progress.Completed, string(receipt.Validation.Status))
		} else {
			preparedCandidate, preparedTarget := receipt.Candidate.SHA, receipt.TargetSHA
			containsPreparedTarget, ancestorErr := isMergeAncestorWithRunner(ctx, options.resolveRunner(), receipt.Candidate.Worktree, preparedTarget, preparedCandidate)
			if ancestorErr != nil || !containsPreparedTarget {
				if ancestorErr == nil {
					ancestorErr = fmt.Errorf("prepared candidate %s no longer contains recorded target %s", preparedCandidate, preparedTarget)
				}
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, ancestorErr, save)
			}
			reportWorktreeMergeProgress(options.Progress, "rebase_candidate", progress.Started, shortMergeRevision(remoteTarget))
			if _, _, err := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, receipt.Candidate.Worktree,
				"git", "rebase", "--rebase-merges", "--onto", remoteTarget, preparedTarget, receipt.Candidate.Branch); err != nil {
				_, _, _ = runCommand(ctx, options.resolveRunner(), options.Timeout, 0, receipt.Candidate.Worktree, "git", "rebase", "--abort")
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, fmt.Errorf("target drift conflicts while rebasing the isolated candidate onto %s: %w", remoteTarget, err), save)
			}
			receipt.Candidate.SHA, err = mergeRevision(ctx, options.resolveRunner(), receipt.Candidate.Worktree, "HEAD")
			if err != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
			}
			receipt.TargetSHA = remoteTarget
			receipt.Rebase = &WorktreeMergeRebaseReceipt{
				CandidateBefore: preparedCandidate, TargetBefore: preparedTarget,
				TargetAfter: remoteTarget, CandidateAfter: receipt.Candidate.SHA,
			}
			receipt.UpdatedAt = time.Now().UTC()
			if err := save(receipt); err != nil {
				return receipt, err
			}
			if err := requireCleanMergeWorktreeWithRunner(ctx, options.resolveRunner(), receipt.Candidate.Worktree); err != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
			}
			reportWorktreeMergeProgress(options.Progress, "validate_rebased_candidate", progress.Started, shortMergeRevision(receipt.Candidate.SHA))
			checkTimeout, shardAttemptTimeout := receiptWorktreeMergeValidationTimeouts(receipt)
			plan, planErr := planHolder.resolve(ctx, receipt.Repository, receipt.Target, options.Route, options.ValidateLocally, options.AllowUnfenced || receipt.AllowUnfenced, options.DirectCIPullRequest)
			if planErr != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, planErr, save)
			}
			if validationErr := applyOrDeferWorktreeMergeValidation(ctx, &receipt, plan, options.Timeout, options.Retry, checkTimeout, shardAttemptTimeout, options.Progress); validationErr != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeValidationFailed, fmt.Errorf("candidate validation failed after incorporating target drift: %w", validationErr), save)
			}
		}
	}
	if options.StopBeforeMerge {
		// StopBeforeMerge requires the pull-request route (checked at the top
		// of this function), so resolving the plan here and stamping
		// receipt.Route before the reuse check lets preparedValidationStillValid
		// recognize a matching PR-route deferral from an earlier call in this
		// same receipt's history, without waiting for the later route
		// resolution/publish guard below.
		plan, planErr := planHolder.resolve(ctx, receipt.Repository, receipt.Target, options.Route, options.ValidateLocally, options.AllowUnfenced || receipt.AllowUnfenced, options.DirectCIPullRequest)
		if planErr != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, planErr, save)
		}
		receipt.Route = plan.Route
		checkTimeout, _ := receiptWorktreeMergeValidationTimeouts(receipt)
		reusable, identityErr := preparedValidationStillValidContext(ctx, receipt, plan, options.Timeout, options.Retry, checkTimeout)
		if identityErr != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, fmt.Errorf("recheck prepared validation identity: %w", identityErr), save)
		}
		if !reusable {
			reportWorktreeMergeProgress(options.Progress, "validate_preserved_candidate", progress.Started, shortMergeRevision(receipt.Candidate.SHA))
			checkTimeout, shardAttemptTimeout := receiptWorktreeMergeValidationTimeouts(receipt)
			if validationErr := applyOrDeferWorktreeMergeValidation(ctx, &receipt, plan, options.Timeout, options.Retry, checkTimeout, shardAttemptTimeout, options.Progress); validationErr != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeValidationFailed, fmt.Errorf("preserved candidate validation failed: %w", validationErr), save)
			}
			reportWorktreeMergeProgress(options.Progress, "validate_preserved_candidate", progress.Completed, string(receipt.Validation.Status))
		} else {
			reportWorktreeMergeProgress(options.Progress, "validate_preserved_candidate", progress.Completed, "reused exact prepared validation")
		}
	}

	// Reuse the route this call already resolved ONCE, above, rather than
	// re-querying GitHub: every validation site in this call and this
	// publish/landing guard must observe the same decision (M7, B1:
	// sneat-dev/wb#591).
	plan, planErr := planHolder.resolve(ctx, receipt.Repository, receipt.Target, options.Route, options.ValidateLocally, options.AllowUnfenced || receipt.AllowUnfenced, options.DirectCIPullRequest)
	if planErr != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, planErr, save)
	}
	decision := plan.Route
	if decision.Route == WorktreeMergeRouteUnsupported {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, fmt.Errorf("unsupported target policy: %s", decision.Reason), save)
	}
	reportWorktreeMergeProgress(options.Progress, "resolve_route", progress.Completed, string(decision.Route)+": "+decision.Reason)
	receipt.Phase, receipt.Route = WorktreeMergePhaseLand, decision
	if receipt.PreviousTargetSHA == "" {
		receipt.PreviousTargetSHA = remoteTarget
	}
	receipt.Cleanup = receipt.Cleanup || options.Cleanup
	receipt.UpdatedAt = time.Now().UTC()
	if err := save(receipt); err != nil {
		return receipt, err
	}
	if resumedFromLandPhase && !options.StopBeforeMerge {
		// A land-phase receipt whose candidate has moved past its last proven
		// SHA must never reach a publish or landing transition without proving
		// the exact candidate SHA again. Before this block, an ordinary resume
		// of a receipt shaped like an already-published PR (PullRequest set,
		// PublishedCandidateSHA naming an old head) whose candidate had since
		// advanced to a new, still-failing SHA (Status validation_failed) fell
		// straight through the old published-PR carve-out below and pushed the
		// unvalidated candidate to the PR branch. StopBeforeMerge is excluded
		// here because it already re-validates the preserved candidate earlier
		// in this function (see preparedValidationStillValid above); running
		// this block too would revalidate the same SHA twice. This block is
		// gated on resumedFromLandPhase (the receipt's phase as loaded, before
		// the unconditional reassignment above) rather than the always-true
		// post-assignment receipt.Phase, so a fresh first-ever transition from
		// prepare into land keeps refusing on a bare identity mismatch instead
		// of silently re-validating it away.
		identity := receipt.ValidationIdentity
		identityMismatch := identity == nil || identity.CandidateSHA != receipt.Candidate.SHA
		needsRevalidation := receipt.Status == WorktreeMergeValidationFailed ||
			receipt.Validation.Revision != receipt.Candidate.SHA || identityMismatch
		// Finding B1 (sneat-dev/wb#591): this carve-out is proven safe only by
		// the push gate and PR-route CI checks that ran after publishing at
		// this exact SHA. It must not fire on a route THIS call resolved as
		// direct — that includes a resume that flips from a prior PR-route
		// deferral to `--route direct` (scenario g), which must validate.
		publishedAtCurrentSHA := decision.Route == WorktreeMergeRoutePullRequest &&
			receipt.PublishedCandidateSHA != "" && receipt.PublishedCandidateSHA == receipt.Candidate.SHA
		if needsRevalidation && !publishedAtCurrentSHA {
			if err := applyWorktreeMergeValidationLimits(&receipt, options, save); err != nil {
				return receipt, err
			}
			if err := requireCleanMergeWorktreeWithRunner(ctx, options.resolveRunner(), receipt.Candidate.Worktree); err != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
			}
			head, headErr := mergeRevision(ctx, options.resolveRunner(), receipt.Candidate.Worktree, "HEAD")
			if headErr != nil || head != receipt.Candidate.SHA {
				if headErr == nil {
					headErr = fmt.Errorf("candidate head drifted from %s to %s", receipt.Candidate.SHA, head)
				}
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, headErr, save)
			}
			reportWorktreeMergeProgress(options.Progress, "revalidate_candidate", progress.Started, shortMergeRevision(receipt.Candidate.SHA))
			checkTimeout, shardAttemptTimeout := receiptWorktreeMergeValidationTimeouts(receipt)
			validationContext := ctx
			cancelValidation := func() {}
			if options.PrepareTimeout > 0 {
				validationContext, cancelValidation = context.WithTimeout(ctx, options.PrepareTimeout)
			}
			validationErr := applyOrDeferWorktreeMergeValidation(validationContext, &receipt, plan, options.Timeout, options.Retry, checkTimeout, shardAttemptTimeout, options.Progress)
			cancelValidation()
			if validationErr != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeValidationFailed, fmt.Errorf("resumed land-phase candidate re-validation failed: %w", validationErr), save)
			}
			// Clear the stale validation_failed status the same way the
			// prepare-phase re-validation block does (see the WorktreeMergePrepared
			// assignment above): the guard below refuses on a lingering
			// validation_failed status even after Validation itself now proves
			// this exact candidate SHA.
			receipt.Status = WorktreeMergePrepared
			receipt.Failure = ""
			receipt.UpdatedAt = time.Now().UTC()
			if err := save(receipt); err != nil {
				return receipt, err
			}
			reportWorktreeMergeProgress(options.Progress, "revalidate_candidate", progress.Completed, string(receipt.Validation.Status))
		}
	}
	// Finding X1 (sneat-dev/wb#591 red-team follow-up): a receipt whose
	// current validation record is still a stale deferral recorded by an
	// earlier call must be re-validated locally right here whenever THIS
	// call's plan no longer permits deferring (e.g. --validate-locally or
	// --allow-unfenced on a resume, or the target's fence having been
	// removed since the deferral was recorded) — never silently accepted by
	// requireWorktreeMergePublishedValidation, and never silently refused
	// either, since the whole point of these escape hatches is to let the
	// call proceed after actually validating.
	if receipt.ValidationDeferral != nil && receipt.Validation.Status == quality.StatusSkipped && !plan.Defer {
		if err := requireCleanMergeWorktreeWithRunner(ctx, options.resolveRunner(), receipt.Candidate.Worktree); err != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
		}
		reportWorktreeMergeProgress(options.Progress, "revalidate_candidate", progress.Started, shortMergeRevision(receipt.Candidate.SHA))
		checkTimeout, shardAttemptTimeout := receiptWorktreeMergeValidationTimeouts(receipt)
		if validationErr := applyOrDeferWorktreeMergeValidation(ctx, &receipt, plan, options.Timeout, options.Retry, checkTimeout, shardAttemptTimeout, options.Progress); validationErr != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeValidationFailed, fmt.Errorf("stale deferral must be re-validated locally on this call: %w", validationErr), save)
		}
		receipt.Status = WorktreeMergePrepared
		receipt.Failure = ""
		receipt.UpdatedAt = time.Now().UTC()
		if err := save(receipt); err != nil {
			return receipt, err
		}
		reportWorktreeMergeProgress(options.Progress, "revalidate_candidate", progress.Completed, string(receipt.Validation.Status))
	}
	checkTimeout, _ := receiptWorktreeMergeValidationTimeouts(receipt)
	if err := requireWorktreeMergePublishedValidationContext(ctx, receipt, plan, options.Timeout, options.Retry, checkTimeout); err != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
	}
	if options.StopBeforeMerge && receipt.PullRequest != "" {
		if receipt.PublishedCandidateSHA != "" && receipt.PublishedCandidateSHA != receipt.Candidate.SHA {
			remoteRef := "refs/heads/" + receipt.Candidate.Branch
			reportWorktreeMergeProgress(options.Progress, "pre_push_gate", progress.Started, remoteRef)
			receipt.PushGate, err = runWorktreeMergePrePushGate(ctx, receipt.Candidate.Worktree, receipt.Candidate.SHA, remoteRef, options.Timeout, options.Retry)
			if err != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
			}
			if receipt.PushGate.PreviousRemoteSHA != receipt.PublishedCandidateSHA {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, fmt.Errorf("published candidate ref %s moved from recorded predecessor %s to %s", receipt.Candidate.Branch, receipt.PublishedCandidateSHA, receipt.PushGate.PreviousRemoteSHA), save)
			}
			if err := save(receipt); err != nil {
				return receipt, err
			}
			if err := pushWorktreeMergeRef(ctx, receipt.Candidate.Worktree, receipt.Candidate.SHA, remoteRef, true, options.Timeout, options.Retry); err != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, fmt.Errorf("candidate descendant push failed without force: %w", err), save)
			}
			reportWorktreeMergeProgress(options.Progress, "publish_candidate", progress.Completed, remoteRef+"@"+shortMergeRevision(receipt.Candidate.SHA))
			receipt.PublishedCandidateSHA = receipt.Candidate.SHA
			receipt.UpdatedAt = time.Now().UTC()
			if err := save(receipt); err != nil {
				return receipt, err
			}
		}
		if err := verifyPublishedWorktreeMergePullRequest(ctx, receipt, options); err != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
		}
		receipt.PublishedCandidateSHA = receipt.Candidate.SHA
		receipt.Status = WorktreeMergePublished
		receipt.Failure = ""
		receipt.UpdatedAt = time.Now().UTC()
		if err := save(receipt); err != nil {
			return receipt, err
		}
		reportWorktreeMergeProgress(options.Progress, "published_merge_pending", progress.Completed, receipt.PullRequest)
		return receipt, nil
	}

	serverLanding := receipt.Candidate.SHA
	if decision.Route == WorktreeMergeRouteDirect {
		if plan.DirectCI != nil {
			if err := verifyWorktreeMergeDirectCIPullRequest(ctx, receipt, *plan.DirectCI, receipt.TargetSHA); err != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, fmt.Errorf("direct CI pull request changed before push: %w", err), save)
			}
		}
		remoteRef := "refs/heads/" + receipt.Target
		reportWorktreeMergeProgress(options.Progress, "pre_push_gate", progress.Started, remoteRef)
		receipt.PushGate, err = runWorktreeMergePrePushGate(ctx, receipt.Candidate.Worktree, receipt.Candidate.SHA, remoteRef, options.Timeout, options.Retry)
		if err != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
		}
		if err := save(receipt); err != nil {
			return receipt, err
		}
		if err := pushWorktreeMergeRef(ctx, receipt.Candidate.Worktree, receipt.Candidate.SHA, remoteRef, false, options.Timeout, options.Retry); err != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, fmt.Errorf("direct target push failed without force: %w", err), save)
		}
		reportWorktreeMergeProgress(options.Progress, "publish_target", progress.Completed, remoteRef+"@"+shortMergeRevision(receipt.Candidate.SHA))
	} else {
		remoteRef := "refs/heads/" + receipt.Candidate.Branch
		reportWorktreeMergeProgress(options.Progress, "pre_push_gate", progress.Started, remoteRef)
		receipt.PushGate, err = runWorktreeMergePrePushGate(ctx, receipt.Candidate.Worktree, receipt.Candidate.SHA, remoteRef, options.Timeout, options.Retry)
		if err != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
		}
		if receipt.PullRequest != "" && receipt.PublishedCandidateSHA != "" && receipt.PushGate.PreviousRemoteSHA != receipt.PublishedCandidateSHA {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, fmt.Errorf("published candidate ref %s moved from recorded predecessor %s to %s", receipt.Candidate.Branch, receipt.PublishedCandidateSHA, receipt.PushGate.PreviousRemoteSHA), save)
		}
		if err := save(receipt); err != nil {
			return receipt, err
		}
		if err := pushWorktreeMergeRef(ctx, receipt.Candidate.Worktree, receipt.Candidate.SHA, remoteRef, true, options.Timeout, options.Retry); err != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, fmt.Errorf("candidate push failed without force: %w", err), save)
		}
		reportWorktreeMergeProgress(options.Progress, "publish_candidate", progress.Completed, remoteRef+"@"+shortMergeRevision(receipt.Candidate.SHA))
		receipt.PublishedCandidateSHA = receipt.Candidate.SHA
		receipt.UpdatedAt = time.Now().UTC()
		if err := save(receipt); err != nil {
			return receipt, err
		}
		receipt.PullRequest, err = findExactOpenWorktreeMergePullRequest(ctx, receipt)
		if err != nil {
			return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
		}
		if receipt.PullRequest != "" {
			if err := verifyPublishedWorktreeMergePullRequest(ctx, receipt, options); err != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, fmt.Errorf("verify adopted pull request: %w", err), save)
			}
			reportWorktreeMergeProgress(options.Progress, "adopt_pull_request", progress.Completed, receipt.PullRequest)
		} else {
			title, body, textErr := worktreeMergePRText(ctx, receipt)
			if textErr != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, textErr, save)
			}
			receipt.PullRequest, err = openPullRequest(ctx, receipt.Candidate.Worktree, receipt.Candidate.Branch, receipt.Target, title, body,
				Options{Timeout: options.Timeout, Retry: options.Retry})
			if err != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
			}
			reportWorktreeMergeProgress(options.Progress, "open_pull_request", progress.Completed, receipt.PullRequest)
		}
		receipt.UpdatedAt = time.Now().UTC()
		if err := save(receipt); err != nil {
			return receipt, err
		}
		if options.StopBeforeMerge {
			if err := verifyPublishedWorktreeMergePullRequest(ctx, receipt, options); err != nil {
				return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
			}
			receipt.Status = WorktreeMergePublished
			receipt.Failure = ""
			receipt.UpdatedAt = time.Now().UTC()
			if err := save(receipt); err != nil {
				return receipt, err
			}
			reportWorktreeMergeProgress(options.Progress, "published_merge_pending", progress.Completed, receipt.PullRequest)
			return receipt, nil
		}
		reportWorktreeMergeProgress(options.Progress, "candidate_checks", progress.Waiting, receipt.PullRequest)
		receipt, serverLanding, err = landWorktreeMergePullRequest(ctx, receipt, options)
		if err != nil {
			return receipt, err
		}
		reportWorktreeMergeProgress(options.Progress, "merge_pull_request", progress.Completed, shortMergeRevision(serverLanding))
	}

	landing, err := fetchExactMergeTargetWithRunner(ctx, options.resolveRunner(), receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
	}
	containsServerLanding, err := isMergeAncestorWithRunner(ctx, options.resolveRunner(), receipt.Candidate.Worktree, serverLanding, landing)
	if err != nil || !containsServerLanding {
		if err == nil {
			err = fmt.Errorf("exact remote target %s does not contain server landing %s", landing, serverLanding)
		}
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, err, save)
	}
	if receipt.ValidationDeferral != nil && receipt.ValidationDeferral.Route == WorktreeMergeRouteDirect && landing != receipt.Candidate.SHA {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeConflict, fmt.Errorf("direct CI deferral requires exact remote target %s, found %s", receipt.Candidate.SHA, landing), save)
	}
	receipt.LandingSHA = landing
	reportWorktreeMergeProgress(options.Progress, "verify_remote_landing", progress.Completed, receipt.Target+"@"+shortMergeRevision(landing))
	receipt.Status = WorktreeMergeLanded
	receipt.UpdatedAt = time.Now().UTC()
	if err := save(receipt); err != nil {
		return receipt, err
	}

	reportWorktreeMergeProgress(options.Progress, "target_checks", progress.Waiting, shortMergeRevision(landing))
	postChecks, postErr := waitForWorktreeMergeChecks(ctx, receipt, options, "", landing, true)
	receipt.Checks = postChecks
	if postErr != nil {
		status := WorktreeMergePostTargetCIFailed
		if postChecks.Status == PullRequestWaitPending {
			status = WorktreeMergeChecksPending
		}
		failed, failure := failWorktreeMergeReceiptWithSave(receipt, status, postErr, save)
		if status == WorktreeMergePostTargetCIFailed && strings.TrimSpace(options.OnFailure) == "revert" {
			_, _ = PrepareWorktreeMergeRevert(ctx, options.ProjectsRoot, failed.ReceiptPath, options.Timeout, options.Retry)
		}
		return failed, failure
	}

	canonical, canonicalErr := worktrees.CanonicalRepositoryPath(options.ProjectsRoot, receipt.Repository)
	if canonicalErr != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeCanonicalSyncBlocked, canonicalErr, save)
	}
	reportWorktreeMergeProgress(options.Progress, "sync_canonical", progress.Started, canonical)
	receipt.CanonicalSync, err = syncCanonicalMergeTarget(ctx, canonical, receipt.Target, landing, options.Timeout, options.Retry, options.CheckoutUpdated)
	if err != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeCanonicalSyncBlocked, err, save)
	}
	if err := save(receipt); err != nil {
		return receipt, err
	}
	reportWorktreeMergeProgress(options.Progress, "sync_canonical", progress.Completed, receipt.CanonicalSync)
	reportWorktreeMergeProgress(options.Progress, "reconcile_source_prs", progress.Started, "discovering exact absorbed heads")
	if err := reconcileAbsorbedSourcePullRequests(ctx, options.ProjectsRoot, &receipt, options.Timeout, options.Retry, options.Progress); err != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeLanded, err, save)
	}
	reportWorktreeMergeProgress(options.Progress, "reconcile_source_prs", progress.Completed, fmt.Sprintf("%d pull requests", len(receipt.SourcePullRequests)))
	if !receipt.Cleanup {
		reportWorktreeMergeProgress(options.Progress, "landed", progress.Completed, receipt.ReceiptPath)
		return receipt, nil
	}
	if err := lock.Release(); err != nil {
		return receipt, err
	}
	locked = false
	reportWorktreeMergeProgress(options.Progress, "cleanup", progress.Started, strings.Join(sortedUniqueMergeTasks(receipt), ", "))
	if err := cleanupWorktreeMergeAssets(ctx, options.ProjectsRoot, &receipt); err != nil {
		return failWorktreeMergeReceiptWithSave(receipt, WorktreeMergeLanded, err, save)
	}
	reportWorktreeMergeProgress(options.Progress, "cleanup", progress.Completed, strings.Join(receipt.CleanedTasks, ", "))
	receipt.Status = WorktreeMergeComplete
	receipt.Failure = ""
	receipt.UpdatedAt = time.Now().UTC()
	if err := save(receipt); err != nil {
		return receipt, err
	}
	return receipt, nil
}

// applyWorktreeMergeValidationLimits persists effective overrides before validation.
// A call without overrides leaves both the receipt and storage untouched.
func applyWorktreeMergeValidationLimits(receipt *WorktreeMergeReceipt, options WorktreeMergeLandOptions, save func(WorktreeMergeReceipt) error) error {
	if options.CheckTimeout <= 0 && options.ShardAttemptTimeout <= 0 {
		return nil
	}
	storedCheckTimeout, storedShardAttemptTimeout := receiptWorktreeMergeValidationTimeouts(*receipt)
	if options.CheckTimeout > 0 {
		storedCheckTimeout = options.CheckTimeout
	}
	if options.ShardAttemptTimeout > 0 {
		storedShardAttemptTimeout = options.ShardAttemptTimeout
	}
	receipt.ValidationTimeouts = worktreeMergeValidationTimeouts(storedCheckTimeout, storedShardAttemptTimeout)
	receipt.UpdatedAt = time.Now().UTC()
	return save(*receipt)
}

// failWorktreeMergeReceiptWithSave keeps primary failure identity while recording
// truthful failure state using this invocation's actual persistence stage.
func failWorktreeMergeReceiptWithSave(receipt WorktreeMergeReceipt, status WorktreeMergeStatus, failure error, save func(WorktreeMergeReceipt) error) (WorktreeMergeReceipt, error) {
	receipt.Status = status
	receipt.Failure = failure.Error()
	receipt.UpdatedAt = time.Now().UTC()
	if persistErr := save(receipt); persistErr != nil {
		return receipt, fmt.Errorf("%w; persist failure receipt: %v", failure, persistErr)
	}
	return receipt, failure
}
