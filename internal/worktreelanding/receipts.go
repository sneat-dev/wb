package worktreelanding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/worktreeproof"
)

// GitHubPullRequest is the neutral API shape shared by branch inventory and
// landing proofs. An API response is evidence only after exact Git checks.
type GitHubPullRequest struct {
	Number         int        `json:"number"`
	URL            string     `json:"html_url"`
	State          string     `json:"state"`
	Base           GitHubRef  `json:"base"`
	Head           GitHubRef  `json:"head"`
	MergeCommitSHA string     `json:"merge_commit_sha"`
	MergedAt       *time.Time `json:"merged_at"`
}

type GitHubRef struct {
	Ref  string            `json:"ref"`
	SHA  string            `json:"sha"`
	Repo *GitHubRepository `json:"repo"`
}

type GitHubRepository struct {
	FullName string `json:"full_name"`
}

type PullRequest = worktreeproof.PullRequest

// GitHubCommand and GitHubGet carry only the response fields receipt policy
// needs. The facade supplies the actual authenticated observer operations.
type GitHubCommand struct {
	Stdout, Stderr []byte
	Err            error
}

type GitHubGetRequest struct {
	Dir, Repository, Target, Endpoint string
}

type ReceiptPorts struct {
	Git           worktreeproof.GitQuery
	IsAncestor    func(context.Context, string, string, string) (bool, error)
	ValidBranch   func(context.Context, string) bool
	GitHubExecute func(context.Context, string, ...string) GitHubCommand
	GitHubGet     func(context.Context, GitHubGetRequest) ([]byte, error)
	MergeTreeRun  func(context.Context, string, string, string) (stdout, stderr string, exitCode int, err error)
}

type ReceiptService struct{ Ports ReceiptPorts }

// AbsorbedReceipt identifies the exact landing commit and its optional
// immutable GitHub receipt after source and target corroboration.
type AbsorbedReceipt struct {
	LandingSHA  string
	PullRequest *PullRequest
}

// GitHubPullRequests reads pull requests associated with the immutable source
// commit rather than filtering by the current branch name. A branch can be
// renamed, deleted, or (as in a rebase merge) differ from the managed
// worktree's branch while the exact head SHA remains the durable receipt.
func (service ReceiptService) GitHubPullRequests(ctx context.Context, worktree, repository, head string) ([]GitHubPullRequest, error) {
	pullRequests, _, err := service.GitHubPullRequestsForCommit(ctx, worktree, repository, head)
	return pullRequests, err
}

// GitHubPullRequestsForCommit additionally reports whether GitHub knows the
// commit at all. A commit it has never seen was never pushed, and a checkout
// holding one is the single class that can still lose work — so it is the one
// class no widening may ever retire, and saying "never pushed" out loud is the
// difference between a refusal an operator can act on and a mystery.
func (service ReceiptService) GitHubPullRequestsForCommit(ctx context.Context, worktree, repository, head string) ([]GitHubPullRequest, bool, error) {
	result := service.Ports.GitHubExecute(ctx, worktree, "api", "--paginate", "repos/"+repository+"/commits/"+head+"/pulls")
	if result.Err != nil {
		if service.UnknownGitHubCommit(result.Stdout) {
			// A commit GitHub has never seen has no pull request associated
			// with it, which is an answer rather than a failure. Local commits
			// on an unpushed branch are ordinary, and treating them as fatal
			// hid the whole worktree behind a malformed-candidate diagnostic —
			// including from --absorbed-by, which exists precisely for work
			// that reached the target without this commit ever being pushed.
			return nil, false, nil
		}
		return nil, false, fmt.Errorf(
			"query pull requests for %s source commit %s: %w: %s",
			repository, head, result.Err, strings.TrimSpace(string(result.Stderr)+string(result.Stdout)),
		)
	}
	var pullRequests []GitHubPullRequest
	if err := json.Unmarshal(result.Stdout, &pullRequests); err != nil {
		return nil, true, fmt.Errorf("decode pull requests for %s source commit %s: %w", repository, head, err)
	}
	return pullRequests, true, nil
}

// GitHubPullRequestsForBranch reads closed pull requests for an exact recorded
// source branch. It is used only after that branch disappeared from origin:
// GitHub keeps the PR's immutable head SHA after deleting its ref, whereas the
// commit-to-PR index can point solely to the earlier PR into that branch.
func (service ReceiptService) GitHubPullRequestsForBranch(ctx context.Context, worktree, repository, branch, base string) ([]GitHubPullRequest, error) {
	return service.GitHubPullRequestsForBranchWithExecute(ctx, worktree, repository, branch, base, service.Ports.GitHubExecute)
}

