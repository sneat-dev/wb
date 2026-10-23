package orchestrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

const (
	batchBase   = "1111111111111111111111111111111111111111"
	batchMerge1 = "2222222222222222222222222222222222222222"
	batchHead   = "3333333333333333333333333333333333333333"
	batchPin    = "4444444444444444444444444444444444444444"
	batchTip    = "5555555555555555555555555555555555555555"
	batchFix    = "6666666666666666666666666666666666666666"
)

func pinReview(head string) string {
	return "**Review by opus@claude-code@pin-reviewer**\nReviewed-Head: " + head + "\n"
}

func batchReviewBody(head string) string {
	return "**Review by opus@codex@delta-reviewer**\nReviewed-Head: " + head + "\n"
}

// batchWorld is a first-parent history base <- merge1(pin) <- head(fix).
type batchWorld struct {
	comments  map[int64]string
	parents   map[string][]string
	ancestors map[string]bool // sha -> is on target history
	tip       string
	tipReason string
	reason    string
	pulls     map[int]githubchecks.PullRequestView
	pullErr   error
	parentErr error
	remote    *fakeSourceRemote
}

func newBatchWorld() *batchWorld {
	return &batchWorld{
		comments: map[int64]string{1: pinReview(batchPin), 2: batchReviewBody(batchHead)},
		parents: map[string][]string{
			batchHead:   {batchMerge1},
			batchMerge1: {batchBase, batchPin},
		},
		ancestors: map[string]bool{batchBase: true, batchTip: true},
		tip:       batchTip,
		pulls:     map[int]githubchecks.PullRequestView{},
		remote:    &fakeSourceRemote{},
	}
}

func (world *batchWorld) ports() *batchLandingPorts {
	return &batchLandingPorts{
		comment: func(_ context.Context, _ string, id int64) (string, error) {
			body, ok := world.comments[id]
			if !ok {
				return "", errors.New("comment gone")
			}
			return body, nil
		},
		parents: func(_ context.Context, _, sha string) ([]string, error) {
			return world.parents[sha], world.parentErr
		},
		contains: func(_ context.Context, _, ancestor, descendant string) (bool, string) {
			if world.reason != "" {
				return false, world.reason
			}
			if descendant == batchHead && ancestor == batchTip {
				return world.ancestors["head-contains-tip"], ""
			}
			return world.ancestors[ancestor], ""
		},
		targetHead: func(context.Context, string, string) (string, string) { return world.tip, world.tipReason },
		pullRequest: func(_ context.Context, _ string, number int) (githubchecks.PullRequestView, error) {
			return world.pulls[number], world.pullErr
		},
		source: world.remote,
	}
}

type fakeSourceRemote struct {
	commented, closed []int
	has               bool
	hasErr, err       error
	closeErr          error
}

func (remote *fakeSourceRemote) associated(context.Context, string, string) ([]githubchecks.PullRequestView, error) {
	return nil, nil
}
func (remote *fakeSourceRemote) hasComment(context.Context, string, int, string) (bool, error) {
	return remote.has, remote.hasErr
}
func (remote *fakeSourceRemote) comment(_ context.Context, _ string, number int, _ string) error {
	remote.commented = append(remote.commented, number)
	return remote.err
}
func (remote *fakeSourceRemote) close(_ context.Context, _ string, number int) error {
	remote.closed = append(remote.closed, number)
	return remote.closeErr
}

func batchState() BatchState {
	return BatchState{Version: 1, Repository: "acme/app", Target: "public-demo", Branch: "public-demo-batch-1", Base: batchBase,
		MergeOptions: batchMergeOptions, BuiltBy: []string{"opus@claude-code@builder"},
		Steps: []BatchStep{{Kind: BatchStepSource, PullRequest: 510, Head: batchPin, ReviewCommentID: 1, ReviewDigest: ReviewDigest(pinReview(batchPin)), State: BatchStateMerged}}}
}

