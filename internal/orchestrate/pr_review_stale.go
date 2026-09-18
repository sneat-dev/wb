package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// LandRefusalReviewStale is distinct from LandRefusalHeadMoved (#586):
// head-moved protects the gap between one invocation's own observation and
// its own merge write; review-stale protects the much longer gap between
// when a review was recorded and whenever a landing later executes against
// it. A rebase, a "fix lint" commit from another agent, or a force-push
// between review and land produces exactly this: a merge of an unreviewed
// head carrying a stale "Review:" line.
const LandRefusalReviewStale = "review-stale"

// LandRefusalReviewHeadMalformed reports a "Reviewed-Head:" line whose value
// is not a full 40-hex SHA (round 3, minor 1). A short SHA is never treated
// as a legitimate reviewed head to compare against: it can never again
// equal a real 40-hex current head, so silently accepting it would produce
// a permanent, unresolvable "review-stale" rather than a clear parse error
// the caller can fix by writing the SHA in full.
const LandRefusalReviewHeadMalformed = "review-head-malformed"

// LandRefusalReviewCommentCrossRepo reports a review comment URL that names
// a repository or pull/issue number other than the one being landed (round
// 3, minor 2). Fetching and trusting the wrong PR's comment would bind this
// landing to a head an entirely different review discusses.
const LandRefusalReviewCommentCrossRepo = "review-comment-cross-repo"

// maxReviewAdvanceHops bounds the walk back from the current head toward
// the reviewed one. A real WB update-branch chain during one landing is at
// most a handful of hops (the target moving once per poll); a value this
// far past that is itself evidence something is wrong, and refusing rather
// than looping forever is the safe failure.
const maxReviewAdvanceHops = 25

// reviewHeadAdvanceProof is the pluggable core of the head-binding check. It
// delegates to verifyUpdateBranchMergeProof (worktree_merge_pr_land.go) —
// the same primitives adoptServerUpdatedWorktreeMergeHead uses to prove a
// server-side update-branch advance — rather than duplicating that git/API
// plumbing a second time (round 3, minor 4: this was a stale, weaker copy
// that never got verifyUpdateBranchMergeProof's M4/M5 fixes: the
// deleted-branch commit-tree API fallback, and telling a transient GitHub
// read failure apart from a genuine mismatch). Tests substitute a fake so
// the walk is exercised without a live git checkout or GitHub.
var reviewHeadAdvanceProof = verifyUpdateBranchMergeProof

// reviewCommitParents is the pluggable lookup reviewedHeadAdvanceChain uses
// for a commit's parent SHAs. Tests substitute a fake alongside
// reviewHeadAdvanceProof so the walk needs neither a live git checkout nor
// a live GitHub.
var reviewCommitParents = pullRequestCommitParents

// reviewedHeadStillCurrent walks the first-parent chain back from
// currentHead toward reviewedHead, requiring every hop in between to be an
// update-branch merge WB's own advance would have produced (proved by
// reviewHeadAdvanceProof). Any other kind of new commit — a foreign push, a
// fix commit, a force-push — breaks the chain and this returns
// advanced=false, unverifiable=false.
//
// currentHead == reviewedHead (the ordinary case: nothing moved) returns
// advanced=true without any git call.
//
// unverifiable=true means the walk could not run at all — no local checkout
// of this branch, no canonical clone of the repository either (round 3,
// minor 5), or a transient GitHub read failure mid-walk — and is distinct
// from advanced=false: the caller must not refuse review-stale on an
// unverifiable result, since a false "stale" here is exactly the false
// refusal that made landing another pull request after WB's own
// update-branch fail outright. It records a finding and lands instead.
func reviewedHeadStillCurrent(ctx context.Context, options PullRequestLandOptions, view PullRequestView, reviewedHead, currentHead string) (advanced, unverifiable bool, cause string) {
	reviewedHead = strings.ToLower(strings.TrimSpace(reviewedHead))
	currentHead = strings.ToLower(strings.TrimSpace(currentHead))
	if reviewedHead == "" || currentHead == "" || reviewedHead == currentHead {
		return true, false, ""
	}
	worktree, branch, found := resolveReviewCheckout(ctx, options, view)
	if !found {
		return false, true, "no local checkout of this repository was available to prove (or disprove) an update-branch advance"
	}
	// The commits between reviewedHead and currentHead (an update-branch
	// merge GitHub produced server-side, or a reviewedHead recorded in an
	// earlier invocation) are not necessarily in this checkout's object
	// database yet. Best effort: bring the branch's current tip into reach
	// before the walk needs its objects; a failure here (a deleted branch
	// after merge) is not fatal — verifyUpdateBranchMergeProof's own M4
	// fallback (the commits API) covers a head this fetch could not reach.
	_, _, _ = runCommand(ctx, 0, 0, worktree, "git", "fetch", "--no-tags", "origin",
		"+refs/heads/"+branch+":refs/remotes/origin/"+branch)
	return reviewedHeadAdvanceChain(ctx, worktree, branch, options.Repository, view.Base.Ref, reviewedHead, currentHead)
}