func (service ReceiptService) GitHubPullRequestsForBranchWithExecute(
	ctx context.Context,
	worktree, repository, branch, base string,
	execute func(context.Context, string, ...string) GitHubCommand,
) ([]GitHubPullRequest, error) {
	owner, _, ok := strings.Cut(repository, "/")
	if !ok || owner == "" || branch == "" || base == "" {
		return nil, fmt.Errorf("query exact merged pull request requires repository, source branch, and default base")
	}
	query := url.Values{
		"base":  []string{base},
		"head":  []string{owner + ":" + branch},
		"state": []string{"closed"},
	}.Encode()
	result := execute(ctx, worktree, "api", "--paginate", "repos/"+repository+"/pulls?"+query)
	if result.Err != nil {
		return nil, fmt.Errorf("query pull requests for deleted target %s in %s: %w: %s", branch, repository, result.Err, strings.TrimSpace(string(result.Stderr)+string(result.Stdout)))
	}
	var pullRequests []GitHubPullRequest
	if err := json.Unmarshal(result.Stdout, &pullRequests); err != nil {
		return nil, fmt.Errorf("decode pull requests for deleted target %s in %s: %w", branch, repository, err)
	}
	return pullRequests, nil
}

// exactDeletedTargetDefaultBranchReceipt selects the only receipt that may
// replace a missing recorded target. It binds the recorded branch and current
// worktree head to a merged PR into the repository default branch, and keeps
// both immutable GitHub commit identities for the subsequent ancestry check.
func (service ReceiptService) ExactDeletedTargetDefaultBranchReceipt(ctx context.Context, worktree, repository, recordedTarget, defaultBase, head string) (*PullRequest, error) {
	if !service.Ports.ValidBranch(ctx, recordedTarget) || !service.Ports.ValidBranch(ctx, defaultBase) || !worktreeproof.IsGitObjectID(head) {
		return nil, fmt.Errorf("invalid deleted-target recovery identity")
	}
	pullRequests, err := service.GitHubPullRequestsForBranch(ctx, worktree, repository, recordedTarget, defaultBase)
	if err != nil {
		return nil, err
	}
	return service.SelectExactDeletedTargetDefaultBranchReceipt(ctx, repository, pullRequests, recordedTarget, defaultBase, head)
}

func (service ReceiptService) SelectExactDeletedTargetDefaultBranchReceipt(ctx context.Context, repository string, pullRequests []GitHubPullRequest, recordedTarget, defaultBase, head string) (*PullRequest, error) {
	var receipt *PullRequest
	for _, candidate := range pullRequests {
		if candidate.MergedAt == nil || !strings.EqualFold(candidate.State, "closed") ||
			candidate.Head.Ref != recordedTarget || candidate.Head.SHA != head ||
			candidate.Base.Ref != defaultBase || !worktreeproof.IsGitObjectID(candidate.Head.SHA) || !worktreeproof.IsGitObjectID(candidate.MergeCommitSHA) {
			continue
		}
		candidateReceipt := service.MergedPullRequestReceipt(repository, candidate)
		if receipt != nil && (receipt.Number != candidateReceipt.Number || receipt.MergeSHA != candidateReceipt.MergeSHA) {
			return nil, fmt.Errorf("multiple exact merged pull-request receipts found for deleted target %s at head %s", recordedTarget, head)
		}
		receipt = candidateReceipt
	}
	return receipt, nil
}

// unknownGitHubCommit recognizes only GitHub's own structured answer that the
// commit does not exist there. It reads the API error body rather than
// matching human-readable text anywhere in the output, so an unrelated
// failure that merely mentions a commit is never mistaken for this one.
func (service ReceiptService) UnknownGitHubCommit(body []byte) bool {
	var failure struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(body), &failure); err != nil {
		return false
	}
	return failure.Status == "422" && strings.HasPrefix(failure.Message, "No commit found for SHA")
}

