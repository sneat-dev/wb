package orchestrate

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

// Landing a batch pull request (spec/features/integration-branch-batches,
// REQ batch-landing): `wb pr land` recognises the batch by its body block and
// lands it only as a merge commit, never updated server-side, under an
// independent review of its current head, after re-proving where every merge
// on its first-parent history came from.

const (
	// LandRefusalBatchDeltaUnreviewed: no independent Reviewed-Head names the
	// batch PR's current head.
	LandRefusalBatchDeltaUnreviewed = "batch-delta-unreviewed"
	// LandRefusalBatchDeltaSelfReview: the batch review's identity is
	// incomplete, the operator's, or one of the batch builders'.
	LandRefusalBatchDeltaSelfReview = "batch-delta-self-review"
	// LandRefusalBatchBehindTarget: the batch does not contain the target
	// tip; WB refreshes it with a recorded merge, never update-branch.
	LandRefusalBatchBehindTarget = "batch-behind-target"
	// LandRefusalBatchMergeMethod: a batch lands only as a merge commit.
	LandRefusalBatchMergeMethod = "batch-merge-method"
	// LandRefusalBatchBlockInvalid: the body block is unreadable or names a
	// different branch or target than the pull request.
	LandRefusalBatchBlockInvalid = "batch-block-invalid"

	// maxBatchFirstParentWalk bounds the first-parent walk from the batch
	// head back to its recorded base.
	maxBatchFirstParentWalk = 500
)

// batchLandingPorts are the GitHub reads and writes batch landing makes.
type batchLandingPorts struct {
	comment     func(ctx context.Context, repository string, id int64) (BatchComment, error)
	parents     func(ctx context.Context, repository, sha string) ([]string, error)
	contains    func(ctx context.Context, repository, ancestor, descendant string) (bool, string)
	targetHead  func(ctx context.Context, repository, target string) (string, string)
	pullRequest func(ctx context.Context, repository string, number int) (githubchecks.PullRequestView, error)
	source      sourcePullRequestRemote
}

func defaultBatchLandingPorts() batchLandingPorts {
	github := DefaultBatchGitHub()
	return batchLandingPorts{
		comment: github.IssueComment, parents: pullRequestCommitParents,
		contains: githubchecks.ContainsTarget, targetHead: githubchecks.TargetHead,
		pullRequest: github.PullRequest, source: githubSourcePullRequestRemote{},
	}
}

func (options PullRequestLandOptions) resolveBatchPorts() batchLandingPorts {
	if options.batchPorts != nil {
		return *options.batchPorts
	}
	return defaultBatchLandingPorts()
}

func batchRefusal(code, reason, command string) *landRefusal {
	return &landRefusal{code: code, reason: reason, command: command}
}

