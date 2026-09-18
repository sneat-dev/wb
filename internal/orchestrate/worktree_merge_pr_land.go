package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// landWorktreeMergePullRequest drives the worktree-merge PR route's exact
// published candidate through the same wait-arm-merge engine `wb pr land`
// uses (awaitLandablePullRequest / mergeOrAdoptAutoMerge), rather than the
// route's own bespoke wait-then-`gh pr merge --match-head-commit` sequence.
//
// It replaces the previous pairing of waitForWorktreeMergeChecks (candidate
// route) and mergeExactPullRequest, which is now retired: this is a full
// cutover, not an alternative path.
//
// On success it returns the receipt (updated with any recorded update-branch
// advance, AutoMergeArmed, and MergedBy) and the server's merge-result SHA.
// On failure it returns a receipt already persisted via failWorktreeMergeReceipt,
// matching every other terminal step in this file.
// failWorktreeMergePRLand adapts failWorktreeMergeReceipt's two-value return
// to landWorktreeMergePullRequest's three-value one; the receipt it returns
// is already persisted with the failure, exactly like every other terminal
// step in worktree_merge.go.
func failWorktreeMergePRLand(receipt WorktreeMergeReceipt, status WorktreeMergeStatus, failure error) (WorktreeMergeReceipt, string, error) {
	receipt, err := failWorktreeMergeReceipt(receipt, status, failure)
	return receipt, "", err
}

func landWorktreeMergePullRequest(ctx context.Context, receipt WorktreeMergeReceipt, options WorktreeMergeLandOptions) (WorktreeMergeReceipt, string, error) {
	view, err := ReadPullRequest(ctx, receipt.Repository, receipt.PullRequest)
	if err != nil {
		return failWorktreeMergePRLand(receipt, WorktreeMergeConflict, fmt.Errorf("read published pull request: %w", err))
	}
	if view.Head.SHA != receipt.Candidate.SHA {
		return failWorktreeMergePRLand(receipt, WorktreeMergeConflict,
			fmt.Errorf("published pull request head %s does not match exact candidate %s", view.Head.SHA, receipt.Candidate.SHA))
	}

	title, body, textErr := worktreeMergePRText(ctx, receipt)
	if textErr != nil {
		return failWorktreeMergePRLand(receipt, WorktreeMergeConflict, fmt.Errorf("prepare pull-request merge text: %w", textErr))
	}
	method, methodErr := repositoryPullRequestMergeMethod(ctx, receipt.Repository)
	if methodErr != nil {
		return failWorktreeMergePRLand(receipt, WorktreeMergeConflict, methodErr)
	}

	slice := options.Timeout
	if slice <= 0 || slice > 8*time.Minute {
		slice = 8 * time.Minute
	}
	landOptions := PullRequestLandOptions{
		Repository:          receipt.Repository,
		PullRequest:         receipt.PullRequest,
		ProjectsRoot:        options.ProjectsRoot,
		MergeMethod:         method,
		MergeMethodExplicit: true,
		AllowUnfenced:       options.AllowUnfenced,
		Slice:               slice,
		CheckPollInterval:   options.CheckPollInterval,
		OperationProgress:   options.Progress,
		Lane:                options.Lane,
		headUpdated: func(previous, updated string) error {
			return adoptWorktreeMergeUpdateBranchAdvance(ctx, &receipt, previous, updated)
		},
	}

	evidence := map[string]string{}
	updatedView, waited, autoMergeArmed, mergedByGitHub, updateRefusal, err := awaitLandablePullRequest(ctx, landOptions, view, receipt.PullRequest, title, body, evidence)
	receipt.AutoMergeArmed = receipt.AutoMergeArmed || autoMergeArmed
	if err != nil {
		return failWorktreeMergePRLand(receipt, WorktreeMergeConflict, err)
	}
	if updateRefusal != nil {
		return failWorktreeMergePRLand(receipt, WorktreeMergeConflict,
			fmt.Errorf("%s; resume with wb worktree merge resume %s", updateRefusal.reason, receipt.ReceiptPath))
	}
	receipt.Checks = waited
	if waited.Status != PullRequestWaitPassed && !mergedByGitHub {
		status := WorktreeMergeChecksFailed
		reason := fmt.Errorf("exact-head checks failed: %s", waited.Reason)
		switch {
		case waited.Status == PullRequestWaitPending:
			status = WorktreeMergeChecksPending
			note := ""
			if autoMergeArmed {
				note = "; auto-merge is armed, so this lands without WB once checks pass"
			}
			reason = fmt.Errorf("exact-head checks remain pending: %s%s; resume with wb worktree merge resume %s", waited.Reason, note, receipt.ReceiptPath)
		case !options.AllowUnfenced && strings.Contains(waited.Reason, "strict up-to-date fence"):
			reason = fmt.Errorf("exact-head checks failed: %s; resume with wb worktree merge resume %s --allow-unfenced", waited.Reason, receipt.ReceiptPath)
		}
		return failWorktreeMergePRLand(receipt, status, reason)
	}

	head := updatedView.Head.SHA
	_, mergeRefusal, mergeErr := mergeOrAdoptAutoMerge(ctx, landOptions, receipt.PullRequest, head, method, title, body, autoMergeArmed, mergedByGitHub, evidence)
	if mergeErr != nil {
		return failWorktreeMergePRLand(receipt, WorktreeMergeConflict, mergeErr)
	}
	if mergeRefusal != nil {
		return failWorktreeMergePRLand(receipt, WorktreeMergeConflict, fmt.Errorf("%s", mergeRefusal.reason))
	}
	if merged := evidence["merged_by"]; merged != "" {
		receipt.MergedBy = merged
	}

	serverLanding, isMerged, receiptErr := pullRequestLandingReceipt(ctx, receipt, options)
	if receiptErr != nil {
		return failWorktreeMergePRLand(receipt, WorktreeMergeConflict, receiptErr)
	}
	if !isMerged {
		return failWorktreeMergePRLand(receipt, WorktreeMergeConflict,
			fmt.Errorf("pull request did not report a merged server result after merge"))
	}
	receipt.UpdatedAt = time.Now().UTC()
	if persistErr := persistWorktreeMergeReceipt(receipt); persistErr != nil {
		return receipt, "", persistErr
	}
	return receipt, serverLanding, nil
}