func (service ReceiptService) MatchingPullRequests(pullRequests []GitHubPullRequest, repository, base, branch, head string) (open, merged *PullRequest) {
	for _, candidate := range pullRequests {
		pullRequest := &PullRequest{
			Number: candidate.Number, URL: candidate.URL, State: candidate.State,
			Repository: repository, Base: candidate.Base.Ref, BaseSHA: candidate.Base.SHA, HeadSHA: candidate.Head.SHA, Merged: candidate.MergedAt,
		}
		if candidate.MergedAt != nil {
			pullRequest.State = "MERGED"
		}
		pullRequest.MergeSHA = candidate.MergeCommitSHA
		if strings.EqualFold(candidate.State, "OPEN") {
			// A SHA can name several branches after a child lands directly or
			// starts with zero changes over its target. Only the exact source
			// repository and branch owns an open-PR cleanup veto. Merged PR
			// recovery below deliberately remains bound to immutable head/base
			// identities because its source ref may already be renamed or gone.
			if candidate.Head.SHA != head || candidate.Head.Ref != branch || candidate.Head.Repo == nil ||
				!strings.EqualFold(candidate.Head.Repo.FullName, repository) {
				continue
			}
			if open == nil || candidate.Number > open.Number {
				open = pullRequest
			}
			continue
		}
		if !strings.EqualFold(pullRequest.State, "MERGED") ||
			candidate.Base.Ref != base ||
			candidate.Head.SHA != head ||
			candidate.MergedAt == nil {
			continue
		}
		if merged == nil || candidate.MergedAt.After(*merged.Merged) {
			merged = pullRequest
		}
	}
	return open, merged
}

// mergedPullRequestTarget returns the replacement target branch of an exact-head
// merged PR when the recorded lifecycle target is missing or stale. It deliberately
// refuses ambiguity: two merged PRs for the same head targeting different
// branches do not identify which remote target should authorize cleanup.
func (service ReceiptService) MergedPullRequestTarget(ctx context.Context, pullRequests []GitHubPullRequest, head, recordedBase string) (string, bool) {
	target := ""
	for _, candidate := range pullRequests {
		if candidate.Head.SHA != head || candidate.MergedAt == nil || candidate.Base.Ref == recordedBase {
			continue
		}
		if !service.Ports.ValidBranch(ctx, candidate.Base.Ref) {
			continue
		}
		if target != "" && target != candidate.Base.Ref {
			return "", false
		}
		target = candidate.Base.Ref
	}
	return target, target != ""
}

// rebaseMergedPullRequestIntegrated recognizes the one case in which a
// branch's exact source head is correctly absent from the target history: a
// GitHub rebase merge. The immutable PR receipt must bind that exact source
// head to an exact merge-result commit. That result must be in the freshly
// fetched target and have precisely the same tree as the source; matching a
// PR number, title, or a merely similar patch is deliberately insufficient.
func (service ReceiptService) RebaseMergedPullRequestIntegrated(ctx context.Context, repository, head, target string, pullRequest *PullRequest) (bool, error) {
	if pullRequest == nil || pullRequest.HeadSHA != head || !worktreeproof.IsGitObjectID(pullRequest.MergeSHA) {
		return false, nil
	}
	mergeInTarget, err := service.Ports.IsAncestor(ctx, repository, pullRequest.MergeSHA, target)
	if err != nil || !mergeInTarget {
		return mergeInTarget, err
	}
	sourceTree, err := service.CommitTree(ctx, repository, head)
	if err != nil {
		return false, err
	}
	mergeTree, err := service.CommitTree(ctx, repository, pullRequest.MergeSHA)
	if err != nil {
		return false, err
	}
	return sourceTree == mergeTree, nil
}

// AbsorbedReceipt is the landing evidence for a branch whose exact head can
// never reach the target because a differently named integration branch
// carried its content there. A merger batching several completed candidates
// onto one integration branch and landing that branch once is the workflow a
// repository requiring linear history forces; the source branch tips are then
// absent from the target by construction, not by omission.
// absorbedLandingReceipt establishes, with evidence only, that a branch's
// content reached the exact fetched origin target inside another branch.
//
// Two receipt sources are accepted, never a bare assertion. GitHub's own
// commit-to-pull-request index is preferred: it is computed by GitHub, not
// written by the author, and it already binds this immutable source commit to
// the pull request that introduced it. An operator pointer (--absorbed-by)
// covers the landings GitHub cannot associate, such as content cherry-picked
// rather than merged into the integration branch, and is held to a stricter
// bar precisely because a human chose it.
//
// Every path proves containment locally and cryptographically: merging the
// branch into the landing commit must add nothing to it, and merging it into
// the freshly fetched target must add nothing there either. The second proof
// is what refuses a branch whose work landed and was later reverted.
//
// A discovered receipt that does not hold is an ordinary negative answer. An
// explicitly supplied one that does not hold is returned as a rejection
// string, so the operator reads exactly which verification refused it rather
// than a generic awaiting_push verdict.
func (service ReceiptService) AbsorbedLandingReceipt(
	ctx context.Context,
	worktree, repository, slug, head, base, target, absorbedBy string,
	pullRequests []GitHubPullRequest,
) (*AbsorbedReceipt, string, error) {
	if absorbedBy != "" {
		return service.AttestedAbsorbedReceipt(ctx, worktree, repository, slug, head, base, target, absorbedBy)
	}
	pullRequest := service.AbsorbingPullRequest(pullRequests, base)
	if pullRequest == nil || !worktreeproof.IsGitObjectID(pullRequest.MergeSHA) {
		return nil, "", nil
	}
	landed, err := service.Ports.IsAncestor(ctx, repository, pullRequest.MergeSHA, target)
	if err != nil || !landed {
		return nil, "", err
	}
	absorbed, err := service.ContentAbsorbed(ctx, repository, head, pullRequest.MergeSHA, target)
	if err != nil || !absorbed {
		return nil, "", err
	}
	return &AbsorbedReceipt{LandingSHA: pullRequest.MergeSHA, PullRequest: pullRequest}, "", nil
}

