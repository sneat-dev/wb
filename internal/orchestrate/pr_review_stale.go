package orchestrate

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
)

// LandRefusalReviewStale is distinct from LandRefusalHeadMoved (#586):
// head-moved protects the gap between one invocation's own observation and
// its own merge write; review-stale protects the much longer gap between
// when a review was recorded and whenever a landing later executes against
// it. A rebase, a "fix lint" commit from another agent, or a force-push
// between review and land produces exactly this: a merge of an unreviewed
// head carrying a stale "Review:" line.
const LandRefusalReviewStale = "review-stale"

// maxReviewAdvanceHops bounds the walk back from the current head toward
// the reviewed one. A real WB update-branch chain during one landing is at
// most a handful of hops (the target moving once per poll); a value this
// far past that is itself evidence something is wrong, and refusing rather
// than looping forever is the safe failure.
const maxReviewAdvanceHops = 25

// reviewHeadAdvanceProof is the pluggable core of the head-binding check: it
// decides whether headSHA is an ordinary merge of candidateSHA (its first
// parent) and targetParent (its second parent) — the shape WB's own
// update-branch produces — by proving targetParent is an ancestor of the
// repository's current remote target and that headSHA's tree exactly
// matches the tree `git merge-tree --write-tree` would produce for those two
// parents. Tests substitute a fake so the mechanism is exercised without a
// live git checkout; the production implementation reuses the exact
// primitives `adoptServerUpdatedWorktreeMergeHead` already uses for the same
// proof.
var reviewHeadAdvanceProof = verifyReviewHeadAdvanceProof

// reviewCommitParents is the pluggable lookup reviewedHeadAdvanceChain uses
// for a commit's parent SHAs. Tests substitute a fake alongside
// reviewHeadAdvanceProof so the walk needs neither a live git checkout nor
// a live GitHub.
var reviewCommitParents = pullRequestCommitParents

func verifyReviewHeadAdvanceProof(ctx context.Context, worktree, target, candidateSHA, targetParent, headSHA string) bool {
	if strings.TrimSpace(worktree) == "" {
		return false
	}
	remoteTarget, fetchErr := fetchExactMergeTarget(ctx, worktree, target)
	if fetchErr != nil {
		return false
	}
	targetAncestor, ancestorErr := isMergeAncestor(ctx, worktree, targetParent, remoteTarget)
	if ancestorErr != nil || !targetAncestor {
		return false
	}
	writtenTree, treeErr := runGit(ctx, worktree, "merge-tree", "--write-tree", candidateSHA, targetParent)
	if treeErr != nil {
		return false
	}
	headTree, headTreeErr := runGit(ctx, worktree, "show", "-s", "--format=%T", headSHA)
	if headTreeErr != nil {
		return false
	}
	return strings.TrimSpace(writtenTree) == strings.TrimSpace(headTree)
}

// reviewedHeadStillCurrent walks the first-parent chain back from
// currentHead toward reviewedHead, requiring every hop in between to be an
// update-branch merge WB's own advance would have produced (proved by
// reviewHeadAdvanceProof). Any other kind of new commit — a foreign push, a
// fix commit, a force-push — breaks the chain and this returns false.
//
// currentHead == reviewedHead (the ordinary case: nothing moved) returns
// true without any git call.
func reviewedHeadStillCurrent(ctx context.Context, options PullRequestLandOptions, view PullRequestView, reviewedHead, currentHead string) bool {
	reviewedHead = strings.TrimSpace(reviewedHead)
	currentHead = strings.TrimSpace(currentHead)
	if reviewedHead == "" || currentHead == "" || reviewedHead == currentHead {
		return true
	}
	worktree, _, found, locateErr := locateBranchCheckout(ctx, options.ProjectsRoot, options.Repository, view.Head.Ref, view.Base.Ref)
	if locateErr != nil || !found || strings.TrimSpace(worktree) == "" {
		// No local checkout to prove the advance against: refuse rather than
		// trust an unprovable chain. This is the safe default — a false
		// "stale" refusal costs a re-review; a false "still current" would
		// land an unreviewed change.
		return false
	}
	// The commits between reviewedHead and currentHead (an update-branch
	// merge GitHub produced server-side, or a reviewedHead recorded in an
	// earlier invocation) are not necessarily in this worktree's object
	// database yet. Best effort: bring the branch's current tip into reach
	// before the walk needs its objects; a failure here (a deleted branch
	// after merge) is not fatal — the walk below simply fails its own proof
	// if the objects genuinely cannot be read.
	_, _, _ = runCommand(ctx, 0, 0, worktree, "git", "fetch", "--no-tags", "origin",
		"+refs/heads/"+view.Head.Ref+":refs/remotes/origin/"+view.Head.Ref)
	return reviewedHeadAdvanceChain(ctx, worktree, options.Repository, view.Base.Ref, reviewedHead, currentHead)
}

