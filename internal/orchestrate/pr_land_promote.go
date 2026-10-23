package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

// Landing a promotion pull request (spec/features/integration-branch-batches,
// REQ promote-landing): `wb pr land` recognises the promotion by its body
// marker and lands it only under a fresh, independent review of its exact
// head, after re-proving where every merge on its first-parent history came
// from, never updating the branch server-side and never arming auto-merge.

const (
	// LandRefusalBatchDeltaUnreviewed: no independent Reviewed-Head names the
	// promotion's current head.
	LandRefusalBatchDeltaUnreviewed = "batch-delta-unreviewed"
	// LandRefusalBatchDeltaSelfReview: the promotion review's identity is
	// incomplete, the operator's, or one that made a lane merge.
	LandRefusalBatchDeltaSelfReview = "batch-delta-self-review"
	// LandRefusalBatchBlockInvalid: a pull request carries lane merges but
	// not the promotion marker, or its history is too long to verify.
	LandRefusalBatchBlockInvalid = "batch-block-invalid"
)

// maxListedPullRequestCommits is how many commits GitHub lists for one pull
// request.
const maxListedPullRequestCommits = 250

// IssueComment is one pull request or issue comment.
type IssueComment struct {
	Body        string
	PullRequest int
}

// promoteLandingPorts are the GitHub reads promotion landing makes.
type promoteLandingPorts struct {
	comment  func(ctx context.Context, repository string, id int64) (IssueComment, error)
	review   commitCommentReader
	contains func(ctx context.Context, repository, ancestor, descendant string) (bool, string)
	operator func() ReviewerIdentity
	// mergeMethod is the repository's preferred enabled method: a merge
	// commit when allowed, so the integration branch stays an ancestor of
	// the target and needs no refresh.
	mergeMethod func(ctx context.Context, repository string) (string, error)
}

func defaultPromoteLandingPorts() promoteLandingPorts {
	return promoteLandingPorts{comment: readIssueComment, review: DefaultIntegrationGitHub(), contains: githubchecks.ContainsTarget,
		operator: currentSessionIdentity, mergeMethod: repositoryPullRequestMergeMethod}
}

func (options PullRequestLandOptions) resolvePromotePorts() promoteLandingPorts {
	if options.promotePorts != nil {
		return *options.promotePorts
	}
	return defaultPromoteLandingPorts()
}

// readIssueComment reads one comment and the pull request or issue it is on.
func readIssueComment(ctx context.Context, repository string, id int64) (IssueComment, error) {
	raw, err := integrationGH(ctx, "api", "repos/"+repository+"/issues/comments/"+strconv.FormatInt(id, 10))
	return decodeIssueComment(raw, err, repository, id)
}

func decodeIssueComment(raw []byte, err error, repository string, id int64) (IssueComment, error) {
	var comment struct {
		Body     string `json:"body"`
		IssueURL string `json:"issue_url"`
	}
	if err == nil {
		err = json.Unmarshal(raw, &comment)
	}
	if err != nil {
		return IssueComment{}, fmt.Errorf("read review comment %d: %w", id, err)
	}
	// issue_url is https://api.github.com/repos/<owner>/<repo>/issues/<n>.
	prefix := "/repos/" + strings.ToLower(repository) + "/issues/"
	number := 0
	if index := strings.LastIndex(strings.ToLower(comment.IssueURL), prefix); index >= 0 {
		number, _ = strconv.Atoi(comment.IssueURL[index+len(prefix):])
	}
	return IssueComment{Body: comment.Body, PullRequest: number}, nil
}

// PromotionReceipt is what landing a promotion verified.
type PromotionReceipt struct {
	Lanes    []LaneMerge `json:"lanes"`
	Reviewer string      `json:"reviewer"`
}

func promoteRefusal(code, reason, command string) *landRefusal {
	return &landRefusal{code: code, reason: reason, command: command}
}

func carriesLaneTrailer(commit SourceCommit) bool {
	for _, match := range trailerLinePattern.FindAllStringSubmatch(commit.Body, -1) {
		if match[1] == trailerLane {
			return true
		}
	}
	return false
}