// attestedAbsorbedReceipt verifies an operator-supplied pointer. The pointer
// selects which commit to examine; it grants nothing. Beyond the containment
// proofs every receipt needs, the named commit must be exactly where the work
// entered the target: without that test an operator could name the target tip
// itself and silently reduce the flag to an unreceipted content assertion.
func (service ReceiptService) AttestedAbsorbedReceipt(
	ctx context.Context,
	worktree, repository, slug, head, base, target, absorbedBy string,
) (*AbsorbedReceipt, string, error) {
	landingSHA, pullRequest, rejection, err := service.ResolveAbsorbedBy(ctx, worktree, repository, slug, base, absorbedBy)
	if err != nil || rejection != "" {
		return nil, rejection, err
	}
	if pullRequest != nil {
		receiptAfterContentProof := func(landing string) (*AbsorbedReceipt, string, error) {
			absorbed, err := service.ContentAbsorbed(ctx, repository, head, landing, target)
			if err != nil {
				return nil, "", err
			}
			if !absorbed {
				return nil, fmt.Sprintf(
					"work absorbed by %s no longer survives in the exact fetched origin/%s target %s",
					landing, base, target,
				), nil
			}
			return &AbsorbedReceipt{LandingSHA: landing, PullRequest: pullRequest}, "", nil
		}
		// A numbered PR has a stronger, topology-aware proof than generic
		// patch containment: the exact source head is in the fetched PR
		// head, and the reported merge is in the fresh target. Two landing
		// shapes are recognized. A squash (or rebase) landing creates a new
		// commit disconnected from the source branch's own history, so tree
		// equality between the PR head and the merge commit is the only
		// available proof there. A genuine "Create a merge commit" landing
		// instead keeps the exact source head reachable through the merge
		// commit's own parent chain — Git ancestry, not tree equality, is the
		// correct proof there, and unlike tree equality it is not defeated by
		// the target having advanced past the source's last sync with it
		// before the merge, which is the common case in an actively landing
		// repository.
		squashRejection, err := service.VerifyAttestedSquashPullRequest(ctx, repository, head, target, absorbedBy, pullRequest)
		if err != nil {
			return nil, "", err
		}
		if squashRejection == "" {
			return receiptAfterContentProof(landingSHA)
		}
		mergeCommitRejection, err := service.VerifyAttestedMergeCommitPullRequest(ctx, repository, head, target, absorbedBy, pullRequest)
		if err != nil {
			return nil, "", err
		}
		if mergeCommitRejection == "" {
			return receiptAfterContentProof(pullRequest.MergeSHA)
		}
		return nil, squashRejection + "; " + mergeCommitRejection, nil
	}
	landed, err := service.Ports.IsAncestor(ctx, repository, landingSHA, target)
	if err != nil {
		return nil, "", err
	}
	if !landed {
		return nil, fmt.Sprintf(
			"--absorbed-by %s resolved to %s, which is not contained in the exact fetched origin/%s target %s",
			absorbedBy, landingSHA, base, target,
		), nil
	}
	inLanding, err := service.ContentContained(ctx, repository, head, landingSHA)
	if err != nil {
		return nil, "", err
	}
	if !inLanding {
		return nil, fmt.Sprintf(
			"--absorbed-by %s resolved to %s, which does not contain this branch's content",
			absorbedBy, landingSHA,
		), nil
	}
	inTarget, err := service.ContentContained(ctx, repository, head, target)
	if err != nil {
		return nil, "", err
	}
	if !inTarget {
		return nil, fmt.Sprintf(
			"work absorbed by %s no longer survives in the exact fetched origin/%s target %s",
			landingSHA, base, target,
		), nil
	}
	parent, err := service.CommitFirstParent(ctx, repository, landingSHA)
	if err != nil {
		return nil, "", err
	}
	if parent != "" {
		beforeLanding, err := service.ContentContained(ctx, repository, head, parent)
		if err != nil {
			return nil, "", err
		}
		if beforeLanding {
			return nil, fmt.Sprintf(
				"--absorbed-by %s resolved to %s, which is not where this work entered the target: %s already contained it",
				absorbedBy, landingSHA, parent,
			), nil
		}
	}
	return &AbsorbedReceipt{LandingSHA: landingSHA, PullRequest: pullRequest}, "", nil
}

