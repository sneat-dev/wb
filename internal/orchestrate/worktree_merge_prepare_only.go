package orchestrate

import (
	"context"
	"fmt"
	"strings"
)

func validatePrepareOnlyShape(r WorktreeMergeReceipt, o WorktreeMergeLandOptions) error {
	if o.Cleanup || o.StopBeforeMerge || o.DirectCIPullRequest != "" {
		return fmt.Errorf("prepare-only cannot combine with cleanup, stop-before-merge or deferred direct CI")
	}
	if r.Phase != WorktreeMergePhasePrepare || r.Status != WorktreeMergePreparing || len(r.ForwardRepairs) == 0 || r.LandingSHA != "" || r.PullRequest != "" || r.PublishedCandidateSHA != "" || r.Candidate.SHA == "" {
		return fmt.Errorf("prepare-only requires an exact interrupted unpublished forward-repair preparing receipt")
	}
	return nil
}

// Reuse WB's custody and graph proofs under the lane lock. No caller may use
// this as a way to validate a different source or stale target and then rebatch.
func validatePrepareOnlyCandidate(ctx context.Context, o WorktreeMergeLandOptions, r WorktreeMergeReceipt) error {
	if _, err := validateMergeAcknowledgementCandidateWithRunner(ctx, o.resolveRunner(), o.ProjectsRoot, r, r.Candidate); err != nil {
		return err
	}
	if err := recheckWorktreeMergeSourcesWithRunner(ctx, o.resolveRunner(), r.Sources); err != nil {
		return err
	}
	target, err := fetchExactMergeTargetWithRunner(ctx, o.resolveRunner(), r.Candidate.Worktree, r.Target)
	if err != nil {
		return err
	}
	if target != r.TargetSHA {
		return fmt.Errorf("prepare-only target drifted from %s to %s", r.TargetSHA, target)
	}
	remote, _, err := runCommand(ctx, o.resolveRunner(), o.Timeout, o.Retry, r.Candidate.Worktree, "git", "ls-remote", "--heads", "origin", "refs/heads/"+r.Candidate.Branch)
	if err != nil {
		return err
	}
	if strings.TrimSpace(remote) != "" {
		return fmt.Errorf("prepare-only candidate branch is already published")
	}
	return nil
}