// promotionLandingGate returns the receipt when view is a promotion that may
// land, a refusal when it may not, and nil, nil, nil when view is not a
// promotion. It narrows options so the reviewed head is what lands.
func promotionLandingGate(ctx context.Context, options *PullRequestLandOptions, view githubchecks.PullRequestView, number string, commits []SourceCommit) (*PromotionReceipt, *landRefusal, error) {
	promote := "wb integration promote " + options.Repository + " " + shellSingleQuote(view.Head.Ref) + " --to " + shellSingleQuote(view.Base.Ref)
	ports := options.resolvePromotePorts()
	if !strings.Contains(view.Body, promoteMarker) {
		for _, commit := range commits {
			if carriesLaneTrailer(commit) {
				return nil, promoteRefusal(LandRefusalBatchBlockInvalid,
					fmt.Sprintf("commit %s is a WB lane merge but the body lacks the promotion marker", shortMergeRevision(commit.SHA)), promote), nil
			}
		}
		// GitHub lists at most maxListedPullRequestCommits; a longer branch
		// could hide a lane merge from the check above.
		if len(commits) >= maxListedPullRequestCommits {
			if _, refusal, err := promotionWalk(ctx, ports, options.Repository, view, commits); refusal != nil || err != nil {
				return nil, refusal, err
			}
		}
		return nil, nil, nil
	}
	options.NoUpdateBranch, options.NoAutoMerge = true, true
	// Prefer a merge commit, so the integration branch stays an ancestor of
	// the target; a queue or an explicit --merge-method decides otherwise.
	if !options.MergeMethodExplicit && options.mergeQueue == nil {
		method, err := ports.mergeMethod(ctx, options.Repository)
		if err != nil {
			return nil, nil, err
		}
		options.MergeMethod = method
	}
	delta := promotionDeltaCommand(options.Repository, view)
	reviewer, refusal := promotionReview(ctx, *options, ports, view, number, delta)
	if refusal != nil {
		return nil, refusal, nil
	}
	walk, refusal, err := promotionWalk(ctx, ports, options.Repository, view, commits)
	if refusal != nil || err != nil {
		return nil, refusal, err
	}
	inSet := map[string]bool{}
	for _, commit := range commits {
		inSet[strings.ToLower(commit.SHA)] = true
	}
	// A second parent outside the pull request's own commits must still be
	// proved to be target history, not merely assumed.
	inTarget := func(sha string) (bool, error) {
		if inSet[sha] {
			return false, nil
		}
		contained, reason := ports.contains(ctx, options.Repository, sha, view.Base.Ref)
		if reason != "" {
			return false, fmt.Errorf("check merge parent %s: %s", shortMergeRevision(sha), reason)
		}
		return contained, nil
	}
	verdict, err := verifyProvenance(ctx, ports.review, options.Repository, walk, inTarget)
	if err != nil {
		return nil, nil, err
	}
	if len(verdict.Unrecorded) > 0 {
		return nil, promoteRefusal(BatchRefusalUnrecordedMerge,
			fmt.Sprintf("merge %s brings in commits no still-valid review covers", shortMergeRevision(verdict.Unrecorded[0])), delta), nil
	}
	if verdict.mergedBy(reviewer.Session) {
		return nil, promoteRefusal(LandRefusalBatchDeltaSelfReview, fmt.Sprintf("reviewer session %s made a lane merge in this promotion", reviewer.Session), delta), nil
	}
	return &PromotionReceipt{Lanes: verdict.Lanes, Reviewer: reviewer.String()}, nil, nil
}

// promotionApprovalForm refuses, before any review comment is posted, a
// promotion whose --approved-by is not a comment URL on it.
func promotionApprovalForm(options PullRequestLandOptions, view githubchecks.PullRequestView, number string) *landRefusal {
	if !strings.Contains(view.Body, promoteMarker) {
		return nil
	}
	match := issueCommentURLPattern.FindStringSubmatch(strings.TrimSpace(options.ApprovedBy))
	if match != nil && strings.EqualFold(match[1], options.Repository) && match[2] == number {
		return nil
	}
	return promoteRefusal(LandRefusalBatchDeltaUnreviewed,
		"a promotion lands only with --approved-by naming an existing review comment on this pull request whose Reviewed-Head is its head",
		promotionDeltaCommand(options.Repository, view))
}

