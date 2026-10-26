package orchestrate

import (
	"context"
	"errors"
	"fmt"
)

// This deliberately excludes ordinary published receipts and unpublished
// conflicts. Recovery is an explicit, target-fenced immutable rebatch only.
func refreshedPublicationRebatchEligible(r WorktreeMergeReceipt) bool {
	return r.Phase == WorktreeMergePhasePrepare && r.Status == WorktreeMergeConflict && r.PullRequest != "" && r.PublishedCandidateSHA != "" && r.PublishedCandidateSHA != r.Candidate.SHA && r.LandingSHA == ""
}

func proveRefreshedPublicationRebatch(ctx context.Context, projectsRoot string, r WorktreeMergeReceipt, sources []WorktreeMergeSource, claimBase string) error {
	identity := r
	identity.PullRequest, identity.PublishedCandidateSHA = "", ""
	if err := validatePrepareFailureSupersessionReceipt(identity, r.ReceiptPath); err != nil {
		return err
	}
	advanced, err := worktreeMergeSourcesAdvanced(ctx, defaultRunner, r.Sources, sources)
	if err != nil {
		return err
	}
	if !advanced {
		return errors.New("refreshed publication rebatch requires identical source custody and at least one strict descendant advance")
	}
	// Reuse the exact repository/base/head/ref publication proof. Closed PRs
	// are handled only by the existing persisted supersession-intent recovery.
	if err := provePublishedCandidatePullRequest(ctx, defaultRunner, r, r.PullRequest, true); err != nil {
		return err
	}
	roots := []string{claimBase, r.PublishedCandidateSHA, r.TargetSHA, r.Candidate.SHA}
	for _, source := range r.Sources {
		roots = append(roots, source.SHA)
	}
	for _, refresh := range r.SourceRefreshes {
		for _, source := range refresh.Sources {
			roots = append(roots, source.SHA)
		}
	}
	for _, refresh := range r.TargetRefreshes {
		roots = append(roots, refresh.PreviousTargetSHA, refresh.NewTargetSHA, refresh.PreviousCandidateSHA, refresh.NewCandidateSHA)
	}
	for _, root := range roots {
		contains, err := isMergeAncestor(ctx, r.Candidate.Worktree, root, r.Candidate.SHA)
		if err != nil || !contains {
			return fmt.Errorf("refreshed candidate lost captured root %s: ancestor=%t err=%v", root, contains, err)
		}
	}
	for _, source := range sources {
		head, _, err := validateValidationFailedSupersessionSourceWithRunner(ctx, defaultRunner, projectsRoot, r, source)
		if err != nil {
			return err
		}
		for _, root := range roots {
			retained, err := isMergeAncestor(ctx, source.Worktree, root, source.SHA)
			if err != nil || !retained {
				return fmt.Errorf("advanced source lost captured root %s: ancestor=%t err=%v", root, retained, err)
			}
		}
		if head != source.SHA {
			return errors.New("refreshed publication source head drifted during proof")
		}
	}
	return nil
}
