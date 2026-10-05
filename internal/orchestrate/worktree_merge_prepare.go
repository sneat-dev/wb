package orchestrate

import (
	"context"
	"time"

	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type prepareContinuation uint8

const (
	prepareRefused prepareContinuation = iota
	prepareRefresh
	prepareForwardRepair
)

// choosePrepareContinuation is evaluated independently before and after lane
// exclusivity. Refresh always precedes repair; callers retain temporal reads
// and their distinct refusal diagnostics and never clear an earlier repair.
func choosePrepareContinuation(ctx context.Context, run runner.Runner, prior WorktreeMergeReceipt, sources []WorktreeMergeSource) (prepareContinuation, error) {
	refresh, err := canRefreshWorktreeMergeReceiptWithRunner(ctx, run, prior, sources)
	if err != nil {
		return prepareRefused, err
	}
	if refresh {
		return prepareRefresh, nil
	}
	repair, err := canPreparePostTargetRepairWithRunner(ctx, run, prior, sources)
	if err != nil {
		return prepareRefused, err
	}
	if repair {
		return prepareForwardRepair, nil
	}
	return prepareRefused, nil
}

// preparingReceiptInput contains only facts already established by Prepare.
// Construction performs no custody validation, native effects, or persistence.
type preparingReceiptInput struct {
	options                                          WorktreeMergePrepareOptions
	candidate                                        worktrees.CreateResult
	sources                                          []WorktreeMergeSource
	prior                                            *WorktreeMergeReceipt
	rebatch                                          *WorktreeMergePreparedRebatch
	laneRecord                                       landinglane.Record
	forwardRepair                                    bool
	now                                              time.Time
	operation, lane, repository, target, receiptPath string
}

func newPreparingWorktreeMergeReceipt(input preparingReceiptInput) WorktreeMergeReceipt {
	options, candidate, sources, prior, rebatch := input.options, input.candidate, input.sources, input.prior, input.rebatch
	laneRecord, forwardRepair, now := input.laneRecord, input.forwardRepair, input.now
	operation, lane, repository, target, receiptPath := input.operation, input.lane, input.repository, input.target, input.receiptPath
	createdAt := now
	var refreshes []WorktreeMergeSourceRefresh
	if prior != nil {
		createdAt = prior.CreatedAt
		refreshes = append(refreshes, prior.SourceRefreshes...)
		refreshes = append(refreshes, WorktreeMergeSourceRefresh{RecordedAt: now, Sources: append([]WorktreeMergeSource(nil), prior.Sources...)})
	}
	receipt := WorktreeMergeReceipt{
		SchemaVersion: WorktreeMergeSchemaVersion,
		ID:            operation, Lane: lane, Phase: WorktreeMergePhasePrepare, Status: WorktreeMergePreparing,
		Repository: repository, Target: target, TargetSHA: candidate.BaseSHA,
		Sources:            sources,
		Candidate:          WorktreeMergeCandidate{Task: operation, Worktree: candidate.WorktreeDir, Branch: candidate.Branch},
		ValidationTimeouts: worktreeMergeValidationTimeouts(options.CheckTimeout, options.ShardAttemptTimeout),
		SourceRefreshes:    refreshes,
		ResumeArgs:         worktreeMergePrepareResumeArgs(receiptPath, options.ProgressRequested),
		HostLoadAdmission:  options.HostLoadAdmission,
		ReceiptPath:        receiptPath, CreatedAt: createdAt, UpdatedAt: now,
	}
	if laneRecord.Owner.WBSessionID != "" {
		record := laneRecord
		receipt.LaneOwner = &record
	}
	if rebatch != nil {
		receipt.RebatchOf = rebatch.ReceiptPath
		receipt.RebatchedCandidates = []WorktreeMergeCandidate{rebatch.OriginalCandidate}
	}
	if prior != nil {
		// A refresh inherits omitted limits, but explicit caller limits win.
		// Keeping the entire old policy silently reuses obsolete short deadlines.
		checkTimeout, shardTimeout := receiptWorktreeMergeValidationTimeouts(*prior)
		if options.CheckTimeout > 0 {
			checkTimeout = options.CheckTimeout
		}
		if options.ShardAttemptTimeout > 0 {
			shardTimeout = options.ShardAttemptTimeout
		}
		receipt.ValidationTimeouts = worktreeMergeValidationTimeouts(checkTimeout, shardTimeout)
		receipt.Route = prior.Route
		receipt.Cleanup = prior.Cleanup
		receipt.OnFailure = prior.OnFailure
		receipt.ResumeArgs = append([]string(nil), prior.ResumeArgs...)
		if forwardRepair {
			receipt.ForwardRepairs = append([]WorktreeMergeForwardRepairReceipt(nil), prior.ForwardRepairs...)
			receipt.ForwardRepairs = append(receipt.ForwardRepairs, WorktreeMergeForwardRepairReceipt{
				Status: prior.Status, TargetSHA: prior.TargetSHA, CandidateSHA: prior.Candidate.SHA,
				LandingSHA: prior.LandingSHA, PullRequest: prior.PullRequest, Checks: prior.Checks, Failure: prior.Failure,
			})
		} else {
			receipt.PullRequest = prior.PullRequest
			receipt.PublishedCandidateSHA = prior.PublishedCandidateSHA
			if receipt.PullRequest != "" && receipt.PublishedCandidateSHA == "" {
				receipt.PublishedCandidateSHA = prior.Candidate.SHA
			}
			receipt.PreviousTargetSHA = prior.PreviousTargetSHA
		}
	}
	return receipt
}