// batchLandingGate returns the batch state when view is a batch pull request
// that may land, a refusal when it may not, and nil, nil, nil when view is not
// a batch. It narrows options to a merge commit without update-branch.
func batchLandingGate(ctx context.Context, options *PullRequestLandOptions, view githubchecks.PullRequestView, number string) (*BatchState, *landRefusal, error) {
	state, ok, err := ParseBatchBody(view.Body)
	land := "wb pr land " + options.Repository + "#" + number
	if !ok {
		return nil, nil, nil
	}
	if err != nil || state.Branch != view.Head.Ref || state.Target != view.Base.Ref || !strings.EqualFold(state.Repository, options.Repository) {
		reason := fmt.Sprintf("the batch block names %s %s into %s, not this pull request's %s into %s", state.Repository, state.Branch, state.Target, view.Head.Ref, view.Base.Ref)
		if err != nil {
			reason = err.Error()
		}
		return nil, batchRefusal(LandRefusalBatchBlockInvalid, reason, ""), nil
	}
	delta := fmt.Sprintf("wb pr batch delta %s --target %s", options.Repository, state.Target)
	switch {
	case options.mergeQueue != nil:
		return nil, batchRefusal(BatchRefusalTargetMergeQueue, view.Base.Ref+" merges through a merge queue, which rebuilds the reviewed heads", ""), nil
	case (options.MergeMethodExplicit && options.MergeMethod != "merge") || len(options.KeepCommits) > 0:
		return nil, batchRefusal(LandRefusalBatchMergeMethod, "a batch lands only as a merge commit, which keeps every reviewed head", land), nil
	}
	options.MergeMethod, options.NoUpdateBranch = "merge", true

	ports := options.resolveBatchPorts()
	reviewed, reviewer, refusal := batchReview(ctx, *options, ports, number, delta)
	if refusal != nil {
		return nil, refusal, nil
	}
	if reviewed != view.Head.SHA {
		return nil, batchRefusal(LandRefusalBatchDeltaUnreviewed,
			fmt.Sprintf("the batch review names %s, not the batch head %s", shortMergeRevision(reviewed), shortMergeRevision(view.Head.SHA)), delta), nil
	}
	if !identityComplete(reviewer) || strings.EqualFold(reviewer.Session, currentSessionIdentity().Session) || builtByBatch(state, reviewer) {
		return nil, batchRefusal(LandRefusalBatchDeltaSelfReview,
			fmt.Sprintf("batch review identity %q is incomplete, yours, or a batch builder's", reviewer.String()), delta), nil
	}
	tip, reason := ports.targetHead(ctx, options.Repository, state.Target)
	if reason != "" {
		return nil, nil, fmt.Errorf("read %s tip: %s", state.Target, reason)
	}
	if current, reason := ports.contains(ctx, options.Repository, tip, view.Head.SHA); reason != "" {
		return nil, nil, fmt.Errorf("check the batch contains %s: %s", state.Target, reason)
	} else if !current {
		return nil, batchRefusal(LandRefusalBatchBehindTarget, "the batch does not contain "+state.Target+" "+shortMergeRevision(tip)+"; refresh it with a recorded merge",
			fmt.Sprintf("wb pr batch add %s --target %s", options.Repository, state.Target)), nil
	}
	if refusal, err := batchProvenance(ctx, ports, options.Repository, state, view.Head.SHA, tip); refusal != nil || err != nil {
		return nil, refusal, err
	}
	return &state, nil, nil
}

// batchReview reads the --approved-by comment on this pull request.
func batchReview(ctx context.Context, options PullRequestLandOptions, ports batchLandingPorts, number, delta string) (string, ReviewerIdentity, *landRefusal) {
	match := issueCommentURLPattern.FindStringSubmatch(strings.TrimSpace(options.ApprovedBy))
	if match == nil || !strings.EqualFold(match[1], options.Repository) || match[2] != number {
		return "", ReviewerIdentity{}, batchRefusal(LandRefusalBatchDeltaUnreviewed,
			"a batch lands only with --approved-by naming a review comment on this pull request whose Reviewed-Head is the batch head", delta)
	}
	id, _ := strconv.ParseInt(match[3], 10, 64)
	comment, err := ports.comment(ctx, options.Repository, id)
	if err != nil {
		return "", ReviewerIdentity{}, batchRefusal(LandRefusalBatchDeltaUnreviewed, err.Error(), delta)
	}
	if strconv.Itoa(comment.PullRequest) != number {
		return "", ReviewerIdentity{}, batchRefusal(LandRefusalBatchDeltaUnreviewed, fmt.Sprintf("review comment %d is on #%d, not this pull request", id, comment.PullRequest), delta)
	}
	body := comment.Body
	reviewed, err := parseReviewedHeadLine(body)
	if err != nil || reviewed == "" {
		return "", ReviewerIdentity{}, batchRefusal(LandRefusalBatchDeltaUnreviewed, "the batch review has no full Reviewed-Head line", delta)
	}
	return reviewed, declaredReviewer(body), nil
}