// verifyAttestedSquashPullRequest proves the physical relationship that a
// squash landing hides from ordinary ancestry. GitHub supplies the immutable
// pull-request head and merge commit; Git supplies the exact source, the
// freshly fetched target, and both trees. No commit message, title, or branch
// name can stand in for any part of this proof.
func (service ReceiptService) VerifyAttestedSquashPullRequest(
	ctx context.Context,
	repository, sourceHead, target, absorbedBy string,
	pullRequest *PullRequest,
) (string, error) {
	if pullRequest == nil || pullRequest.Merged == nil || pullRequest.Number <= 0 ||
		strings.TrimSpace(pullRequest.Base) == "" || !worktreeproof.IsGitObjectID(pullRequest.HeadSHA) || !worktreeproof.IsGitObjectID(pullRequest.MergeSHA) {
		return fmt.Sprintf("--absorbed-by %s has incomplete merged pull request metadata", absorbedBy), nil
	}
	if _, err := service.FetchExactRemotePullRequestHead(ctx, repository, pullRequest.Number, pullRequest.HeadSHA); err != nil {
		var mismatch *PullRequestHeadMismatchError
		if errors.As(err, &mismatch) {
			return mismatch.Error(), nil
		}
		return "", fmt.Errorf("fetch pull request %d head %s for --absorbed-by %s: %w", pullRequest.Number, pullRequest.HeadSHA, absorbedBy, err)
	}
	sourceInPullRequest, err := service.Ports.IsAncestor(ctx, repository, sourceHead, pullRequest.HeadSHA)
	if err != nil {
		return "", err
	}
	if !sourceInPullRequest {
		return fmt.Sprintf("--absorbed-by %s pull request head %s does not contain exact source head %s", absorbedBy, pullRequest.HeadSHA, sourceHead), nil
	}
	mergeInTarget, err := service.Ports.IsAncestor(ctx, repository, pullRequest.MergeSHA, target)
	if err != nil {
		return "", err
	}
	if !mergeInTarget {
		return fmt.Sprintf("--absorbed-by %s merge commit %s is not contained in the exact fetched origin/%s target %s", absorbedBy, pullRequest.MergeSHA, pullRequest.Base, target), nil
	}
	pullRequestTree, err := service.CommitTree(ctx, repository, pullRequest.HeadSHA)
	if err != nil {
		return "", err
	}
	mergeTree, err := service.CommitTree(ctx, repository, pullRequest.MergeSHA)
	if err != nil {
		return "", err
	}
	if pullRequestTree != mergeTree {
		return fmt.Sprintf("--absorbed-by %s pull request head tree %s does not equal merge tree %s", absorbedBy, pullRequestTree, mergeTree), nil
	}
	return "", nil
}

