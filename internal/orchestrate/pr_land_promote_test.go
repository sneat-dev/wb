package orchestrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

const (
	promoteHead  = "1111111111111111111111111111111111111111"
	promoteLane  = "2222222222222222222222222222222222222222"
	promoteFirst = "3333333333333333333333333333333333333333"
	promoteBase  = "4444444444444444444444444444444444444444"
)

// promoteWorld is a promotion pull request whose head is one recorded lane
// merge on top of target history.
type promoteWorld struct {
	review      string
	reviewPR    int
	commentErr  error
	laneBody    string
	laneCommit  string
	contained   bool
	containsErr string
	operator    string
}

func newPromoteWorld() *promoteWorld {
	return &promoteWorld{
		review:     "**Review by opus@codex@delta-reviewer**\nReviewed-Head: " + promoteHead + "\n",
		reviewPR:   7,
		laneBody:   "**Review by opus@codex@lane-reviewer**\nReviewed-Head: " + promoteLane + "\n",
		laneCommit: promoteLane,
		contained:  true,
		operator:   "operator",
	}
}

func (world *promoteWorld) ports() *promoteLandingPorts {
	return &promoteLandingPorts{
		comment: func(context.Context, string, int64) (IssueComment, error) {
			return IssueComment{Body: world.review, PullRequest: world.reviewPR}, world.commentErr
		},
		review: &fakeCommitComments{comments: map[int64]CommitComment{9: {CommitID: world.laneCommit, Body: world.laneBody}}},
		contains: func(context.Context, string, string, string) (bool, string) {
			return world.contained, world.containsErr
		},
		operator: func() ReviewerIdentity {
			return ReviewerIdentity{Model: "opus", Harness: "claude-code", Session: world.operator}
		},
	}
}

type fakeCommitComments struct{ comments map[int64]CommitComment }

func (fake *fakeCommitComments) CommitComment(_ context.Context, _ string, id int64) (CommitComment, error) {
	comment, ok := fake.comments[id]
	if !ok {
		return CommitComment{}, errors.New("404")
	}
	return comment, nil
}

func laneMergeBody(laneBody, mergedBy string) string {
	return "Wb-Lane: lane-1@" + promoteLane + "\nWb-Review: https://github.com/acme/app/commit/" + promoteLane + "#commitcomment-9\n" +
		"Wb-Review-Digest: " + ReviewDigest(laneBody) + "\nWb-Reviewer: opus@codex@lane-reviewer\nWb-Merged-By: opus@claude-code@" + mergedBy
}

func promotionCommits(world *promoteWorld) []SourceCommit {
	return []SourceCommit{
		{SHA: promoteHead, Subject: "Merge lane-1 into public-demo", Body: laneMergeBody(world.laneBody, "merger"), Parents: []string{promoteFirst, promoteLane}},
		{SHA: promoteLane, Subject: "lane work", Parents: []string{promoteBase}},
		{SHA: promoteFirst, Subject: "fix", Parents: []string{promoteBase}},
	}
}

func promotionView() githubchecks.PullRequestView {
	view := githubchecks.PullRequestView{Number: 7, Body: promoteMarker + "\nlanes"}
	view.Head.SHA, view.Head.Ref, view.Base.Ref = promoteHead, "public-demo", "main"
	return view
}

func runPromotionGate(world *promoteWorld, view githubchecks.PullRequestView, commits []SourceCommit, approvedBy string) (*PromotionReceipt, *landRefusal, PullRequestLandOptions, error) {
	options := PullRequestLandOptions{Repository: "acme/app", ApprovedBy: approvedBy, promotePorts: world.ports()}
	receipt, refusal, err := promotionLandingGate(context.Background(), &options, view, "7", commits)
	return receipt, refusal, options, err
}

const promoteApproval = "https://github.com/acme/app/pull/7#issuecomment-5"