func landingBatchView(state BatchState) githubchecks.PullRequestView {
	view := githubchecks.PullRequestView{Number: 7, State: "open", Body: RenderBatchBody(state), HTMLURL: "https://github.com/acme/app/pull/7"}
	view.Head.SHA, view.Head.Ref, view.Base.Ref = batchHead, state.Branch, state.Target
	return view
}

func gateOptions(world *batchWorld) PullRequestLandOptions {
	return PullRequestLandOptions{Repository: "acme/app", PullRequest: "7", MergeMethod: "merge",
		ApprovedBy: "https://github.com/acme/app/pull/7#issuecomment-2", batchPorts: world.ports()}
}

func runGate(t *testing.T, options PullRequestLandOptions, view githubchecks.PullRequestView) (*BatchState, *landRefusal, error, PullRequestLandOptions) {
	t.Helper()
	state, refusal, err := batchLandingGate(context.Background(), &options, view, "7")
	return state, refusal, err, options
}

func TestBatchGateLandsAReviewedBatchAsAMergeCommitWithoutUpdateBranch(t *testing.T) {
	t.Parallel()
	world := newBatchWorld()
	world.ancestors["head-contains-tip"] = true
	state, refusal, err, narrowed := runGate(t, gateOptions(world), landingBatchView(batchState()))
	if err != nil || refusal != nil || state == nil || state.Branch != "public-demo-batch-1" {
		t.Fatalf("state=%v refusal=%+v err=%v", state, refusal, err)
	}
	if narrowed.MergeMethod != "merge" || !narrowed.NoUpdateBranch {
		t.Fatalf("options not narrowed: method=%s noUpdate=%v", narrowed.MergeMethod, narrowed.NoUpdateBranch)
	}
	plain := landingBatchView(batchState())
	plain.Body = "an ordinary pull request"
	if state, refusal, err, _ := runGate(t, gateOptions(world), plain); state != nil || refusal != nil || err != nil {
		t.Fatal("an ordinary pull request went through the batch gate")
	}
}

func TestBatchGateRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*batchWorld, *PullRequestLandOptions, *githubchecks.PullRequestView)
		code   string
	}{
		{"block for another branch", func(_ *batchWorld, _ *PullRequestLandOptions, v *githubchecks.PullRequestView) { v.Head.Ref = "other" }, LandRefusalBatchBlockInvalid},
		{"unreadable block", func(_ *batchWorld, _ *PullRequestLandOptions, v *githubchecks.PullRequestView) {
			v.Body = batchBlockBegin + "\n```yaml\n: [\n```\n" + batchBlockEnd
		}, LandRefusalBatchBlockInvalid},
		{"merge queue", func(_ *batchWorld, o *PullRequestLandOptions, _ *githubchecks.PullRequestView) {
			o.mergeQueue = &githubchecks.MergeQueuePolicy{}
		}, BatchRefusalTargetMergeQueue},
		{"squash requested", func(_ *batchWorld, o *PullRequestLandOptions, _ *githubchecks.PullRequestView) {
			o.MergeMethod, o.MergeMethodExplicit = "squash", true
		}, LandRefusalBatchMergeMethod},
		{"kept commits", func(_ *batchWorld, o *PullRequestLandOptions, _ *githubchecks.PullRequestView) {
			o.KeepCommits = []string{"abc"}
		}, LandRefusalBatchMergeMethod},
		{"no review", func(_ *batchWorld, o *PullRequestLandOptions, _ *githubchecks.PullRequestView) {
			o.ApprovedBy = "review.md"
		}, LandRefusalBatchDeltaUnreviewed},
		{"review on another pull request", func(_ *batchWorld, o *PullRequestLandOptions, _ *githubchecks.PullRequestView) {
			o.ApprovedBy = "https://github.com/acme/app/pull/8#issuecomment-2"
		}, LandRefusalBatchDeltaUnreviewed},
		{"review comment gone", func(w *batchWorld, _ *PullRequestLandOptions, _ *githubchecks.PullRequestView) { delete(w.comments, 2) }, LandRefusalBatchDeltaUnreviewed},
		{"review without a head", func(w *batchWorld, _ *PullRequestLandOptions, _ *githubchecks.PullRequestView) {
			w.comments[2] = "LGTM"
		}, LandRefusalBatchDeltaUnreviewed},
		{"review of an older head", func(w *batchWorld, _ *PullRequestLandOptions, _ *githubchecks.PullRequestView) {
			w.comments[2] = batchReviewBody(batchMerge1)
		}, LandRefusalBatchDeltaUnreviewed},
		{"anonymous review", func(w *batchWorld, _ *PullRequestLandOptions, _ *githubchecks.PullRequestView) {
			w.comments[2] = "Reviewed-Head: " + batchHead + "\n"
		}, LandRefusalBatchDeltaSelfReview},
		{"builder reviewed it", func(w *batchWorld, _ *PullRequestLandOptions, _ *githubchecks.PullRequestView) {
			w.comments[2] = "**Review by sonnet@claude-code@builder**\nReviewed-Head: " + batchHead + "\n"
		}, LandRefusalBatchDeltaSelfReview},
		{"behind the target", func(w *batchWorld, _ *PullRequestLandOptions, _ *githubchecks.PullRequestView) {
			w.ancestors["head-contains-tip"] = false
		}, LandRefusalBatchBehindTarget},
		{"base off the target", func(w *batchWorld, _ *PullRequestLandOptions, _ *githubchecks.PullRequestView) {
			w.ancestors[batchBase] = false
		}, BatchRefusalBaseInvalid},
		{"base off the first-parent path", func(w *batchWorld, _ *PullRequestLandOptions, _ *githubchecks.PullRequestView) {
			w.parents[batchMerge1] = nil
		}, BatchRefusalBaseInvalid},
		{"hand-pushed merge", func(w *batchWorld, _ *PullRequestLandOptions, _ *githubchecks.PullRequestView) {
			w.parents[batchHead] = []string{batchMerge1, batchFix}
		}, BatchRefusalUnrecordedMerge},
		{"pin review edited", func(w *batchWorld, _ *PullRequestLandOptions, _ *githubchecks.PullRequestView) {
			w.comments[1] += "edited\n"
		}, BatchRefusalUnrecordedMerge},
		{"pin review deleted", func(w *batchWorld, _ *PullRequestLandOptions, _ *githubchecks.PullRequestView) { delete(w.comments, 1) }, BatchRefusalUnrecordedMerge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			world := newBatchWorld()
			world.ancestors["head-contains-tip"] = true
			options := gateOptions(world)
			view := landingBatchView(batchState())
			tc.mutate(world, &options, &view)
			options.batchPorts = world.ports()
			_, refusal, err, _ := runGate(t, options, view)
			if err != nil || refusal == nil || refusal.code != tc.code {
				t.Fatalf("refusal = %+v err = %v, want %s", refusal, err, tc.code)
			}
		})
	}
}

func TestBatchGateAcceptsATargetRefreshMergeAndBoundsTheWalk(t *testing.T) {
	t.Parallel()
	world := newBatchWorld()
	world.ancestors["head-contains-tip"] = true
	world.ancestors[batchFix] = true // a refresh brings in target history
	world.parents[batchHead] = []string{batchMerge1, batchFix}
	if _, refusal, err, _ := runGate(t, gateOptions(world), landingBatchView(batchState())); refusal != nil || err != nil {
		t.Fatalf("refresh merge refused: %+v %v", refusal, err)
	}
	loop := newBatchWorld()
	loop.ancestors["head-contains-tip"] = true
	loop.parents[batchHead] = []string{batchHead}
	if _, refusal, _, _ := runGate(t, gateOptions(loop), landingBatchView(batchState())); refusal == nil || !strings.Contains(refusal.reason, "first-parent commits") {
		t.Fatalf("unbounded walk: %+v", refusal)
	}
}