// repositoryPullRequestMergeMethod reads the repository's enabled
// pull-request merge methods and picks the one landWorktreeMergePullRequest
// (via the shared engine's mergePullRequest) should use, preferring a merge
// commit, then squash, then rebase — matching the order the retired
// mergeExactPullRequest used.
func repositoryPullRequestMergeMethod(ctx context.Context, repository string) (string, error) {
	output, err := githubGet(ctx, "", repository, "", "", "repos/"+repository)
	if err != nil {
		return "", fmt.Errorf("read repository merge methods: %w", err)
	}
	var settings struct {
		AllowMerge  bool `json:"allow_merge_commit"`
		AllowSquash bool `json:"allow_squash_merge"`
		AllowRebase bool `json:"allow_rebase_merge"`
	}
	if err := json.Unmarshal(output, &settings); err != nil {
		return "", fmt.Errorf("decode repository merge methods: %w", err)
	}
	switch {
	case settings.AllowMerge:
		return "merge", nil
	case settings.AllowSquash:
		return "squash", nil
	case settings.AllowRebase:
		return "rebase", nil
	default:
		return "", fmt.Errorf("repository exposes no supported pull-request merge method")
	}
}

// pullRequestCommitParents reads one commit's parent SHAs through the
// GitHub API, so a merge commit produced by a server-side update-branch (or
// discovered already on a resumed pull request head) can be attributed to
// the target head it merged in without depending on the local worktree
// already having fetched it.
func pullRequestCommitParents(ctx context.Context, repository, sha string) ([]string, error) {
	output, err := githubGet(ctx, "", repository, "", sha, "repos/"+repository+"/commits/"+sha)
	if err != nil {
		return nil, fmt.Errorf("read commit parents for %s: %w", shortMergeRevision(sha), err)
	}
	var decoded struct {
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	}
	if err := json.Unmarshal(output, &decoded); err != nil {
		return nil, fmt.Errorf("decode commit parents for %s: %w", shortMergeRevision(sha), err)
	}
	parents := make([]string, 0, len(decoded.Parents))
	for _, parent := range decoded.Parents {
		parents = append(parents, parent.SHA)
	}
	return parents, nil
}