// batchProvenance applies REQ batch-merge-provenance through GitHub alone:
// the recorded base is on the target, and every first-parent merge from the
// head back to it brings in a recorded pin whose review still resolves, or
// target history.
func batchProvenance(ctx context.Context, ports batchLandingPorts, repository string, state BatchState, head, tip string) (*landRefusal, error) {
	if onTarget, reason := ports.contains(ctx, repository, state.Base, tip); reason != "" {
		return nil, fmt.Errorf("check the batch base: %s", reason)
	} else if !onTarget {
		return batchRefusal(BatchRefusalBaseInvalid, "recorded base "+shortMergeRevision(state.Base)+" is not on "+state.Target, ""), nil
	}
	sha := head
	for walked := 0; sha != state.Base; walked++ {
		if walked == maxBatchFirstParentWalk {
			return batchRefusal(BatchRefusalBaseInvalid, fmt.Sprintf("recorded base %s is not within %d first-parent commits of the batch head", shortMergeRevision(state.Base), maxBatchFirstParentWalk), ""), nil
		}
		parents, err := ports.parents(ctx, repository, sha)
		if err != nil {
			return nil, err
		}
		if len(parents) == 0 {
			return batchRefusal(BatchRefusalBaseInvalid, "recorded base "+shortMergeRevision(state.Base)+" is not on the batch's first-parent history", ""), nil
		}
		if len(parents) > 1 {
			recorded, err := recordedBatchParent(ctx, ports, repository, state, parents[1], tip)
			if err != nil {
				return nil, err
			}
			if !recorded {
				return batchRefusal(BatchRefusalUnrecordedMerge, "merge "+shortMergeRevision(sha)+" brings in "+shortMergeRevision(parents[1])+", which is neither a recorded, still-reviewed pin nor target history",
					fmt.Sprintf("wb pr batch delta %s --target %s", repository, state.Target)), nil
			}
		}
		sha = parents[0]
	}
	return nil, nil
}

func recordedBatchParent(ctx context.Context, ports batchLandingPorts, repository string, state BatchState, parent, tip string) (bool, error) {
	for _, step := range state.sources() {
		if step.Head != parent {
			continue
		}
		comment, err := ports.comment(ctx, repository, step.ReviewCommentID)
		if err != nil || comment.PullRequest != step.PullRequest {
			return false, nil
		}
		reviewer := declaredReviewer(comment.Body)
		if !identityComplete(reviewer) || builtByBatch(state, reviewer) {
			return false, nil
		}
		reviewed, parseErr := parseReviewedHeadLine(comment.Body)
		return ReviewDigest(comment.Body) == step.ReviewDigest && parseErr == nil && reviewed == parent, nil
	}
	refresh, reason := ports.contains(ctx, repository, parent, tip)
	if reason != "" {
		return false, fmt.Errorf("check merge parent %s: %s", shortMergeRevision(parent), reason)
	}
	return refresh, nil
}

// BatchSourceOutcome records what landing a batch did to one source PR.
type BatchSourceOutcome struct {
	PullRequest int    `json:"pr"`
	Head        string `json:"head"`
	State       string `json:"state"`
	Detail      string `json:"detail,omitempty"`
}

const batchSourceCommentMarker = "<!-- wb:batch-source-pr -->"

// reconcileBatchSources records each source PR after the batch landed:
// merged by GitHub, closed with a comment when its pinned head landed, or
// left open with a comment when its head advanced after the pin. It never
// fails the landing; a failure is recorded on the source.
func reconcileBatchSources(ctx context.Context, ports batchLandingPorts, repository string, state BatchState, batch string) []BatchSourceOutcome {
	outcomes := make([]BatchSourceOutcome, 0, len(state.sources()))
	for _, step := range state.sources() {
		outcome := BatchSourceOutcome{PullRequest: step.PullRequest, Head: step.Head}
		view, err := ports.pullRequest(ctx, repository, step.PullRequest)
		switch {
		case err != nil:
			outcome.State, outcome.Detail = "unknown", err.Error()
		case view.Merged:
			outcome.State = "merged"
		case !strings.EqualFold(view.State, "open"):
			outcome.State = "closed"
		default:
			outcome.State, outcome.Detail = notifyBatchSource(ctx, ports.source, repository, step, view, batch)
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}

func notifyBatchSource(ctx context.Context, remote sourcePullRequestRemote, repository string, step BatchStep, view githubchecks.PullRequestView, batch string) (string, string) {
	advanced := !strings.EqualFold(view.Head.SHA, step.Head)
	body := batchSourceCommentMarker + "\nWB landed pinned head `" + step.Head + "` through batch " + batch + "."
	if advanced {
		body += " This pull request's head has since moved to `" + view.Head.SHA + "`, so it stays open for the newer commits."
	}
	if commented, err := remote.hasComment(ctx, repository, view.Number, batchSourceCommentMarker); err != nil {
		return "open", err.Error()
	} else if !commented {
		if err := remote.comment(ctx, repository, view.Number, body); err != nil {
			return "open", err.Error()
		}
	}
	if advanced {
		return "open", "head advanced after the pin"
	}
	if err := remote.close(ctx, repository, view.Number); err != nil {
		return "open", err.Error()
	}
	return "closed", "pinned head landed in the batch"
}