func TestPromotionGateLandsAReviewedPromotionWithoutUpdateBranchOrAutoMerge(t *testing.T) {
	t.Parallel()
	world := newPromoteWorld()
	receipt, refusal, narrowed, err := runPromotionGate(world, promotionView(), promotionCommits(world), promoteApproval)
	if err != nil || refusal != nil || receipt == nil || len(receipt.Lanes) != 1 || receipt.Reviewer != "opus@codex@delta-reviewer" {
		t.Fatalf("receipt = %+v refusal = %+v err = %v", receipt, refusal, err)
	}
	if !narrowed.NoUpdateBranch || !narrowed.NoAutoMerge {
		t.Fatalf("options not narrowed: %+v", narrowed)
	}
	plain := promotionView()
	plain.Body = "an ordinary pull request"
	ordinary := []SourceCommit{{SHA: promoteHead, Subject: "feat", Parents: []string{promoteBase}}}
	if receipt, refusal, narrowed, err := runPromotionGate(world, plain, ordinary, ""); receipt != nil || refusal != nil || err != nil || narrowed.NoAutoMerge {
		t.Fatal("an ordinary pull request went through the promotion gate")
	}
}

func TestPromotionGateRefusals(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		mutate     func(*promoteWorld, *githubchecks.PullRequestView, *[]SourceCommit)
		approvedBy string
		code       string
	}{
		"marker deleted":   {func(_ *promoteWorld, v *githubchecks.PullRequestView, _ *[]SourceCommit) { v.Body = "edited" }, promoteApproval, LandRefusalBatchBlockInvalid},
		"no review":        {func(*promoteWorld, *githubchecks.PullRequestView, *[]SourceCommit) {}, "", LandRefusalBatchDeltaUnreviewed},
		"review elsewhere": {func(*promoteWorld, *githubchecks.PullRequestView, *[]SourceCommit) {}, "https://github.com/acme/app/pull/8#issuecomment-5", LandRefusalBatchDeltaUnreviewed},
		"unreadable review": {func(w *promoteWorld, _ *githubchecks.PullRequestView, _ *[]SourceCommit) {
			w.commentErr = errors.New("404")
		}, promoteApproval, LandRefusalBatchDeltaUnreviewed},
		"comment moved": {func(w *promoteWorld, _ *githubchecks.PullRequestView, _ *[]SourceCommit) { w.reviewPR = 8 }, promoteApproval, LandRefusalBatchDeltaUnreviewed},
		"stale review": {func(w *promoteWorld, _ *githubchecks.PullRequestView, _ *[]SourceCommit) {
			w.review = "**Review by opus@codex@r**\nReviewed-Head: " + promoteLane
		}, promoteApproval, LandRefusalBatchDeltaUnreviewed},
		"anonymous review": {func(w *promoteWorld, _ *githubchecks.PullRequestView, _ *[]SourceCommit) {
			w.review = "Reviewed-Head: " + promoteHead
		}, promoteApproval, LandRefusalBatchDeltaSelfReview},
		"operator reviewed": {func(w *promoteWorld, _ *githubchecks.PullRequestView, _ *[]SourceCommit) {
			w.operator = "delta-reviewer"
		}, promoteApproval, LandRefusalBatchDeltaSelfReview},
		"merger reviewed": {func(w *promoteWorld, _ *githubchecks.PullRequestView, c *[]SourceCommit) {
			(*c)[0].Body = laneMergeBody(w.laneBody, "delta-reviewer")
		}, promoteApproval, LandRefusalBatchDeltaSelfReview},
		"edited lane review": {func(w *promoteWorld, _ *githubchecks.PullRequestView, _ *[]SourceCommit) { w.laneBody += "edited\n" }, promoteApproval, BatchRefusalUnrecordedMerge},
		"history truncated":  {func(w *promoteWorld, _ *githubchecks.PullRequestView, _ *[]SourceCommit) { w.contained = false }, promoteApproval, LandRefusalBatchBlockInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			world, view := newPromoteWorld(), promotionView()
			commits := promotionCommits(world)
			tc.mutate(world, &view, &commits)
			_, refusal, _, err := runPromotionGate(world, view, commits, tc.approvedBy)
			if err != nil || refusal == nil || refusal.code != tc.code {
				t.Fatalf("refusal = %+v err = %v, want %s", refusal, err, tc.code)
			}
		})
	}
}