// verifyAttestedMergeCommitPullRequest proves a genuine (non-squash,
// non-rebase) "Create a merge commit" landing, tried after the squash shape
// above finds a tree mismatch. Unlike a squash commit, a real merge commit's
// tree can legitimately differ from its own PR head's tree — it only needs
// to record whatever else the target carried at merge time — so tree
// equality is the wrong test here and would reject a landing that Git's own
// object graph already proves. The one fact that is both necessary and
// sufficient is that the pull request really has a recorded merge commit and
// that the exact source head — not merely the PR's reported head — is
// reachable from it via `git merge-base --is-ancestor` into the freshly
// fetched target. This is the same ordinary containment cleanup itself
// already trusts without any receipt; it is re-run here only because
// abort's own --absorbed-by safety gate requires the stronger,
// explicitly-attested AbsorbedAtOrigin before it will rely on that ancestry.
func (service ReceiptService) VerifyAttestedMergeCommitPullRequest(
	ctx context.Context,
	repository, sourceHead, target, absorbedBy string,
	pullRequest *PullRequest,
) (string, error) {
	if pullRequest == nil || pullRequest.Merged == nil || pullRequest.Number <= 0 ||
		strings.TrimSpace(pullRequest.Base) == "" || !worktreeproof.IsGitObjectID(pullRequest.MergeSHA) {
		return fmt.Sprintf("--absorbed-by %s has incomplete merged pull request metadata", absorbedBy), nil
	}
	mergeInTarget, err := service.Ports.IsAncestor(ctx, repository, pullRequest.MergeSHA, target)
	if err != nil {
		return "", err
	}
	if !mergeInTarget {
		return fmt.Sprintf("--absorbed-by %s merge commit %s is not contained in the exact fetched origin/%s target %s", absorbedBy, pullRequest.MergeSHA, pullRequest.Base, target), nil
	}
	sourceInTarget, err := service.Ports.IsAncestor(ctx, repository, sourceHead, target)
	if err != nil {
		return "", err
	}
	if !sourceInTarget {
		return fmt.Sprintf(
			"--absorbed-by %s names a merge commit, but exact source head %s is not contained in the exact fetched origin/%s target %s (a squash or rebase landing needs the squash-tree proof instead)",
			absorbedBy, sourceHead, pullRequest.Base, target,
		), nil
	}
	return "", nil
}

// fetchExactRemotePullRequestHead obtains GitHub's stable numbered pull-head
// ref without creating a local ref or touching FETCH_HEAD. An API-reported SHA
// alone is not proof that the configured origin exposes the named pull request;
// conversely, fetching an arbitrary object SHA relies on server configuration
// and can accidentally accept an unrelated reachable object.
type PullRequestHeadMismatchError struct{ Message string }

func (err *PullRequestHeadMismatchError) Error() string { return err.Message }

func (service ReceiptService) FetchExactRemotePullRequestHead(ctx context.Context, repository string, number int, expectedSHA string) (string, error) {
	return service.FetchExactRemotePullRequestHeadWithRun(ctx, repository, number, expectedSHA, func(runCtx context.Context, args ...string) (string, error) {
		return service.Ports.Git(runCtx, repository, args...)
	})
}

func (service ReceiptService) FetchExactRemotePullRequestHeadWithRun(
	ctx context.Context,
	repository string,
	number int,
	expectedSHA string,
	run func(context.Context, ...string) (string, error),
) (string, error) {
	if number <= 0 {
		return "", fmt.Errorf("invalid pull request number %d", number)
	}
	if !worktreeproof.IsGitObjectID(expectedSHA) {
		return "", fmt.Errorf("invalid expected pull request head %q", expectedSHA)
	}
	ref := "refs/pull/" + strconv.Itoa(number) + "/head"
	remote, err := run(ctx, "ls-remote", "--exit-code", "origin", ref)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(remote)
	if len(fields) != 2 || fields[1] != ref || !worktreeproof.IsGitObjectID(fields[0]) {
		return "", &PullRequestHeadMismatchError{Message: fmt.Sprintf("origin returned malformed %s response %q", ref, remote)}
	}
	if fields[0] != expectedSHA {
		return "", &PullRequestHeadMismatchError{Message: fmt.Sprintf("origin advertises %s as %s, expected exact API head %s", ref, fields[0], expectedSHA)}
	}
	if _, err := run(ctx, "fetch", "--no-tags", "--no-write-fetch-head", "--", "origin", ref); err != nil {
		return "", err
	}
	fetched, err := run(ctx, "rev-parse", "--verify", "--end-of-options", expectedSHA+"^{commit}")
	if err != nil {
		return "", err
	}
	if fetched != expectedSHA {
		return "", &PullRequestHeadMismatchError{Message: fmt.Sprintf("fetched %s resolved to %s, expected exact API head %s", ref, fetched, expectedSHA)}
	}
	return fetched, nil
}

// absorbedByPullRequestURLPattern matches a GitHub pull-request URL, web
// (".../pull/<N>") or API (".../pulls/<N>") shape, with an optional
// trailing path/query/fragment (e.g. "/files", "?diff=split"). The captured
// repository slug is checked against the command's own --repository so a
// URL naming a different repository is refused rather than silently
// resolved against the wrong one.
var absorbedByPullRequestURLPattern = regexp.MustCompile(`(?i)^https?://(?:www\.)?github\.com/([^/\s]+/[^/\s]+)/pulls?/(\d+)(?:[/?#].*)?$`)

