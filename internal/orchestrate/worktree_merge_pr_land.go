package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
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

// recordDeferredValidationCheckSkippedFinding is round 3's non-blocking
// replacement for round 2's strict required-check gating (sneat-dev/wb#591
// red-team follow-up, findings B1/B2). It runs only here, on the
// candidate/PR phase's own wait result — never on waitForWorktreeMergeChecks'
// post-target phase, which does not call it — and only when receipt's local
// validation was deferred to CI. It never refuses a landing and never
// lengthens a wait: it only records, for a human or a later audit, which
// required check(s) concluded "skipped" or "neutral" instead of actually
// running on the head GitHub itself judged landable.
//
// The caller only ever invokes this once the wait has actually resolved to
// something worth recording (round 4, minor 1): a landing whose checks are
// still pending or have failed is resumed in a later slice, and each resume
// re-observes the same wait's checks from scratch — calling this on every
// slice used to append one finding per resume, letting the same skip get
// recorded again and again for a landing that had not actually landed yet.
// It also replaces any existing finding with this code rather than
// appending a second one, so a receipt only ever carries one.
func recordDeferredValidationCheckSkippedFinding(receipt *WorktreeMergeReceipt, waited PullRequestWaitResult) {
	if receipt.ValidationDeferral == nil {
		return
	}
	skipped := skippedOrNeutralRequiredChecks(waited.Checks, waited.RequiredChecks)
	if len(skipped) == 0 {
		return
	}
	finding := WorktreeMergeFinding{
		Code: WorktreeMergeFindingDeferredValidationCheckSkipped,
		Message: fmt.Sprintf(
			"candidate validation was deferred to CI, and required check(s) %s concluded skipped or neutral instead of actually running; GitHub branch protection judged the head landable anyway",
			strings.Join(skipped, ", "),
		),
		Checks: skipped,
	}
	for i, existing := range receipt.Findings {
		if existing.Code == WorktreeMergeFindingDeferredValidationCheckSkipped {
			receipt.Findings[i] = finding
			return
		}
	}
	receipt.Findings = append(receipt.Findings, finding)
}

