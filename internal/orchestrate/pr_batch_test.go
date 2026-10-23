package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestParseBatchPinAcceptsTheDocumentedShapeOnly(t *testing.T) {
	t.Parallel()
	pin, err := ParseBatchPin("acme/app", "#510@3B32619C=https://github.com/acme/app/pull/510#issuecomment-42")
	if err != nil || pin.Number != 510 || pin.SHA != "3b32619c" || pin.ReviewCommentID != 42 {
		t.Fatalf("pin = %+v err = %v", pin, err)
	}
	for name, raw := range map[string]string{
		"no review":        "510@3b32619c",
		"not a comment":    "510@3b32619c=https://github.com/acme/app/pull/510",
		"other repository": "510@3b32619c=https://github.com/acme/other/pull/510#issuecomment-42",
		"other pr":         "510@3b32619c=https://github.com/acme/app/pull/511#issuecomment-42",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseBatchPin("acme/app", raw); err == nil {
				t.Fatalf("%q was accepted", raw)
			}
		})
	}
}

func TestBatchBodyRoundTripsItsStateAndRejectsBadBlocks(t *testing.T) {
	t.Parallel()
	state := BatchState{Version: 1, Repository: "acme/app", Target: "public-demo", Branch: "public-demo-batch-1", Base: strings.Repeat("a", 40),
		MergeOptions: batchMergeOptions, BuiltBy: []string{"opus@claude-code@s"},
		Steps: []BatchStep{{Kind: BatchStepSource, PullRequest: 510, Head: strings.Repeat("b", 40), Reviewer: "r", State: BatchStateMerged, MergeCommit: strings.Repeat("c", 40)},
			{Kind: BatchStepRefresh, Head: strings.Repeat("d", 40), State: BatchStatePending}}}
	body := RenderBatchBody(state)
	if !strings.Contains(body, "| 1 | #510 |") || !strings.Contains(body, "refresh `public-demo`") || !strings.Contains(body, "| - |") {
		t.Fatalf("body = %s", body)
	}
	parsed, ok, err := ParseBatchBody(body)
	if err != nil || !ok || len(parsed.Steps) != 2 || parsed.Steps[0].Head != state.Steps[0].Head {
		t.Fatalf("parsed = %+v ok=%v err=%v", parsed, ok, err)
	}
	if _, ok, err := ParseBatchBody("no block"); ok || err != nil {
		t.Fatalf("plain body: ok=%v err=%v", ok, err)
	}
	if _, ok, err := ParseBatchBody(batchBlockBegin + "\n```yaml\nwb_batch: 2\n```\n" + batchBlockEnd); !ok || err == nil {
		t.Fatalf("future version: ok=%v err=%v", ok, err)
	}
	if _, ok, err := ParseBatchBody(batchBlockBegin + "\n```yaml\n: [\n```\n" + batchBlockEnd); !ok || err == nil {
		t.Fatalf("bad yaml: ok=%v err=%v", ok, err)
	}
	badBase := state
	badBase.Base = "--upload-pack=evil"
	if _, ok, err := ParseBatchBody(RenderBatchBody(badBase)); !ok || err == nil {
		t.Fatalf("non-SHA base accepted: ok=%v err=%v", ok, err)
	}
	badStep := state
	badStep.Steps = []BatchStep{{Kind: BatchStepSource, Head: strings.Repeat("b", 40), MergeCommit: "HEAD~1"}}
	if _, ok, err := ParseBatchBody(RenderBatchBody(badStep)); !ok || err == nil {
		t.Fatalf("non-SHA merge commit accepted: ok=%v err=%v", ok, err)
	}
	state.addBuilder("opus@claude-code@s")
	state.addBuilder("other")
	if strings.Join(state.BuiltBy, ",") != "opus@claude-code@s,other" {
		t.Fatalf("built_by = %v", state.BuiltBy)
	}
}

func TestBatchResultExitCodesAndJSON(t *testing.T) {
	t.Parallel()
	for outcome, want := range map[LandOutcome]int{LandSuccess: 0, LandFindings: 1, LandRefused: 2} {
		if got := (BatchResult{Outcome: outcome}).ExitCode(); got != want {
			t.Errorf("%s exit = %d, want %d", outcome, got, want)
		}
	}
	raw, err := MarshalBatchResult(BatchResult{Verb: "pr batch delta", Outcome: LandSuccess})
	if err != nil || !strings.Contains(string(raw), `"verb": "pr batch delta"`) {
		t.Fatalf("json = %s err = %v", raw, err)
	}
}