// adoptWorktreeMergeUpdateBranchAdvance is the worktree-merge PR route's
// options.headUpdated hook (see PullRequestLandOptions.headUpdated and
// awaitLandablePullRequest). It closes red-team finding M1: without
// advancing receipt.TargetSHA to the update-branch merge's own target
// parent, absorbedSourceHeads (worktree_merge_source_prs.go) would walk
// `git rev-list --merges TargetSHA..Candidate.SHA` across every commit main
// picked up since the STALE TargetSHA, reporting unrelated already-merged
// pull requests as "absorbed by this PR".
//
// It also closes M3's ordering requirement: the receipt is persisted with
// the new Candidate.SHA / PublishedCandidateSHA / TargetSHA and a
// TargetRefreshes entry BEFORE the local candidate worktree is fast-forwarded.
// A dirty or missing worktree therefore never blocks recording an
// update-branch merge that already happened server-side; the fast-forward
// failure is swallowed here (best effort) and left for
// adoptServerUpdatedWorktreeMergeHead to reconcile on a later resume.
func adoptWorktreeMergeUpdateBranchAdvance(ctx context.Context, receipt *WorktreeMergeReceipt, previous, updated string) error {
	if receipt == nil {
		return fmt.Errorf("worktree-merge PR-land hook called with no receipt")
	}
	parents, err := pullRequestCommitParents(ctx, receipt.Repository, updated)
	if err != nil {
		return fmt.Errorf("read update-branch merge commit parents: %w", err)
	}
	newTarget, targetErr := updateBranchMergeTargetParent(parents, previous)
	if targetErr != nil {
		return fmt.Errorf("update-branch merge commit %s: %w", updated, targetErr)
	}
	receipt.TargetRefreshes = append(receipt.TargetRefreshes, WorktreeMergeTargetRefresh{
		RecordedAt:           time.Now().UTC(),
		PreviousTargetSHA:    receipt.TargetSHA,
		NewTargetSHA:         newTarget,
		PreviousCandidateSHA: receipt.Candidate.SHA,
		NewCandidateSHA:      updated,
	})
	receipt.TargetSHA = newTarget
	receipt.Candidate.SHA = updated
	receipt.PublishedCandidateSHA = updated
	receipt.UpdatedAt = time.Now().UTC()
	if err := persistWorktreeMergeReceipt(*receipt); err != nil {
		return fmt.Errorf("persist update-branch advance: %w", err)
	}
	// Best effort: an unavailable or dirty worktree must not undo the
	// persisted advance above (M3).
	_ = fastForwardWorktreeMergeCandidateBranch(ctx, receipt.Candidate.Worktree, receipt.Candidate.Branch, updated)
	return nil
}

// updateBranchMergeTargetParent picks the update-branch merge commit's
// target-side parent: the one that is not the previous candidate head. It is
// the pure core of red-team finding M1's fix, split out from
// adoptWorktreeMergeUpdateBranchAdvance so the parent-selection logic is
// testable without a GitHub round trip.
func updateBranchMergeTargetParent(parents []string, previous string) (string, error) {
	switch {
	case len(parents) == 2 && parents[0] == previous:
		return parents[1], nil
	case len(parents) == 2 && parents[1] == previous:
		return parents[0], nil
	default:
		return "", fmt.Errorf("has unexpected parents %v for previous head %s", parents, previous)
	}
}

// fastForwardWorktreeMergeCandidateBranch brings the local candidate
// worktree up to the exact head a server-side update-branch merge (or an
// adopted foreign advance) just produced. Failure here is always tolerated
// by its caller: the receipt is the durable record, and the worktree is a
// convenience the next resume can still repair.
func fastForwardWorktreeMergeCandidateBranch(ctx context.Context, worktree, branch, updatedHead string) error {
	if strings.TrimSpace(worktree) == "" {
		return fmt.Errorf("candidate worktree path is empty")
	}
	if info, statErr := os.Stat(worktree); statErr != nil || !info.IsDir() {
		if statErr == nil {
			statErr = fmt.Errorf("%s is not a directory", worktree)
		}
		return fmt.Errorf("candidate worktree unavailable: %w", statErr)
	}
	refspec := "+refs/heads/" + branch + ":refs/remotes/origin/" + branch
	if _, _, err := runCommand(ctx, 0, 0, worktree, "git", "fetch", "--no-tags", "origin", refspec); err != nil {
		return fmt.Errorf("fetch updated candidate branch: %w", err)
	}
	fetched, err := mergeRevision(ctx, worktree, "refs/remotes/origin/"+branch)
	if err != nil {
		return err
	}
	if fetched != updatedHead {
		return fmt.Errorf("fetched candidate branch head %s does not match updated head %s", fetched, updatedHead)
	}
	if _, _, err := runCommand(ctx, 0, 0, worktree, "git", "merge", "--ff-only", "refs/remotes/origin/"+branch); err != nil {
		return fmt.Errorf("fast-forward candidate worktree: %w", err)
	}
	return nil
}