// resolveReviewProofCheckout finds a local git checkout the proof can run
// git plumbing against. It prefers a linked worktree with this exact branch
// checked out (locateBranchCheckout, as landKeepingCommits also uses), but
// none of the proof steps actually need that branch checked out — they only
// need a clone of the same repository with network access to fetch
// arbitrary commit-ish refs from origin. Round 3, minor 5: falling back to
// the repository's canonical clone (present for any repository WB has ever
// operated on, whether or not this particular branch has its own worktree)
// is what makes landing a second, unrelated pull request after WB's own
// update-branch on a different branch no longer false-refuse for want of a
// branch-specific checkout that was never going to exist.
// resolveReviewCheckout is the pluggable seam over resolveReviewProofCheckout
// tests substitute so the walk can be exercised without a real worktree
// inventory or canonical clone on disk.
var resolveReviewCheckout = resolveReviewProofCheckout

func resolveReviewProofCheckout(ctx context.Context, options PullRequestLandOptions, view PullRequestView) (worktree, branch string, found bool) {
	if canonical, _, ok, err := locateBranchCheckout(ctx, options.ProjectsRoot, options.Repository, view.Head.Ref, view.Base.Ref); err == nil && ok && strings.TrimSpace(canonical) != "" {
		return canonical, view.Head.Ref, true
	}
	if canonicalPath, err := worktrees.CanonicalRepositoryPath(options.ProjectsRoot, options.Repository); err == nil && strings.TrimSpace(canonicalPath) != "" {
		if info, statErr := os.Stat(canonicalPath); statErr == nil && info.IsDir() {
			return canonicalPath, view.Head.Ref, true
		}
	}
	return "", "", false
}

// reviewedHeadAdvanceChain is reviewedHeadStillCurrent's pure core, taking
// the checkout directly rather than locating one — the seam tests use to
// exercise the walk with reviewHeadAdvanceProof faked, without a real git
// checkout or worktree inventory.
func reviewedHeadAdvanceChain(ctx context.Context, worktree, branch, repository, target, reviewedHead, currentHead string) (advanced, unverifiable bool, cause string) {
	head := currentHead
	for hop := 0; hop < maxReviewAdvanceHops; hop++ {
		if strings.EqualFold(head, reviewedHead) {
			return true, false, ""
		}
		parents, err := reviewCommitParents(ctx, repository, head)
		if err != nil {
			if IsTransientReadFailure(err) {
				return false, true, "a transient GitHub read failure interrupted the check (" + err.Error() + ")"
			}
			return false, false, ""
		}
		if len(parents) != 2 {
			return false, false, ""
		}
		proven, proofErr := reviewHeadAdvanceProof(ctx, worktree, branch, target, repository, parents[0], parents[1], head)
		if proofErr != nil {
			if IsTransientReadFailure(proofErr) {
				return false, true, "a transient GitHub read failure interrupted the check (" + proofErr.Error() + ")"
			}
			return false, false, ""
		}
		if !proven {
			return false, false, ""
		}
		head = parents[0]
	}
	return false, false, ""
}