func TestDeclaredReviewerReadsTheReviewHeader(t *testing.T) {
	t.Parallel()
	if got := declaredReviewer("**Review by opus@claude-code@abc** (head `x`)\n"); got.String() != "opus@claude-code@abc" || !identityComplete(got) {
		t.Fatalf("reviewer = %+v", got)
	}
	if got := declaredReviewer("LGTM"); identityComplete(got) {
		t.Fatalf("headerless review counted as complete: %+v", got)
	}
}

func TestBatchOptionDefaultsResolveProductionPorts(t *testing.T) {
	t.Parallel()
	options := BatchOptions{ProjectsRoot: t.TempDir()}
	if options.resolveRunner() == nil || options.resolveGitHub() == nil || options.resolveCheckout() == nil || options.resolveLaneSession() == nil || options.resolveCache() == nil {
		t.Fatal("a default port is nil")
	}
	if _, found, err := options.resolveCache().Load("acme/app", "public-demo-batch-1"); found || err != nil {
		t.Fatalf("fresh projects root cache: found=%v err=%v", found, err)
	}
	if options.resolveOperator().String() == "" {
		t.Fatal("no operator identity")
	}
	if _, _, _, err := manifestCheckout(t.TempDir()); err == nil {
		t.Fatal("a plain directory read as a WB worktree")
	}
	if session := claimSessionForBranch(t.TempDir(), "acme/app", "lane-1"); session != "" {
		t.Fatalf("session = %q from an empty projects root", session)
	}
	claims := []worktrees.ActiveClaimSummary{{Repository: "acme/other", Branch: "lane-1", WBSessionID: "x"}, {Repository: "ACME/app", Branch: "lane-1", WBSessionID: "wb-1"}}
	if session := claimSessionIn(claims, "acme/app", "lane-1"); session != "wb-1" {
		t.Fatalf("session = %q", session)
	}
}