func TestPromotionGateChecksALongOrdinaryPullRequestForHiddenLaneMerges(t *testing.T) {
	t.Parallel()
	world := newPromoteWorld()
	plain := promotionView()
	plain.Body = "an ordinary pull request"
	long := make([]SourceCommit, maxListedPullRequestCommits)
	for i := range long {
		long[i] = SourceCommit{SHA: strings.Repeat("a", 39) + string(rune('0'+i%10)), Subject: "c"}
	}
	if _, refusal, _, err := runPromotionGate(world, plain, long, ""); refusal != nil || err != nil {
		t.Fatalf("a contained long branch was refused: %+v %v", refusal, err)
	}
	world.contained = false
	if _, refusal, _, _ := runPromotionGate(world, plain, long, ""); refusal == nil || refusal.code != LandRefusalBatchBlockInvalid {
		t.Fatalf("a truncated long branch passed: %+v", refusal)
	}
	world.containsErr = "502"
	if _, _, _, err := runPromotionGate(world, plain, long, ""); err == nil {
		t.Fatal("a failed ancestry read was swallowed")
	}
}

func TestPromotionGateTreatsTargetHistoryAsARefresh(t *testing.T) {
	t.Parallel()
	world := newPromoteWorld()
	commits := promotionCommits(world)
	// A refresh merge of main: its second parent is not a promotion commit.
	refresh := SourceCommit{SHA: strings.Repeat("5", 40), Subject: "Merge main", Parents: []string{promoteHead, promoteBase}}
	view := promotionView()
	view.Head.SHA = refresh.SHA
	world.review = "**Review by opus@codex@delta-reviewer**\nReviewed-Head: " + refresh.SHA + "\n"
	all := append([]SourceCommit{refresh}, commits...)
	receipt, refusal, _, err := runPromotionGate(world, view, all, promoteApproval)
	if err != nil || refusal != nil || len(receipt.Lanes) != 1 {
		t.Fatalf("receipt = %+v refusal = %+v err = %v", receipt, refusal, err)
	}
	// The walk's own end proves out, but the refresh parent's ancestry read
	// fails: the failure surfaces.
	calls := 0
	ports := world.ports()
	ports.contains = func(context.Context, string, string, string) (bool, string) {
		calls++
		if calls > 1 {
			return false, "502"
		}
		return true, ""
	}
	options := PullRequestLandOptions{Repository: "acme/app", ApprovedBy: promoteApproval, promotePorts: ports}
	if _, _, err := promotionLandingGate(context.Background(), &options, view, "7", all); err == nil {
		t.Fatal("a failed refresh ancestry read was swallowed")
	}
}

func TestDecodeIssueCommentBindsItsPullRequest(t *testing.T) {
	t.Parallel()
	comment, err := decodeIssueComment([]byte(`{"body":"ok","issue_url":"https://api.github.com/repos/Acme/App/issues/7"}`), nil, "acme/app", 5)
	if err != nil || comment.PullRequest != 7 || comment.Body != "ok" {
		t.Fatalf("comment = %+v err = %v", comment, err)
	}
	if foreign, _ := decodeIssueComment([]byte(`{"body":"ok","issue_url":"https://api.github.com/repos/acme/other/issues/7"}`), nil, "acme/app", 5); foreign.PullRequest != 0 {
		t.Fatal("a comment on another repository bound this one")
	}
	if _, err := decodeIssueComment(nil, errors.New("404"), "acme/app", 5); err == nil {
		t.Fatal("a failed read was swallowed")
	}
	if ports := (PullRequestLandOptions{}).resolvePromotePorts(); ports.comment == nil || ports.review == nil || ports.contains == nil || ports.operator == nil {
		t.Fatal("a default promotion port is nil")
	}
}