// reviewedHeadLinePattern matches the machine-readable "Reviewed-Head: <sha>"
// line: WB's own posted identity-form comments always carry it (#604), and a
// file or PR/issue-comment review may carry it anywhere in its text (#586,
// founder-decided 2026-09-18: warn, still land, rather than refuse a review
// that names no head). Round 3, minor 1: the trailing run uses [ \t]*, never
// \s*, because \s matches a newline too — an unbounded \s* would let the
// line's value bleed into the next line of the review text. The captured
// value is deliberately unrestricted in length here; parseReviewedHeadLine
// itself enforces the full 40-hex shape so a short SHA is reported as a
// parse error, not silently accepted as a permanently-unmatchable head.
var reviewedHeadLinePattern = regexp.MustCompile(`(?m)^Reviewed-Head:[ \t]*([0-9a-fA-F]+)[ \t]*$`)

// errReviewedHeadMalformed is returned by parseReviewedHeadLine/namedReviewedHead
// when a "Reviewed-Head:" line is present but its value is not a full
// 40-hex SHA.
var errReviewedHeadMalformed = errors.New("Reviewed-Head line is not a full 40-character SHA")

// errReviewedHeadCrossRepository is returned by namedReviewedHead when a
// comment URL names a repository or pull/issue number other than the one
// being landed.
var errReviewedHeadCrossRepository = errors.New("review comment URL names a different repository or pull request")

// parseReviewedHeadLine extracts the "Reviewed-Head: <sha>" line's SHA
// (normalized to lowercase) from arbitrary text. It returns ("", nil) when
// no such line is present, and ("", errReviewedHeadMalformed) when a line is
// present but its value is not a full 40-hex SHA.
func parseReviewedHeadLine(text string) (string, error) {
	match := reviewedHeadLinePattern.FindStringSubmatch(text)
	if match == nil {
		return "", nil
	}
	value := strings.ToLower(strings.TrimSpace(match[1]))
	if len(value) != 40 {
		return "", errReviewedHeadMalformed
	}
	return value, nil
}

// issueCommentURLPattern matches a GitHub pull-request or issue comment URL:
// https://github.com/<owner>/<repo>/(pull|issues)/<n>#issuecomment-<id>. Any
// other URL shape (a plain PR URL with no comment anchor, a non-GitHub host)
// names no fetchable comment and is left unbound.
var issueCommentURLPattern = regexp.MustCompile(`^https://github\.com/([^/]+/[^/]+)/(?:pull|issues)/(\d+)#issuecomment-(\d+)$`)

// namedReviewedHead resolves the commit a file or URL review names, per
// #586's binding rule: a file's own "Reviewed-Head: <sha>" line anywhere in
// it, or — for a URL pointing at a GitHub pull-request or issue comment —
// that same line fetched from the comment's body via the GitHub API.
//
// It returns ("", nil) for a review that names no head, or a URL this
// cannot resolve: the caller treats that as "review-unbound", an
// informational finding, never a refusal. A non-nil error (errReviewedHeadMalformed
// or errReviewedHeadCrossRepository) is different: the caller refuses,
// because these are cases where the review artifact plainly asserts
// something and it does not check out — never silently downgraded to
// "unbound".
func namedReviewedHead(ctx context.Context, approvedBy string, kind approvalKind, repository, number string) (string, error) {
	switch kind {
	case approvalKindFile:
		data, err := os.ReadFile(approvedBy)
		if err != nil {
			return "", nil
		}
		return parseReviewedHeadLine(string(data))
	case approvalKindURL:
		body, ok, crossRepo := fetchIssueCommentBody(ctx, approvedBy, repository, number)
		if crossRepo {
			return "", errReviewedHeadCrossRepository
		}
		if !ok {
			return "", nil
		}
		return parseReviewedHeadLine(body)
	default:
		return "", nil
	}
}