func TestBatchGateReportsGitHubReadFailures(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*batchWorld){
		"tip":      func(w *batchWorld) { w.tipReason = "404" },
		"contains": func(w *batchWorld) { w.reason = "502" },
		"parents":  func(w *batchWorld) { w.parentErr = errors.New("down") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			world := newBatchWorld()
			world.ancestors["head-contains-tip"] = true
			mutate(world)
			if _, _, err, _ := runGate(t, gateOptions(world), landingBatchView(batchState())); err == nil {
				t.Fatal("a GitHub read failure was swallowed")
			}
		})
	}
	// A failing ancestry read on a merge parent and on the base.
	refresh := newBatchWorld()
	refresh.ancestors["head-contains-tip"] = true
	refresh.parents[batchHead] = []string{batchMerge1, batchFix}
	ports := refresh.ports()
	calls := 0
	contains := ports.contains
	ports.contains = func(ctx context.Context, repository, ancestor, descendant string) (bool, string) {
		calls++
		if calls >= 2 {
			return false, "502"
		}
		return contains(ctx, repository, ancestor, descendant)
	}
	options := gateOptions(refresh)
	options.batchPorts = ports
	if _, _, err, _ := runGate(t, options, landingBatchView(batchState())); err == nil {
		t.Fatal("a failing base ancestry read was swallowed")
	}
	calls = 0
	ports.contains = func(ctx context.Context, repository, ancestor, descendant string) (bool, string) {
		calls++
		if calls >= 3 {
			return false, "502"
		}
		return contains(ctx, repository, ancestor, descendant)
	}
	if _, _, err, _ := runGate(t, options, landingBatchView(batchState())); err == nil {
		t.Fatal("a failing merge-parent ancestry read was swallowed")
	}
}

func TestReconcileBatchSourcesRecordsEachSourceAndNeverFails(t *testing.T) {
	t.Parallel()
	world := newBatchWorld()
	state := batchState()
	state.Steps = []BatchStep{
		{Kind: BatchStepSource, PullRequest: 501, Head: "a"},
		{Kind: BatchStepSource, PullRequest: 502, Head: "b"},
		{Kind: BatchStepSource, PullRequest: 503, Head: "c"},
		{Kind: BatchStepRefresh, Head: "t"},
		{Kind: BatchStepSource, PullRequest: 504, Head: "d"},
	}
	open := func(number int, head string) githubchecks.PullRequestView {
		view := githubchecks.PullRequestView{Number: number, State: "open"}
		view.Head.SHA = head
		return view
	}
	world.pulls[501] = githubchecks.PullRequestView{Number: 501, Merged: true}
	world.pulls[502] = open(502, "b")
	world.pulls[503] = open(503, "c2")
	world.pulls[504] = githubchecks.PullRequestView{Number: 504, State: "closed"}
	outcomes := reconcileBatchSources(context.Background(), *world.ports(), "acme/app", state, "#7")
	var got []string
	for _, outcome := range outcomes {
		got = append(got, outcome.State)
	}
	if strings.Join(got, ",") != "merged,closed,open,closed" {
		t.Fatalf("outcomes = %v", outcomes)
	}
	if len(world.remote.commented) != 2 || len(world.remote.closed) != 1 || world.remote.closed[0] != 502 {
		t.Fatalf("commented %v closed %v", world.remote.commented, world.remote.closed)
	}

	failing := newBatchWorld()
	failing.pullErr = errors.New("down")
	if outcomes := reconcileBatchSources(context.Background(), *failing.ports(), "acme/app", batchState(), "#7"); outcomes[0].State != "unknown" {
		t.Fatalf("read failure outcome = %+v", outcomes)
	}
	for name, remote := range map[string]*fakeSourceRemote{
		"comment lookup": {hasErr: errors.New("x")},
		"comment":        {err: errors.New("x")},
		"close":          {has: true, closeErr: errors.New("x")},
	} {
		world := newBatchWorld()
		world.remote = remote
		world.pulls[510] = open(510, batchPin)
		if outcomes := reconcileBatchSources(context.Background(), *world.ports(), "acme/app", batchState(), "#7"); outcomes[0].State != "open" || outcomes[0].Detail == "" {
			t.Errorf("%s failure outcome = %+v", name, outcomes)
		}
	}
	if defaultBatchLandingPorts().comment == nil || (PullRequestLandOptions{}).resolveBatchPorts().parents == nil {
		t.Fatal("default batch landing ports are incomplete")
	}
}
