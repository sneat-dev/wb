package orchestrate

import "errors"

// ReviewedSourceHeads follows the receipt's immutable source ordering. The
// replacement must itself be a pinned source so its reviewed SHA is explicit.
func validateReviewedSourceEvidence(r WorktreeMergeReceipt, replacement WorktreeMergeCandidate, target string, heads []string) error {
	if r.Phase != WorktreeMergePhasePrepare || r.Status != WorktreeMergeValidationFailed || r.PullRequest != "" || r.PublishedCandidateSHA != "" || r.LandingSHA != "" || r.TargetSHA != target || len(heads) != len(r.Sources) {
		return errors.New("reviewed repair requires an unpublished failed prepare, the original remote target and every source")
	}
	advanced := 0
	replacementPinned := false
	branches := map[string]bool{}
	for i, s := range r.Sources {
		if !fullSHAPattern.MatchString(heads[i]) || branches[s.Branch] {
			return errors.New("reviewed repair requires distinct branches and full commit IDs")
		}
		branches[s.Branch] = true
		if heads[i] != s.SHA {
			advanced++
		}
		if replacement.Task == s.Task && replacement.Branch == s.Branch && replacement.Worktree == s.Worktree && replacement.SHA == heads[i] && heads[i] != s.SHA {
			replacementPinned = true
		}
	}
	if advanced < 2 || !replacementPinned {
		return errors.New("reviewed repair requires multiple advanced sources including the replacement")
	}
	return nil
}