// reviewedHeadAdvanceChain is reviewedHeadStillCurrent's pure core, taking
// the checkout directly rather than locating one — the seam tests use to
// exercise the walk with reviewHeadAdvanceProof faked, without a real git
// checkout or worktree inventory.
func reviewedHeadAdvanceChain(ctx context.Context, worktree, repository, target, reviewedHead, currentHead string) bool {
	head := currentHead
	for hop := 0; hop < maxReviewAdvanceHops; hop++ {
		if head == reviewedHead {
			return true
		}
		parents, err := reviewCommitParents(ctx, repository, head)
		if err != nil || len(parents) != 2 {
			return false
		}
		if !reviewHeadAdvanceProof(ctx, worktree, target, parents[0], parents[1], head) {
			return false
		}
		head = parents[0]
	}
	return false
}

// reviewedHeadLinePattern matches the machine-readable "Reviewed-Head: <sha>"
// line: WB's own posted identity-form comments always carry it (#604), and a
// file or PR/issue-comment review may carry it anywhere in its text (#586,
// founder-decided 2026-09-18: warn, still land, rather than refuse a review
// that names no head).
var reviewedHeadLinePattern = regexp.MustCompile(`(?m)^Reviewed-Head:\s*([0-9a-fA-F]{7,40})\s*$`)

// parseReviewedHeadLine extracts the "Reviewed-Head: <sha>" line's SHA from
// arbitrary text, or "" when no such line is present.
func parseReviewedHeadLine(text string) string {
	match := reviewedHeadLinePattern.FindStringSubmatch(text)
	if match == nil {
		return ""
	}
	return strings.TrimSpace(match[1])
}

// issueCommentURLPattern matches a GitHub pull-request or issue comment URL:
// https://github.com/<owner>/<repo>/(pull|issues)/<n>#issuecomment-<id>. Any
// other URL shape (a plain PR URL with no comment anchor, a non-GitHub host)
// names no fetchable comment and is left unbound.
var issueCommentURLPattern = regexp.MustCompile(`^https://github\.com/([^/]+/[^/]+)/(?:pull|issues)/\d+#issuecomment-(\d+)$`)

// namedReviewedHead resolves the commit a file or URL review names, per
// #586's binding rule: a file's own "Reviewed-Head: <sha>" line anywhere in
// it, or — for a URL pointing at a GitHub pull-request or issue comment —
// that same line fetched from the comment's body via the GitHub API. It
// returns "" (never an error) for a review that names no head, or a URL
// this cannot resolve: the caller treats that as "review-unbound", an
// informational finding, never a refusal.
func namedReviewedHead(ctx context.Context, approvedBy string, kind approvalKind) string {
	switch kind {
	case approvalKindFile:
		data, err := os.ReadFile(approvedBy)
		if err != nil {
			return ""
		}
		return parseReviewedHeadLine(string(data))
	case approvalKindURL:
		body, ok := fetchIssueCommentBody(ctx, approvedBy)
		if !ok {
			return ""
		}
		return parseReviewedHeadLine(body)
	default:
		return ""
	}
}

type issueCommentBodyResponse struct {
	Body string `json:"body"`
}

// fetchIssueCommentBody reads one GitHub issue/PR comment's body by its URL,
// for namedReviewedHead's URL case.
func fetchIssueCommentBody(ctx context.Context, url string) (string, bool) {
	match := issueCommentURLPattern.FindStringSubmatch(strings.TrimSpace(url))
	if match == nil {
		return "", false
	}
	repository, commentID := match[1], match[2]
	response := githubExecute(ctx, "", "api", "repos/"+repository+"/issues/comments/"+commentID)
	if response.Err != nil {
		return "", false
	}
	var decoded issueCommentBodyResponse
	if err := json.Unmarshal(response.Stdout, &decoded); err != nil {
		return "", false
	}
	return decoded.Body, true
}

// reviewStaleRefusal is the landRefusal `landPullRequest` returns when the
// reviewed head is no longer current. Auto-merge is never disarmed here —
// WB never disarms it — so when it is armed the refusal says so explicitly,
// naming what will still happen without WB.
func reviewStaleRefusal(ctx context.Context, options PullRequestLandOptions, view PullRequestView, reviewedHead, currentHead string, autoMergeArmed bool, number string) *landRefusal {
	if reviewedHeadStillCurrent(ctx, options, view, reviewedHead, currentHead) {
		return nil
	}
	reason := "the pull request's head (" + shortMergeRevision(currentHead) +
		") is not the head that was reviewed (" + shortMergeRevision(reviewedHead) +
		"); it moved by more than WB's own update-branch merges since the review was recorded"
	if autoMergeArmed {
		reason += "; auto-merge remains armed; GitHub will merge on green unless you disable it"
	}
	return &landRefusal{
		code:    LandRefusalReviewStale,
		reason:  reason,
		command: "review the current head, then: wb pr land " + options.Repository + "#" + number + " --approved-by <fresh review>",
	}
}