// adoptServerUpdatedWorktreeMergeHead closes red-team finding M5 for the one
// crash state adoptWorktreeMergeUpdateBranchAdvance's persist-first ordering
// (M3) cannot itself prevent: a server-side update-branch merge that
// completed but was never recorded because WB crashed, was killed, or lost
// its connection between the update-branch write and the persist that
// follows it in adoptWorktreeMergeUpdateBranchAdvance.
//
// It adopts the pull request's current head as an update-branch advance only
// when ALL of: the head's first parent is the recorded Candidate.SHA; its
// second parent is an ancestor of the current remote target; and its tree is
// exactly the tree an ordinary merge of those two parents would produce
// (`git merge-tree --write-tree`). Anything else is left for the caller's
// ordinary drift/conflict handling to judge — this never adopts a head that
// merely happens to be reachable.
// It is deliberately best-effort end to end: ANY failure to positively prove
// adoption (an unreadable pull request, a repository too old or too
// restricted to expose the commits API, a target fetch failure, and so on)
// returns "not adopted" rather than an error, leaving the receipt exactly as
// it was for the ordinary drift/conflict handling that already runs right
// after this to judge. It must never turn an unrelated, already-handled kind
// of head drift (a local push advancing the candidate worktree, a foreign
// close, and so on) into a hard failure merely because this best-effort
// verification could not complete.
func adoptServerUpdatedWorktreeMergeHead(ctx context.Context, receipt *WorktreeMergeReceipt) (bool, error) {
	if receipt == nil || receipt.PullRequest == "" || receipt.LandingSHA != "" || receipt.Candidate.SHA == "" {
		return false, nil
	}
	view, err := ReadPullRequest(ctx, receipt.Repository, receipt.PullRequest)
	if err != nil {
		return false, nil
	}
	if view.Head.SHA == "" || view.Head.SHA == receipt.Candidate.SHA {
		return false, nil
	}
	parents, err := pullRequestCommitParents(ctx, receipt.Repository, view.Head.SHA)
	if err != nil {
		return false, nil
	}
	if len(parents) != 2 || parents[0] != receipt.Candidate.SHA {
		// Not an update-branch merge sitting directly on our recorded
		// candidate: leave it for the ordinary drift/conflict handling.
		return false, nil
	}
	if strings.TrimSpace(receipt.Candidate.Worktree) == "" {
		return false, nil
	}
	remoteTarget, fetchErr := fetchExactMergeTarget(ctx, receipt.Candidate.Worktree, receipt.Target)
	if fetchErr != nil {
		return false, nil
	}
	targetAncestor, ancestorErr := isMergeAncestor(ctx, receipt.Candidate.Worktree, parents[1], remoteTarget)
	if ancestorErr != nil || !targetAncestor {
		return false, nil
	}
	if _, _, err := runCommand(ctx, 0, 0, receipt.Candidate.Worktree, "git", "fetch", "--no-tags", "origin",
		"+refs/heads/"+receipt.Candidate.Branch+":refs/remotes/origin/"+receipt.Candidate.Branch); err != nil {
		return false, nil
	}
	writtenTree, treeErr := runGit(ctx, receipt.Candidate.Worktree, "merge-tree", "--write-tree", receipt.Candidate.SHA, parents[1])
	if treeErr != nil {
		return false, nil
	}
	headTree, headTreeErr := runGit(ctx, receipt.Candidate.Worktree, "show", "-s", "--format=%T", view.Head.SHA)
	if headTreeErr != nil {
		return false, nil
	}
	if strings.TrimSpace(writtenTree) != strings.TrimSpace(headTree) {
		// Not an ordinary merge of our candidate and the target: leave it
		// for the ordinary drift/conflict handling to judge.
		return false, nil
	}
	receipt.TargetRefreshes = append(receipt.TargetRefreshes, WorktreeMergeTargetRefresh{
		RecordedAt:           time.Now().UTC(),
		PreviousTargetSHA:    receipt.TargetSHA,
		NewTargetSHA:         parents[1],
		PreviousCandidateSHA: receipt.Candidate.SHA,
		NewCandidateSHA:      view.Head.SHA,
	})
	receipt.TargetSHA = parents[1]
	receipt.Candidate.SHA = view.Head.SHA
	receipt.PublishedCandidateSHA = view.Head.SHA
	_ = fastForwardWorktreeMergeCandidateBranch(ctx, receipt.Candidate.Worktree, receipt.Candidate.Branch, view.Head.SHA)
	return true, nil
}

// worktreeMergeTargetAdvanceRecorded reports whether receipt.TargetRefreshes
// already records the exact (from -> to) target advance. It closes M2: a
// conflict-candidate advance acknowledgement snapshot taken before this
// receipt's target moved via a recorded server update (an update-branch
// merge the PR-land engine performed) must not hard-fail resume merely
// because the snapshot's target no longer matches — the receipt's own
// append-only history proves why it changed.
func worktreeMergeTargetAdvanceRecorded(receipt WorktreeMergeReceipt, from, to string) bool {
	if from == to {
		return true
	}
	for _, refresh := range receipt.TargetRefreshes {
		if refresh.PreviousTargetSHA == from && refresh.NewTargetSHA == to {
			return true
		}
	}
	return false
}
