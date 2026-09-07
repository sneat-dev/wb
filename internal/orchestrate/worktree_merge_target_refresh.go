package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// refreshPublishedWorktreeMergeCandidateTarget is the published-branch
// counterpart to the unpublished target-drift rebase performed inline in
// LandWorktreeMerge. A published candidate's branch is an open pull request:
// WB never force-pushes it, so target drift cannot be absorbed by rewriting
// history (rebase). Instead this merges the freshly fetched target directly
// into the candidate's current HEAD (which must already be the exact
// published commit, or a proven descendant of it), producing a new candidate
// commit that is a fast-forward descendant of what is currently published.
// The caller is responsible for persisting the receipt, re-validating the
// resulting candidate at its exact new SHA, and pushing the fast-forward
// through the existing publish machinery — this function only performs and
// records the merge itself.
//
// Evidence: receipt merge-sneat-dev-wb-main-1cbbf49dd60f-e69e39368098.json
// (2026-09-07) recorded a three-source candidate published to PR #450 at
// target 0b84b90; main advanced to 616804b; a same-source re-prepare built
// and validated a new candidate 68ec97ad on the new target and then refused
// to push it, leaving the receipt stranded in prepare/conflict with the PR
// still recorded. Refresh replaces that refusal.
func refreshPublishedWorktreeMergeCandidateTarget(ctx context.Context, receipt *WorktreeMergeReceipt, remoteTarget string, timeout time.Duration, retry int) error {
	if receipt == nil {
		return fmt.Errorf("refresh published candidate: receipt is required")
	}
	if strings.TrimSpace(remoteTarget) == "" {
		return fmt.Errorf("refresh published candidate: remote target revision is required")
	}
	previousCandidate := receipt.Candidate.SHA
	previousTarget := receipt.TargetSHA
	if _, _, mergeErr := runCommand(ctx, timeout, retry, receipt.Candidate.Worktree, "git", "merge", "--no-edit", remoteTarget); mergeErr != nil {
		conflicts, conflictErr := conflictingWorktreeMergePaths(ctx, receipt.Candidate.Worktree)
		_, _, _ = runCommand(ctx, timeout, 0, receipt.Candidate.Worktree, "git", "merge", "--abort")
		if conflictErr != nil || len(conflicts) == 0 {
			return fmt.Errorf(
				"target advanced to %s and refreshing published candidate %s (PR %s) failed to merge cleanly: %w",
				remoteTarget, previousCandidate, receipt.PullRequest, mergeErr,
			)
		}
		return fmt.Errorf(
			"target advanced to %s and refreshing published candidate %s (PR %s) conflicts in %s; resolve the conflict in the candidate worktree %s, commit the resolution, then run `wb worktree merge resume %s`",
			remoteTarget, previousCandidate, receipt.PullRequest, strings.Join(conflicts, ", "), receipt.Candidate.Worktree, receipt.ReceiptPath,
		)
	}
	newCandidate, headErr := mergeRevision(ctx, receipt.Candidate.Worktree, "HEAD")
	if headErr != nil {
		return fmt.Errorf("read refreshed candidate head: %w", headErr)
	}
	receipt.TargetSHA = remoteTarget
	receipt.Candidate.SHA = newCandidate
	receipt.TargetRefreshes = append(receipt.TargetRefreshes, WorktreeMergeTargetRefresh{
		RecordedAt:           time.Now().UTC(),
		PreviousTargetSHA:    previousTarget,
		NewTargetSHA:         remoteTarget,
		PreviousCandidateSHA: previousCandidate,
		NewCandidateSHA:      newCandidate,
	})
	return nil
}

// conflictingWorktreeMergePaths reads the unmerged paths left behind by a
// failed `git merge` so a refusal or conflict receipt can name them exactly,
// alongside the recovery command, instead of only echoing git's own output.
func conflictingWorktreeMergePaths(ctx context.Context, worktree string) ([]string, error) {
	output, _, err := runCommand(ctx, 0, 0, worktree, "git", "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}