// resolveAbsorbedBy turns an operator pointer into one exact landing commit.
// A pull-request number, "#"-prefixed number, or full GitHub pull-request
// URL must name a pull request that really merged into this exact base;
// anything else must resolve to a commit already present in the canonical
// object database, which a genuine landing always is because the target was
// just fetched. All three pointer shapes are accepted consistently for both
// squash and merge-commit landings (S63): a URL used to fail with "does not
// resolve to a commit" because only a bare/"#"-prefixed number and a
// commit-ish were ever tried.
func (service ReceiptService) ResolveAbsorbedBy(
	ctx context.Context,
	worktree, repository, slug, base, absorbedBy string,
) (string, *PullRequest, string, error) {
	trimmed := strings.TrimSpace(absorbedBy)
	if trimmed == "" {
		return "", nil, "--absorbed-by requires a pull request number, pull request URL, or landing commit", nil
	}
	if match := absorbedByPullRequestURLPattern.FindStringSubmatch(trimmed); match != nil {
		if !strings.EqualFold(match[1], slug) {
			return "", nil, fmt.Sprintf("--absorbed-by URL %s names repository %q, not the requested %q", trimmed, match[1], slug), nil
		}
		number, err := strconv.Atoi(match[2])
		if err != nil || number <= 0 {
			return "", nil, fmt.Sprintf("--absorbed-by URL %s has an invalid pull request number", trimmed), nil
		}
		return service.ResolveAbsorbedByPullRequest(ctx, worktree, slug, base, number)
	}
	pointer := strings.TrimPrefix(trimmed, "#")
	if number, err := strconv.Atoi(pointer); err == nil {
		if number <= 0 {
			return "", nil, fmt.Sprintf("--absorbed-by pull request number %d is not positive", number), nil
		}
		return service.ResolveAbsorbedByPullRequest(ctx, worktree, slug, base, number)
	}
	landingSHA, err := service.Ports.Git(ctx, repository, "rev-parse", "--verify", "--end-of-options", pointer+"^{commit}")
	if err != nil {
		return "", nil, fmt.Sprintf("--absorbed-by %s does not resolve to a commit in %s", absorbedBy, repository), nil
	}
	if !worktreeproof.IsGitObjectID(landingSHA) {
		return "", nil, fmt.Sprintf("--absorbed-by %s resolved to invalid commit %q", absorbedBy, landingSHA), nil
	}
	return landingSHA, nil, "", nil
}

func (service ReceiptService) ResolveAbsorbedByPullRequest(
	ctx context.Context,
	worktree, slug, base string,
	number int,
) (string, *PullRequest, string, error) {
	return service.ResolveAbsorbedByPullRequestWithGet(ctx, worktree, slug, base, number, service.Ports.GitHubGet)
}

func (service ReceiptService) ResolveAbsorbedByPullRequestWithGet(
	ctx context.Context,
	worktree, slug, base string,
	number int,
	get func(context.Context, GitHubGetRequest) ([]byte, error),
) (string, *PullRequest, string, error) {
	response, err := get(ctx, GitHubGetRequest{
		Dir:        worktree,
		Repository: slug,
		Target:     base,
		Endpoint:   "repos/" + slug + "/pulls/" + strconv.Itoa(number),
	})
	if err != nil {
		return "", nil, "", fmt.Errorf("read %s pull request %d: %w", slug, number, err)
	}
	var candidate GitHubPullRequest
	if err := json.Unmarshal(response, &candidate); err != nil {
		return "", nil, "", fmt.Errorf("decode %s pull request %d: %w", slug, number, err)
	}
	if candidate.MergedAt == nil {
		return "", nil, fmt.Sprintf("--absorbed-by pull request %s#%d is not merged", slug, number), nil
	}
	if !strings.EqualFold(candidate.State, "closed") {
		return "", nil, fmt.Sprintf("--absorbed-by pull request %s#%d is not closed", slug, number), nil
	}
	if candidate.Base.Ref != base {
		return "", nil, fmt.Sprintf(
			"--absorbed-by pull request %s#%d merged into %q, not the requested base %q",
			slug, number, candidate.Base.Ref, base,
		), nil
	}
	if !worktreeproof.IsGitObjectID(candidate.MergeCommitSHA) {
		return "", nil, fmt.Sprintf(
			"--absorbed-by pull request %s#%d has invalid merge commit %q",
			slug, number, candidate.MergeCommitSHA,
		), nil
	}
	if !worktreeproof.IsGitObjectID(candidate.Head.SHA) {
		return "", nil, fmt.Sprintf(
			"--absorbed-by pull request %s#%d has invalid head commit %q",
			slug, number, candidate.Head.SHA,
		), nil
	}
	return candidate.MergeCommitSHA, service.MergedPullRequestReceipt(slug, candidate), "", nil
}