type issueCommentBodyResponse struct {
	Body string `json:"body"`
}

// fetchIssueCommentBody reads one GitHub issue/PR comment's body by its URL,
// for namedReviewedHead's URL case. Round 3, minor 2: it verifies the URL's
// owner/repo and pull/issue number match the landing being performed before
// trusting anything fetched from it — crossRepo=true means the URL parsed
// fine but named a different repository or PR/issue, so the caller refuses
// rather than silently fetching (and binding this landing to) someone
// else's review.
func fetchIssueCommentBody(ctx context.Context, url, repository, number string) (body string, ok, crossRepo bool) {
	match := issueCommentURLPattern.FindStringSubmatch(strings.TrimSpace(url))
	if match == nil {
		return "", false, false
	}
	urlRepository, urlNumber := match[1], match[2]
	if !strings.EqualFold(urlRepository, strings.TrimSpace(repository)) || urlNumber != strings.TrimSpace(number) {
		return "", false, true
	}
	response := githubExecute(ctx, "", "api", "repos/"+urlRepository+"/issues/comments/"+match[3])
	if response.Err != nil {
		return "", false, false
	}
	var decoded issueCommentBodyResponse
	if err := json.Unmarshal(response.Stdout, &decoded); err != nil {
		return "", false, false
	}
	return decoded.Body, true, false
}

// recordMergedByGitHubReviewBinding is B3's third check (round 3): when
// GitHub's own armed auto-merge already landed the pull request — before
// this process's own review-stale check ever ran, because GitHub, not this
// process, performed the merge write — the merge cannot be undone, so this
// never refuses. But the receipt must never claim the review is bound to a
// head it cannot prove: it verifies the merged head (view.Head.SHA) is the
// reviewed one, or provably descends from it solely through WB/GitHub
// update-branch merges, and downgrades to an honest "review-unbound"
// finding (never a false review_bound: true) when it is not, or when the
// binding could not be verified at all.
func recordMergedByGitHubReviewBinding(ctx context.Context, options PullRequestLandOptions, view PullRequestView, reviewedHead string, result *PullRequestLandResult) {
	if advanced, _, _ := reviewedHeadStillCurrent(ctx, options, view, reviewedHead, view.Head.SHA); advanced {
		return
	}
	result.ReviewBound = boolPtr(false)
	result.Evidence["review"] = "review-unbound: GitHub's armed auto-merge landed " + shortMergeRevision(view.Head.SHA) +
		", which this review does not provably cover (reviewed " + shortMergeRevision(reviewedHead) + ")"
}

// reviewStaleRefusal is the landRefusal `landPullRequest` returns when the
// reviewed head is provably no longer current. Auto-merge is never disarmed
// here — WB never disarms it — so when it is armed the refusal says so
// explicitly, naming what will still happen without WB.
//
// The second return is a non-empty note, never a refusal, when the binding
// could not be verified at all — no local checkout anywhere, or a transient
// GitHub read failure mid-walk, named as the actual cause (round 4, minor
// 5) rather than always blamed on "no local checkout" regardless of which
// one actually happened: the caller records it as a finding and lands,
// rather than refusing on a check that never actually ran.
func reviewStaleRefusal(ctx context.Context, options PullRequestLandOptions, view PullRequestView, reviewedHead, currentHead string, autoMergeArmed bool, number string) (*landRefusal, string) {
	advanced, unverifiable, cause := reviewedHeadStillCurrent(ctx, options, view, reviewedHead, currentHead)
	if advanced {
		return nil, ""
	}
	if unverifiable {
		if strings.TrimSpace(cause) == "" {
			cause = "the check could not be verified"
		}
		return nil, "the review's binding to " + shortMergeRevision(reviewedHead) +
			" could not be verified against the current head " + shortMergeRevision(currentHead) +
			": " + cause
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
	}, ""
}
