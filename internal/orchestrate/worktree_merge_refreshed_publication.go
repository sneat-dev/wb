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

func proveRefreshedPublicationRebatch(ctx context.Context, projectsRoot string, r WorktreeMergeReceipt, sources []WorktreeMergeSource, claimBase string) (WorktreeMergeReceipt, error) {
	identity := r
	identity.PullRequest, identity.PublishedCandidateSHA = "", ""
	if err := validatePrepareFailureSupersessionReceipt(identity, r.ReceiptPath); err != nil {
		return r, err
	}
	advanced, err := worktreeMergeSourcesAdvanced(ctx, defaultRunner, r.Sources, sources)
	if err != nil {
		return r, err
	}
	// Ordinary prepare may already have recorded the forward repair. Compare
	// the exact same current custody with captured history in that case; a
	// changed source list cannot borrow another source's historical advance.
	recordedAdvance := false
	if !advanced && sameWorktreeMergeSources(r.Sources, sources) {
		for _, refresh := range r.SourceRefreshes {
			recordedAdvance, err = worktreeMergeSourcesAdvanced(ctx, defaultRunner, refresh.Sources, sources)
			if err != nil {
				return r, err
			}
			if recordedAdvance {
				break
			}
		}
	}
	if !advanced && !recordedAdvance {
		return r, errors.New("refreshed publication rebatch requires identical source custody and at least one strict descendant advance")
	}
	// Reuse the exact repository/base/head/ref publication proof. Closed PRs
	// are handled only by the existing persisted supersession-intent recovery.
	publication := r
	if err := provePublishedCandidatePullRequest(ctx, defaultRunner, publication, r.PullRequest, true); err != nil {
		// A target-drift refusal can leave the repaired candidate local while
		// the PR still has its exact recorded published head. Prove that head
		// independently for either proven source advance, then carry it into
		// the normal open/close-intent check.
		publication.Candidate.SHA = r.PublishedCandidateSHA
		if err := provePublishedCandidatePullRequest(ctx, defaultRunner, publication, r.PullRequest, true); err != nil {
			return r, err
		}
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
			return r, fmt.Errorf("refreshed candidate lost captured root %s: ancestor=%t err=%v", root, contains, err)
		}
	}
	for _, source := range sources {
		head, _, err := validateValidationFailedSupersessionSourceWithRunner(ctx, defaultRunner, projectsRoot, r, source)
		if err != nil {
			return r, err
		}
		for _, root := range roots {
			retained, err := isMergeAncestor(ctx, source.Worktree, root, source.SHA)
			if err != nil || !retained {
				return r, fmt.Errorf("advanced source lost captured root %s: ancestor=%t err=%v", root, retained, err)
			}
		}
		if head != source.SHA {
			return r, errors.New("refreshed publication source head drifted during proof")
		}
	}
	return publication, nil
}