func TestFileBatchCacheKeepsEveryRevisionImmutable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cache := fileBatchCache{home: func() (string, error) { return root, nil }}
	if _, found, err := cache.Load("acme/app", "public-demo-batch-1"); found || err != nil {
		t.Fatalf("empty cache: found=%v err=%v", found, err)
	}
	options := BatchOptions{cache: cache}
	state := BatchState{Version: 1, Repository: "acme/app", Branch: "public-demo-batch-1"}
	for i := 0; i < 2; i++ {
		if err := options.save(&state); err != nil {
			t.Fatal(err)
		}
	}
	loaded, found, err := cache.Load("acme/app", "public-demo-batch-1")
	if err != nil || !found || loaded.Revision != 2 {
		t.Fatalf("loaded = %+v found=%v err=%v", loaded, found, err)
	}
	stale := BatchState{Version: 1, Repository: "acme/app", Branch: "public-demo-batch-1"}
	if err := options.save(&stale); err != nil || stale.Revision != 3 {
		t.Fatalf("stale state saved as revision %d: %v", stale.Revision, err)
	}
	if err := cache.Save(stale); err == nil {
		t.Fatal("a revision was overwritten")
	}
	dir, _ := cache.dir("acme/app", "public-demo-batch-1")
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "9.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cache.Load("acme/app", "public-demo-batch-1"); err == nil {
		t.Fatal("a corrupt revision was accepted")
	}
	onlyRoot := t.TempDir()
	only := fileBatchCache{home: func() (string, error) { return onlyRoot, nil }}
	onlyDir, _ := only.dir("acme/app", "b")
	if err := os.MkdirAll(onlyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, found, err := only.Load("acme/app", "b"); found || err != nil {
		t.Fatalf("directory without revisions: found=%v err=%v", found, err)
	}
	if err := os.WriteFile(filepath.Join(onlyDir, "1.json"), nil, 0o000); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() != 0 {
		if _, _, err := only.Load("acme/app", "b"); err == nil {
			t.Fatal("an unreadable revision was accepted")
		}
	}
	blockedRoot := t.TempDir()
	blocked := fileBatchCache{home: func() (string, error) { return blockedRoot, nil }}
	blockedDir, _ := blocked.dir("acme/app", "c")
	if err := os.MkdirAll(filepath.Dir(blockedDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blockedDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := blocked.Load("acme/app", "c"); err == nil {
		t.Fatal("a file in place of the cache directory was accepted")
	}
	if err := blocked.Save(BatchState{Repository: "acme/app", Branch: "c"}); err == nil {
		t.Fatal("saved under a file")
	}
	homeless := fileBatchCache{home: func() (string, error) { return "", errors.New("no home") }}
	if _, _, err := homeless.Load("acme/app", "c"); err == nil {
		t.Fatal("a missing WB home was accepted on load")
	}
	if err := homeless.Save(BatchState{}); err == nil {
		t.Fatal("a missing WB home was accepted on save")
	}
}

type failingBatchCache struct{ loadErr, saveErr error }

func (cache failingBatchCache) Load(string, string) (BatchState, bool, error) {
	return BatchState{}, false, cache.loadErr
}
func (cache failingBatchCache) Save(BatchState) error { return cache.saveErr }

func TestBatchSaveReportsCacheFailures(t *testing.T) {
	t.Parallel()
	state := BatchState{}
	if err := (BatchOptions{cache: failingBatchCache{loadErr: errors.New("load")}}).save(&state); err == nil {
		t.Fatal("a cache read failure was swallowed")
	}
	if err := (BatchOptions{cache: failingBatchCache{saveErr: errors.New("save")}}).save(&state); err == nil {
		t.Fatal("a cache write failure was swallowed")
	}
}

func TestGitHubBatchAdapterReadsAndWritesThroughGH(t *testing.T) {
	t.Parallel()
	var calls []string
	responses := map[string]string{
		"repos/acme/app/pulls/7":            `{"number":7,"state":"open","merged":true}`,
		"repos/acme/app/issues/comments/42": `{"body":"LGTM","issue_url":"https://api.github.com/repos/acme/app/issues/510"}`,
		"repos/acme/app":                    `{"allow_merge_commit":true}`,
		"repos/acme/app/pulls?state=all&head=acme:public-demo-batch-1": `[{"number":7}]`,
		"repos/acme/app/pulls":   `{"number":8}`,
		"repos/acme/app/pulls/8": `{}`,
	}
	gh := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		for _, arg := range args {
			if body, ok := responses[arg]; ok {
				return []byte(body), nil
			}
		}
		return nil, errors.New("unexpected call")
	}
	inspector := &waiverInspectorStub{checks: []githubchecks.RemoteCheck{{Name: "ci", Bucket: "pass"}}}
	adapter := githubBatch{gh: gh, inspector: inspector, mergeQueue: func(context.Context, string, string) (*githubchecks.MergeQueuePolicy, error) {
		return &githubchecks.MergeQueuePolicy{}, nil
	}}
	ctx := context.Background()
	if view, err := adapter.PullRequest(ctx, "acme/app", 7); err != nil || !view.Merged {
		t.Fatalf("pull request = %+v %v", view, err)
	}
	if comment, err := adapter.IssueComment(ctx, "acme/app", 42); err != nil || comment.Body != "LGTM" || comment.PullRequest != 510 {
		t.Fatalf("comment = %+v %v", comment, err)
	}
	foreign := githubBatch{gh: func(context.Context, ...string) ([]byte, error) {
		return []byte(`{"body":"x","issue_url":"https://api.github.com/repos/acme/other/issues/510"}`), nil
	}}
	if comment, err := foreign.IssueComment(ctx, "acme/app", 42); err != nil || comment.PullRequest != 0 {
		t.Fatalf("a comment from another repository bound to #%d (%v)", comment.PullRequest, err)
	}
	if allowed, err := adapter.AllowsMergeCommits(ctx, "acme/app"); err != nil || !allowed {
		t.Fatalf("merge commits = %v %v", allowed, err)
	}
	if queued, err := adapter.UsesMergeQueue(ctx, "acme/app", "public-demo"); err != nil || !queued {
		t.Fatalf("queue = %v %v", queued, err)
	}
	if view, err := adapter.BatchPullRequest(ctx, "acme/app", "public-demo-batch-1"); err != nil || view.Number != 7 {
		t.Fatalf("batch pull request = %+v %v", view, err)
	}
	if number, err := adapter.CreatePullRequest(ctx, "acme/app", "public-demo-batch-1", "public-demo", "t", "b"); err != nil || number != 8 {
		t.Fatalf("created = %d %v", number, err)
	}
	if err := adapter.UpdatePullRequestBody(ctx, "acme/app", 8, "b"); err != nil {
		t.Fatal(err)
	}
	if checks, err := adapter.CommitChecks(ctx, "acme/app", strings.Repeat("a", 40)); err != nil || len(checks) != 1 {
		t.Fatalf("checks = %v %v", checks, err)
	}
	if !strings.Contains(strings.Join(calls, "\n"), "--method POST repos/acme/app/pulls -f head=public-demo-batch-1 -f base=public-demo") {
		t.Fatalf("calls = %v", calls)
	}

	empty := githubBatch{gh: func(context.Context, ...string) ([]byte, error) { return []byte(`[]`), nil }}
	if view, err := empty.BatchPullRequest(ctx, "acme/app", "b"); err != nil || view.Number != 0 {
		t.Fatalf("no batch pull request = %+v %v", view, err)
	}
	numberless := githubBatch{gh: func(context.Context, ...string) ([]byte, error) { return []byte(`{}`), nil }}
	if _, err := numberless.CreatePullRequest(ctx, "acme/app", "b", "t", "t", "b"); err == nil || !strings.Contains(err.Error(), "no pull request number") {
		t.Fatalf("numberless create err = %v", err)
	}
	if _, err := empty.CreatePullRequest(ctx, "acme/app", "b", "t", "t", "b"); err == nil {
		t.Fatal("a create without a number was accepted")
	}
	broken := githubBatch{gh: func(context.Context, ...string) ([]byte, error) { return nil, errors.New("down") }, inspector: &waiverInspectorStub{reason: "403"}}
	if _, err := broken.PullRequest(ctx, "acme/app", 7); err == nil {
		t.Fatal("pull request error swallowed")
	}
	if _, err := broken.IssueComment(ctx, "acme/app", 1); err == nil {
		t.Fatal("comment error swallowed")
	}
	if _, err := broken.AllowsMergeCommits(ctx, "acme/app"); err == nil {
		t.Fatal("settings error swallowed")
	}
	if _, err := broken.BatchPullRequest(ctx, "acme/app", "b"); err == nil {
		t.Fatal("list error swallowed")
	}
	if _, err := broken.CreatePullRequest(ctx, "acme/app", "b", "t", "t", "b"); err == nil {
		t.Fatal("create error swallowed")
	}
	if err := broken.UpdatePullRequestBody(ctx, "acme/app", 1, "b"); err == nil {
		t.Fatal("update error swallowed")
	}
	if _, err := broken.CommitChecks(ctx, "acme/app", "a"); err == nil {
		t.Fatal("checks error swallowed")
	}
}

type waiverInspectorStub struct {
	checks []githubchecks.RemoteCheck
	reason string
}

func (stub *waiverInspectorStub) RequiredChecks(context.Context, string, string, bool) ([]githubchecks.RequiredRemoteCheck, string, string) {
	return nil, "", ""
}
func (stub *waiverInspectorStub) TargetHead(context.Context, string, string) (string, string) {
	return "", ""
}
func (stub *waiverInspectorStub) CommitChecks(context.Context, githubchecks.PullRequestWaitOptions) ([]githubchecks.RemoteCheck, bool, string) {
	return stub.checks, false, stub.reason
}

func TestBatchFailedRendersItsClass(t *testing.T) {
	t.Parallel()
	if got := (BatchFailed{Check: "test", Class: BatchFailureSource, Sources: []int{2, 5}}).String(); got != "test: source #2, #5" {
		t.Fatalf("got %q", got)
	}
	if got := (BatchFailed{Check: "lint", Class: BatchFailurePreExisting}).String(); got != "lint: pre-existing" {
		t.Fatalf("got %q", got)
	}
}

func TestExtendsStepsRequiresAMatchingPrefix(t *testing.T) {
	t.Parallel()
	a := BatchStep{Kind: BatchStepSource, Head: "a"}
	b := BatchStep{Kind: BatchStepSource, Head: "b"}
	if !extendsSteps([]BatchStep{a, b}, []BatchStep{a}) || extendsSteps([]BatchStep{a}, []BatchStep{a, b}) || extendsSteps([]BatchStep{b, a}, []BatchStep{a}) {
		t.Fatal("extendsSteps misjudged a prefix")
	}
	merged := BatchStep{Kind: BatchStepSource, Head: "a", MergeCommit: "m1"}
	if extendsSteps([]BatchStep{{Kind: BatchStepSource, Head: "a", MergeCommit: "m2"}}, []BatchStep{merged}) {
		t.Fatal("a cache with a different merge commit was taken as newer")
	}
}

func TestSyncBatchHeadFastForwardsOnlyWhenBehind(t *testing.T) {
	t.Parallel()
	state := BatchState{Branch: "public-demo-batch-1"}
	argv := func(args ...string) func(runnertest.Call) bool {
		return func(call runnertest.Call) bool { return strings.Join(call.Args, " ") == strings.Join(args, " ") }
	}
	ok := func(out string) runner.Result { return runner.Result{Stdout: out} }
	failed := errors.New("exit 1")
	cases := []struct {
		name    string
		script  func(*runnertest.Fake)
		want    string
		wantErr bool
	}{
		{"behind fast-forwards", func(f *runnertest.Fake) {
			f.Expect(argv("rev-parse", "HEAD"), ok("a"), nil).Expect(argv("rev-parse", "origin/public-demo-batch-1"), ok("b"), nil).
				Expect(argv("merge-base", "--is-ancestor", "a", "b"), ok(""), nil).Expect(argv("merge", "--ff-only", "--quiet", "b"), ok(""), nil)
		}, "b", false},
		{"fast-forward fails", func(f *runnertest.Fake) {
			f.Expect(argv("rev-parse", "HEAD"), ok("a"), nil).Expect(argv("rev-parse", "origin/public-demo-batch-1"), ok("b"), nil).
				Expect(argv("merge-base", "--is-ancestor", "a", "b"), ok(""), nil).Expect(argv("merge", "--ff-only", "--quiet", "b"), runner.Result{ExitCode: 128}, failed)
		}, "", true},
		{"ancestry unreadable", func(f *runnertest.Fake) {
			f.Expect(argv("rev-parse", "HEAD"), ok("a"), nil).Expect(argv("rev-parse", "origin/public-demo-batch-1"), ok("b"), nil).
				Expect(argv("merge-base", "--is-ancestor", "a", "b"), runner.Result{ExitCode: 128}, failed)
		}, "", true},
		{"head unreadable", func(f *runnertest.Fake) {
			f.Expect(argv("rev-parse", "HEAD"), runner.Result{ExitCode: 128}, failed)
		}, "", true},
		{"diverged", func(f *runnertest.Fake) {
			f.Expect(argv("rev-parse", "HEAD"), ok("a"), nil).Expect(argv("rev-parse", "origin/public-demo-batch-1"), ok("b"), nil).
				Expect(argv("merge-base", "--is-ancestor", "a", "b"), runner.Result{ExitCode: 1}, failed).
				Expect(argv("merge-base", "--is-ancestor", "b", "a"), runner.Result{ExitCode: 1}, failed)
		}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			tc.script(fake)
			head, err := (BatchOptions{run: fake}).syncBatchHead(context.Background(), batchGit{run: fake}, state)
			if head != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("head = %q err = %v", head, err)
			}
		})
	}
}

func TestAttributeReportsAFailedCheckRead(t *testing.T) {
	t.Parallel()
	refresh, source := strings.Repeat("r", 40), strings.Repeat("s", 40)
	for name, steps := range map[string][]BatchStep{
		"refresh": {{Kind: BatchStepRefresh, Head: refresh}},
		"source":  {{Kind: BatchStepSource, PullRequest: 501, Head: source, State: BatchStateMerged}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			state := BatchState{Base: strings.Repeat("a", 40), Steps: steps}
			checksAt := func(sha string) ([]githubchecks.RemoteCheck, error) {
				if sha == refresh || sha == source {
					return nil, errors.New("rate limited")
				}
				return []githubchecks.RemoteCheck{{Name: "test", Bucket: "pass"}}, nil
			}
			if _, err := attribute("test", state, checksAt); err == nil {
				t.Fatal("a failed check read was swallowed")
			}
		})
	}
}