func landWorktreeMergePullRequest(ctx context.Context, receipt WorktreeMergeReceipt, options WorktreeMergeLandOptions) (WorktreeMergeReceipt, string, error) {
	// The shared engine (awaitLandablePullRequest / mergeOrAdoptAutoMerge)
	// path-builds every GraphQL/REST call it issues from this identity
	// directly - it never re-parses it the way ReadPullRequest/PullRequestNumber
	// callers do. `wb pr land` always hands it a bare number for exactly this
	// reason; receipt.PullRequest is the pull request's full HTML URL, so it
	// must be reduced to the bare number here, once, before it is threaded
	// through.
	number, numberErr := PullRequestNumber(receipt.PullRequest)
	if numberErr != nil {
		return failWorktreeMergePRLand(receipt, WorktreeMergeConflict, fmt.Errorf("resolve published pull request number: %w", numberErr))
	}
	view, err := ReadPullRequest(ctx, receipt.Repository, number)
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
		PullRequest:         number,
		ProjectsRoot:        options.ProjectsRoot,
		MergeMethod:         method,
		MergeMethodExplicit: true,
		AllowUnfenced:       options.AllowUnfenced,
		Slice:               slice,
		CheckPollInterval:   options.CheckPollInterval,
		OperationProgress:   options.Progress,
		Lane:                options.Lane,
		git:                 options.resolveGit(),
		headUpdated: func(previous, updated string) error {
			return adoptWorktreeMergeUpdateBranchAdvance(ctx, options.resolveGit(), &receipt, previous, updated)
		},
	}

	evidence := map[string]string{}
	updatedView, waited, autoMergeArmed, mergedByGitHub, updateRefusal, err := awaitLandablePullRequest(ctx, landOptions, view, number, title, body, evidence)
	receipt.AutoMergeArmed = receipt.AutoMergeArmed || autoMergeArmed
	if err != nil {
		// A transient GitHub read failure - including one the headUpdated
		// hook's own pullRequestCommitParents call surfaces while recording
		// an update-branch advance - is not a verdict on the candidate. It
		// must leave the receipt pending and retryable, not Conflict, the
		// same way every other transient read in this area (ciwait.go) is
		// treated as "ask again", not "judged".
		if IsTransientReadFailure(err) {
			return failWorktreeMergePRLand(receipt, WorktreeMergeChecksPending,
				fmt.Errorf("%w; resume with wb worktree merge resume %s", err, receipt.ReceiptPath))
		}
		return failWorktreeMergePRLand(receipt, WorktreeMergeConflict, err)
	}
	if updateRefusal != nil {
		return failWorktreeMergePRLand(receipt, WorktreeMergeConflict,
			fmt.Errorf("%s; resume with wb worktree merge resume %s", updateRefusal.reason, receipt.ReceiptPath))
	}
	receipt.Checks = waited
	// Round 3 (sneat-dev/wb#591 red-team follow-up): a deferred candidate's
	// required checks are never re-judged here — GitHub branch protection's
	// own evaluation is the gate, exactly as for any other candidate. This
	// only records a non-blocking finding, on the candidate/PR phase this
	// wait itself observed, never on the later post-target phase (see
	// waitForWorktreeMergeChecks in worktree_merge.go, which does not call
	// this helper).
	// Round 4, minor 1: only record once the wait actually resolved to a
	// landing — passed, or GitHub merged it anyway — never while it is
	// still pending or has failed, so a later resume slice does not record
	// the same finding again for a landing that has not landed yet.
	if waited.Status == PullRequestWaitPassed || mergedByGitHub {
		recordDeferredValidationCheckSkippedFinding(&receipt, waited)
	}
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
			// #584 round 3: unlike `wb pr land`, this wait's slice is
			// hard-capped at 8 minutes regardless of --timeout (see the
			// `slice` clamp above) - and --timeout here also feeds git and
			// validation timeouts, so printing a larger one would be
			// inaccurate advice, not a bigger budget. Say plainly that each
			// resume only advances by one more capped slice.
			reason = fmt.Errorf("exact-head checks remain pending: %s%s; resume in the background with wb worktree merge resume %s (each resume observes one more slice, capped at 8m)",
				waited.Reason, note, receipt.ReceiptPath)
		case !options.AllowUnfenced && strings.Contains(waited.Reason, "strict up-to-date fence"):
			reason = fmt.Errorf("exact-head checks failed: %s; resume with wb worktree merge resume %s --allow-unfenced", waited.Reason, receipt.ReceiptPath)
		default:
			// #600: name each failing check and its first error line rather
			// than leaving the caller to hand-roll the same log scraping WB
			// already did while observing the checks.
			if summary := summarizeCheckFailures(waited.FailureDetails); summary != "" {
				reason = fmt.Errorf("exact-head checks failed: %s; %s", waited.Reason, summary)
			}
		}
		return failWorktreeMergePRLand(receipt, status, reason)
	}

	head := updatedView.Head.SHA
	// Every ordinary path that advances head during the wait (the
	// headUpdated hook, on a server-side update-branch) already records the
	// new value onto receipt.Candidate.SHA before returning here. If head
	// still does not match what the receipt names, something advanced it
	// that this landing never recorded (red-team finding M-A): refuse
	// rather than merge a head the receipt does not name.
	if head != "" && head != receipt.Candidate.SHA {
		return failWorktreeMergePRLand(receipt, WorktreeMergeConflict,
			fmt.Errorf("published pull request head advanced to %s during the wait without being recorded on the receipt (still %s); resume with wb worktree merge resume %s",
				head, receipt.Candidate.SHA, receipt.ReceiptPath))
	}
	_, mergeRefusal, mergeErr := mergeOrAdoptAutoMerge(ctx, landOptions, number, head, method, title, body, autoMergeArmed, mergedByGitHub, evidence)
	if mergeErr != nil {
		if IsTransientGitHubFailure(mergeErr) {
			return failWorktreeMergePRLand(receipt, WorktreeMergeChecksPending,
				fmt.Errorf("%w; resume with wb worktree merge resume %s", mergeErr, receipt.ReceiptPath))
		}
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
func adoptWorktreeMergeUpdateBranchAdvance(ctx context.Context, git Git, receipt *WorktreeMergeReceipt, previous, updated string) error {
	if receipt == nil {
		return fmt.Errorf("worktree-merge PR-land hook called with no receipt")
	}
	// Minor 3 (review round on #614): the engine hands this hook whatever
	// head it itself last observed as "previous" - it must be exactly the
	// head this receipt already holds as its candidate. If it is not, this
	// call was never about advancing OUR recorded candidate at all (a stale
	// hook closure, a receipt reused across an unrelated retry, and so on),
	// and adopting "updated" as this receipt's own advance would silently
	// substitute a head this receipt never held. Refuse rather than adopt.
	if previous != receipt.Candidate.SHA {
		return fmt.Errorf("update-branch hook called with previous head %s but the receipt holds candidate %s; refusing to adopt an advance from a head this receipt did not hold",
			shortMergeRevision(previous), shortMergeRevision(receipt.Candidate.SHA))
	}
	parents, err := pullRequestCommitParents(ctx, receipt.Repository, updated)
	if err != nil {
		return fmt.Errorf("read update-branch merge commit parents: %w", err)
	}
	newTarget, targetErr := updateBranchMergeTargetParent(parents, previous)
	if targetErr != nil {
		return fmt.Errorf("update-branch merge commit %s: %w", updated, targetErr)
	}
	// Red-team finding M3: the GitHub commits API told us updated's parents
	// look right, but that alone does not prove updated is an ordinary merge
	// of our recorded candidate and a target-side ancestor - the same proof
	// adoptServerUpdatedWorktreeMergeHead requires before trusting a resumed
	// receipt's stale head. Reuse it here so a crafted force-push cannot be
	// recorded as a trusted advance merely because its two parents happen to
	// match the shape this function checks for. Any failure to positively
	// verify is a refusal, not a silent adoption.
	proved, proofErr := verifyUpdateBranchMergeProof(ctx, git, receipt.Candidate.Worktree, receipt.Candidate.Branch, receipt.Target, receipt.Repository, previous, newTarget, updated)
	if proofErr != nil {
		// Minor 5 (review round on #614): a transient GitHub read failure
		// while computing the proof (here, only commitTreeSHA's fallback
		// read can produce one - see verifyUpdateBranchMergeProof) is not a
		// verdict that the merge is unproven; it is a blip WB never
		// observed. Surface it unflattened so the caller's own
		// IsTransientReadFailure check (worktree_merge_pr_land.go's
		// landWorktreeMergePullRequest) classifies this as ChecksPending
		// and retryable, never Conflict.
		return fmt.Errorf("verify update-branch merge commit %s: %w", shortMergeRevision(updated), proofErr)
	}
	if !proved {
		return fmt.Errorf("update-branch merge commit %s does not prove an ordinary merge of candidate %s and target %s; refusing to adopt it as an advance",
			shortMergeRevision(updated), shortMergeRevision(previous), receipt.Target)
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
	// persisted advance above (M3). fastForwardWorktreeToUpdatedHead
	// (pr_land_local_sync.go) is the one shared helper both this hook and
	// the plain `wb pr land` route's own update-branch success path use;
	// it never errors, only reports a note.
	if note := fastForwardWorktreeToUpdatedHead(ctx, git, receipt.Candidate.Worktree, receipt.Candidate.Branch, updated); note != "" {
		receipt.LocalSync = note
	}
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

// verifyUpdateBranchMergeProof proves that headSHA is an ordinary merge of
// candidateSHA (its first parent) and targetParent (its second parent):
// that targetParent is an ancestor of the repository's current remote
// target, and that headSHA's tree exactly matches the tree
// `git merge-tree --write-tree` would produce for those two parents.
//
// Minor 4 (review round on #614): this proof runs on exactly two call
// sites, both in this file - M3 (adoptWorktreeMergeUpdateBranchAdvance,
// trusting a live update-branch merge before persisting it) and M-A
// (adoptServerUpdatedWorktreeMergeHead, trusting a resumed receipt's stale
// head) - never on the plain `wb pr land` route's own update-branch success
// path (pr_land_engine.go's non-headUpdated branch, which only calls
// syncLocalWorktreeAfterUpdateBranch). That is deliberate, not a gap: the
// plain route never persists a receipt that later code trusts for
// merge-provenance decisions (an absorbed-source-PR walk, a target-SHA
// advance) the way a worktree-merge receipt does - fastForwardWorktreeToUpdatedHead
// is a best-effort local sync only, and a wrong or missing fast-forward
// there is never treated as authoritative by anything downstream. So a
// crafted force-push landing a head that merely LOOKS like an update-branch
// merge (right parent shape, wrong content) cannot be adopted as a trusted
// advance by either of the two paths that DO trust one.
//
// It returns an error only for a transient GitHub read failure it cannot
// tell apart from a genuine mismatch without retrying (see Minor 5 below);
// every other kind of "could not prove it" - an unreadable repository, a
// missing worktree, a real mismatch - returns (false, nil), and callers do
// not need to distinguish those from each other: all of them mean "do not
// trust this as an advance" on their own.
//
// Red-team finding M4: GitHub commonly deletes a pull request's branch the
// moment it merges. Fetching by branch NAME then fails outright, even though
// headSHA itself may already be reachable locally (a plain `wb pr land`
// fast-forwarded this very worktree to it earlier) or readable from GitHub's
// commit API by its exact SHA. Only fetch the branch when headSHA is not
// already local, and fall back to reading its tree from the commits API
// (commitTreeSHA) rather than failing "not proved" merely because the branch
// name no longer resolves.
//
// Minor 5 (review round on #614): that commitTreeSHA fallback is the one
// GitHub API read in this function (every other step is local git or a
// plain `git fetch`). A transient failure there - a 502/503/504, a
// secondary rate limit, or a signal-killed `gh` attempt, exhausted after
// every in-process retry - is not a verdict that the merge is unproven; it
// is a blip WB never observed. That case alone returns (false, err) with err
// satisfying IsTransientReadFailure, so a caller can tell "ask again" apart
// from "refuse" instead of both collapsing into the same false.
func verifyUpdateBranchMergeProof(ctx context.Context, git Git, worktree, branch, target, repository, candidateSHA, targetParent, headSHA string) (bool, error) {
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return false, nil
	}
	// B5: commitExistsLocally's own error return (added spec/plans/
	// coverage-to-100 task-17's review round) is a real command failure --
	// including a guarded runner refusing to start the process at all -- not
	// "the commit is absent". It is surfaced here rather than folded into
	// headLocal=false, the same way this function already tells a transient
	// commitTreeSHA read failure below apart from a genuine mismatch.
	headLocal, headLocalErr := commitExistsLocally(ctx, git, worktree, headSHA)
	if headLocalErr != nil {
		return false, headLocalErr
	}
	if !headLocal && strings.TrimSpace(branch) != "" {
		// Best effort: bring headSHA's object in reach locally by branch name.
		// It was produced server-side by GitHub and this worktree has not
		// necessarily fetched it yet. A failure here (deleted branch) is not
		// fatal - the tree can still be read from GitHub's commit API below.
		if _, _, err := runCommand(ctx, 0, 0, worktree, "git", "fetch", "--no-tags", "origin",
			"+refs/heads/"+branch+":refs/remotes/origin/"+branch); err == nil {
			headLocal, headLocalErr = commitExistsLocally(ctx, git, worktree, headSHA)
			if headLocalErr != nil {
				return false, headLocalErr
			}
		}
	}
	remoteTarget, fetchErr := fetchExactMergeTarget(ctx, worktree, target)
	if fetchErr != nil {
		return false, nil
	}
	targetAncestor, ancestorErr := isMergeAncestor(ctx, worktree, targetParent, remoteTarget)
	if ancestorErr != nil || !targetAncestor {
		return false, nil
	}
	writtenTree, treeErr := git.MergeTreeWriteTree(ctx, worktree, candidateSHA, targetParent)
	if treeErr != nil {
		return false, nil
	}
	var headTree string
	if headLocal {
		tree, headTreeErr := git.ShowTreeFormat(ctx, worktree, headSHA)
		if headTreeErr != nil {
			return false, nil
		}
		headTree = tree
	} else {
		tree, treeErr := commitTreeSHA(ctx, repository, headSHA)
		if treeErr != nil {
			if IsTransientReadFailure(treeErr) {
				return false, treeErr
			}
			return false, nil
		}
		headTree = tree
	}
	return strings.TrimSpace(writtenTree) == strings.TrimSpace(headTree), nil
}

// commitExistsLocally reports whether sha's commit object is already
// reachable in worktree's object database, without attempting any network
// fetch. Used to skip a branch-name fetch (which fails once GitHub deletes
// the branch on merge - M4) when the object is already present.
//
// Its error return (B5) is a real command failure -- the object database is
// unreadable, or a guarded runner refused to start the process at all --
// never "the object is absent", which git.CommitObjectExists itself already
// reports as (false, nil).
func commitExistsLocally(ctx context.Context, git Git, worktree, sha string) (bool, error) {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return false, nil
	}
	return git.CommitObjectExists(ctx, worktree, sha)
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
func adoptServerUpdatedWorktreeMergeHead(ctx context.Context, git Git, receipt *WorktreeMergeReceipt) (bool, error) {
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
	// This resume path is deliberately best-effort end to end (see the
	// function doc above): the transient-vs-definitive distinction
	// verifyUpdateBranchMergeProof's error return exists for is Minor 5's
	// concern for the LIVE landing hook (adoptWorktreeMergeUpdateBranchAdvance),
	// which must not turn a retryable blip into a hard Conflict. Here, any
	// failure - transient or not - already falls through to "leave it for
	// the ordinary drift/conflict handling to judge", so the error is
	// intentionally discarded rather than given special treatment.
	proved, _ := verifyUpdateBranchMergeProof(ctx, git, receipt.Candidate.Worktree, receipt.Candidate.Branch, receipt.Target, receipt.Repository, receipt.Candidate.SHA, parents[1], view.Head.SHA)
	if !proved {
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
	if note := fastForwardWorktreeToUpdatedHead(ctx, git, receipt.Candidate.Worktree, receipt.Candidate.Branch, view.Head.SHA); note != "" {
		receipt.LocalSync = note
	}
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
	return worktreeMergeRefreshChainAdvances(receipt.TargetRefreshes, from, to, func(refresh WorktreeMergeTargetRefresh) (string, string) {
		return refresh.PreviousTargetSHA, refresh.NewTargetSHA
	})
}

// worktreeMergeCandidateAdvanceRecorded is worktreeMergeTargetAdvanceRecorded's
// counterpart for the candidate side of the same TargetRefreshes entries:
// it proves receipt.Candidate.SHA advanced from `from` to `to` through one
// or more recorded server-side update-branch merges (red-team finding M2 -
// a snapshot taken before ANY number of update-branch advances, not just
// one, must still be recognized as legitimate history rather than a
// mismatch).
func worktreeMergeCandidateAdvanceRecorded(receipt WorktreeMergeReceipt, from, to string) bool {
	return worktreeMergeRefreshChainAdvances(receipt.TargetRefreshes, from, to, func(refresh WorktreeMergeTargetRefresh) (string, string) {
		return refresh.PreviousCandidateSHA, refresh.NewCandidateSHA
	})
}

// worktreeMergeRefreshChainAdvances walks receipt.TargetRefreshes as a chain
// of (previous -> new) hops - following however many recorded advances it
// takes, not just the first or the last - to prove `from` reaches `to`.
// `pair` selects which of TargetRefresh's two parallel (target, candidate)
// hop pairs to walk.
func worktreeMergeRefreshChainAdvances(refreshes []WorktreeMergeTargetRefresh, from, to string, pair func(WorktreeMergeTargetRefresh) (string, string)) bool {
	if from == to {
		return true
	}
	current := from
	visited := map[string]bool{current: true}
	for {
		advanced := false
		for _, refresh := range refreshes {
			previous, next := pair(refresh)
			if previous == current && !visited[next] {
				current = next
				advanced = true
				break
			}
		}
		if !advanced {
			return false
		}
		if current == to {
			return true
		}
		visited[current] = true
	}
}