func (service ReceiptService) MergedPullRequestReceipt(repository string, candidate GitHubPullRequest) *PullRequest {
	return &PullRequest{
		Number: candidate.Number, URL: candidate.URL, Repository: repository, State: "MERGED",
		Base: candidate.Base.Ref, BaseSHA: candidate.Base.SHA, HeadSHA: candidate.Head.SHA,
		MergeSHA: candidate.MergeCommitSHA, Merged: candidate.MergedAt,
	}
}

// absorbingPullRequest selects the newest merged pull request into the exact
// base that GitHub associates with the immutable source commit. Unlike
// matchingPullRequests it deliberately does not require the pull-request head
// to equal that commit: when a merger batches candidates onto one integration
// branch, the branch name is evidence of nothing and the commit association is
// the receipt. An open pull request is never a landing receipt.
func (service ReceiptService) AbsorbingPullRequest(pullRequests []GitHubPullRequest, base string) *PullRequest {
	var absorbing *PullRequest
	for _, candidate := range pullRequests {
		if candidate.MergedAt == nil || candidate.Base.Ref != base {
			continue
		}
		if absorbing != nil && !candidate.MergedAt.After(*absorbing.Merged) {
			continue
		}
		absorbing = &PullRequest{
			Number: candidate.Number, URL: candidate.URL, State: "MERGED",
			Base: candidate.Base.Ref, BaseSHA: candidate.Base.SHA, HeadSHA: candidate.Head.SHA,
			MergeSHA: candidate.MergeCommitSHA, Merged: candidate.MergedAt,
		}
	}
	return absorbing
}

// contentAbsorbed requires both containment proofs a landing receipt needs:
// the work is wholly inside the commit that carried it, and it is still wholly
// inside the target that was just fetched. Proving only the first would clean
// up a branch whose landing was later reverted.
func (service ReceiptService) ContentAbsorbed(ctx context.Context, repository, head, landingSHA, target string) (bool, error) {
	inLanding, err := service.ContentContained(ctx, repository, head, landingSHA)
	if err != nil || !inLanding {
		return false, err
	}
	return service.ContentContained(ctx, repository, head, target)
}

// contentContained proves that a branch head adds nothing to a commit. The
// three-way merge of the branch into that commit must both succeed and produce
// exactly that commit's own tree; a conflict, or any residual delta, means part
// of the branch is missing from it. A branch containing a revert of work the
// commit still carries therefore fails, because merging it would remove that
// work.
func (service ReceiptService) ContentContained(ctx context.Context, repository, head, commit string) (bool, error) {
	merged, clean, err := service.MergeResultTree(ctx, repository, commit, head)
	if err != nil || !clean {
		return false, err
	}
	existing, err := service.CommitTree(ctx, repository, commit)
	if err != nil {
		return false, err
	}
	return merged == existing, nil
}

// mergeResultTree performs a real three-way merge and reports the resulting
// tree without touching any ref, index, or working tree; only unreferenced
// objects are written. A conflicted merge is a normal negative containment
// answer, not an error.
func (service ReceiptService) MergeResultTree(ctx context.Context, repository, ours, theirs string) (string, bool, error) {
	stdout, stderr, exitCode, err := service.Ports.MergeTreeRun(ctx, repository, ours, theirs)
	if err != nil {
		if exitCode == 1 && ctx.Err() == nil {
			return "", false, nil
		}
		return "", false, fmt.Errorf("merge %s into %s in %s: %w: %s", theirs, ours, repository, err, strings.TrimSpace(stderr))
	}
	tree, _, _ := strings.Cut(strings.TrimSpace(stdout), "\n")
	tree = strings.TrimSpace(tree)
	if !worktreeproof.IsGitObjectID(tree) {
		return "", false, fmt.Errorf("merge %s into %s in %s produced invalid tree %q", theirs, ours, repository, tree)
	}
	return tree, true, nil
}

func (service ReceiptService) CommitFirstParent(ctx context.Context, repository, revision string) (string, error) {
	return worktreeproof.CommitFirstParent(ctx, repository, revision, service.Ports.Git)
}
func (service ReceiptService) CommitTree(ctx context.Context, repository, revision string) (string, error) {
	return worktreeproof.CommitTree(ctx, repository, revision, service.Ports.Git)
}