func promotionDeltaCommand(repository string, view githubchecks.PullRequestView) string {
	return fmt.Sprintf("wb pr batch delta %s --target %s --to %s --overlap", repository, shellSingleQuote(view.Head.Ref), shellSingleQuote(view.Base.Ref))
}

// promotionReview reads the --approved-by comment on this pull request and
// requires it to name the current head from an independent identity.
func promotionReview(ctx context.Context, options PullRequestLandOptions, ports promoteLandingPorts, view githubchecks.PullRequestView, number, delta string) (ReviewerIdentity, *landRefusal) {
	unreviewed := func(reason string) (ReviewerIdentity, *landRefusal) {
		return ReviewerIdentity{}, promoteRefusal(LandRefusalBatchDeltaUnreviewed, reason, delta)
	}
	match := issueCommentURLPattern.FindStringSubmatch(strings.TrimSpace(options.ApprovedBy))
	if match == nil || !strings.EqualFold(match[1], options.Repository) || match[2] != number {
		return unreviewed("a promotion lands only with --approved-by naming a review comment on this pull request whose Reviewed-Head is its head")
	}
	id, _ := strconv.ParseInt(match[3], 10, 64)
	comment, err := ports.comment(ctx, options.Repository, id)
	if err != nil {
		return unreviewed(err.Error())
	}
	if strconv.Itoa(comment.PullRequest) != number {
		return unreviewed(fmt.Sprintf("review comment %d is on #%d, not this pull request", id, comment.PullRequest))
	}
	if reviewed, err := parseReviewedHeadLine(comment.Body); err != nil || !strings.EqualFold(reviewed, view.Head.SHA) {
		return unreviewed(fmt.Sprintf("the review does not name the current head %s", shortMergeRevision(view.Head.SHA)))
	}
	reviewer := declaredReviewer(comment.Body)
	if !identityComplete(reviewer) || strings.EqualFold(reviewer.Session, ports.operator().Session) {
		return ReviewerIdentity{}, promoteRefusal(LandRefusalBatchDeltaSelfReview, fmt.Sprintf("review identity %q is incomplete or yours", reviewer.String()), delta)
	}
	return reviewer, nil
}

// promotionWalk is the first-parent walk of the pull request's own commits
// from its head, proved complete: the commit it ends on is target history.
func promotionWalk(ctx context.Context, ports promoteLandingPorts, repository string, view githubchecks.PullRequestView, commits []SourceCommit) ([]integrationCommit, *landRefusal, error) {
	bySHA := map[string]SourceCommit{}
	for _, commit := range commits {
		bySHA[strings.ToLower(commit.SHA)] = commit
	}
	var walk []integrationCommit
	sha := strings.ToLower(view.Head.SHA)
	for {
		commit, ok := bySHA[sha]
		if !ok || len(commit.Parents) == 0 {
			break
		}
		message := commit.Subject
		if commit.Body != "" {
			message += "\n\n" + commit.Body
		}
		walk = append(walk, integrationCommit{SHA: sha, Parents: commit.Parents, Message: message})
		sha = strings.ToLower(commit.Parents[0])
	}
	// GitHub lists at most 250 commits of a pull request; a walk that stops
	// short of target history would skip merges it never saw.
	contained, reason := ports.contains(ctx, repository, sha, view.Base.Ref)
	if reason != "" {
		return nil, nil, fmt.Errorf("check the promotion history: %s", reason)
	}
	if !contained {
		return nil, promoteRefusal(LandRefusalBatchBlockInvalid,
			fmt.Sprintf("the first-parent history ends at %s, which is not %s history; promote fewer commits at a time", shortMergeRevision(sha), view.Base.Ref), ""), nil
	}
	return walk, nil, nil
}
