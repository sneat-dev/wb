//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/runner"
)

// batchFixture is an origin with an integration branch, source branches
// pushed as pull requests, a batch worktree, and an in-memory GitHub.
type batchFixture struct {
	t        *testing.T
	origin   string
	work     string
	other    string
	base     string
	gh       *fakeBatchGitHub
	cache    *memoryBatchCache
	nextPR   int
	nextNote int64
}

func bgit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "user.name=WB Test", "-c", "user.email=wb@example.test", "-c", "commit.gpgsign=false"}, args...)...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeBatchFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newBatchFixture(t *testing.T) *batchFixture {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	bgit(t, root, "init", "--quiet", "--bare", "--initial-branch=public-demo", origin)
	bgit(t, origin, "config", "uploadpack.allowReachableSHA1InWant", "true")
	bgit(t, root, "clone", "--quiet", origin, seed)
	writeBatchFile(t, seed, "a.go", "package a\n\nconst A = 1\n")
	writeBatchFile(t, seed, "b.go", "package a\n\nconst B = 1\n")
	bgit(t, seed, "add", "-A")
	bgit(t, seed, "commit", "--quiet", "-m", "base")
	bgit(t, seed, "push", "--quiet", "origin", "HEAD:public-demo")
	fixture := &batchFixture{
		t: t, origin: origin, gh: newFakeBatchGitHub(), cache: &memoryBatchCache{},
		nextPR: 500, nextNote: 9000,
		work:  filepath.Join(root, "batch"),
		other: filepath.Join(root, "other"),
	}
	fixture.base = bgit(t, seed, "rev-parse", "HEAD")
	bgit(t, root, "clone", "--quiet", "--branch", "public-demo", origin, fixture.work)
	bgit(t, fixture.work, "switch", "--quiet", "-c", "public-demo-batch-1")
	// The verbs run git themselves in the batch worktree; CI has no global identity.
	bgit(t, fixture.work, "config", "user.name", "WB Test")
	bgit(t, fixture.work, "config", "user.email", "wb@example.test")
	bgit(t, fixture.work, "config", "commit.gpgsign", "false")
	return fixture
}

func (fixture *batchFixture) options(pins ...string) BatchOptions {
	return BatchOptions{
		Repository: "acme/app", Target: "public-demo", Worktree: fixture.work, Pins: pins,
		GitHub: fixture.gh, cache: fixture.cache, run: runner.New(),
		checkout: func(string) (string, string, string, error) {
			return "acme/app", "public-demo-batch-1", "public-demo", nil
		},
		laneSession: func(string, string, string) string { return "lane-session" },
		operator: func() ReviewerIdentity {
			return ReviewerIdentity{Model: "opus", Harness: "claude-code", Session: "operator"}
		},
	}
}

// source pushes a branch off base changing file, opens it as a reviewed PR,
// and returns its pin.
func (fixture *batchFixture) source(file, contents string) (string, int) {
	return fixture.sourceFrom(fixture.base, file, contents, "reviewer")
}

func (fixture *batchFixture) sourceFrom(from, file, contents, reviewerSession string) (string, int) {
	t := fixture.t
	fixture.nextPR++
	number := fixture.nextPR
	dir := filepath.Join(t.TempDir(), "src")
	bgit(t, filepath.Dir(dir), "clone", "--quiet", fixture.origin, dir)
	bgit(t, dir, "switch", "--quiet", "-c", "lane-"+strconv.Itoa(number), from)
	writeBatchFile(t, dir, file, contents)
	bgit(t, dir, "add", "-A")
	bgit(t, dir, "commit", "--quiet", "-m", "lane "+strconv.Itoa(number))
	bgit(t, dir, "push", "--quiet", "origin", "HEAD:lane-"+strconv.Itoa(number))
	head := bgit(t, dir, "rev-parse", "HEAD")
	view := githubchecks.PullRequestView{Number: number, State: "open"}
	view.Head.SHA, view.Head.Ref, view.Base.Ref = head, "lane-"+strconv.Itoa(number), "public-demo"
	fixture.gh.prs[number] = view
	fixture.nextNote++
	fixture.gh.comments[fixture.nextNote] = fmt.Sprintf("**Review by opus@claude-code@%s**\n\nLGTM\n\nReviewed-Head: %s\n", reviewerSession, head)
	fixture.gh.commentPR[fixture.nextNote] = number
	return fmt.Sprintf("%d@%s=https://github.com/acme/app/pull/%d#issuecomment-%d", number, head[:12], number, fixture.nextNote), number
}

func (fixture *batchFixture) advanceTarget(file, contents string) string {
	t := fixture.t
	dir := filepath.Join(t.TempDir(), "advance")
	bgit(t, filepath.Dir(dir), "clone", "--quiet", "--branch", "public-demo", fixture.origin, dir)
	writeBatchFile(t, dir, file, contents)
	bgit(t, dir, "add", "-A")
	bgit(t, dir, "commit", "--quiet", "-m", "target advance")
	bgit(t, dir, "push", "--quiet", "origin", "HEAD:public-demo")
	return bgit(t, dir, "rev-parse", "HEAD")
}

type memoryBatchCache struct {
	revisions    []BatchState
	failAt, call int
}

func (cache *memoryBatchCache) fail() bool {
	cache.call++
	return cache.failAt != 0 && cache.call == cache.failAt
}

func (cache *memoryBatchCache) Load(_, branch string) (BatchState, bool, error) {
	if cache.fail() {
		return BatchState{}, false, errors.New("cache read failed")
	}
	for i := len(cache.revisions) - 1; i >= 0; i-- {
		if cache.revisions[i].Branch == branch {
			return cache.revisions[i], true, nil
		}
	}
	return BatchState{}, false, nil
}

func (cache *memoryBatchCache) Save(state BatchState) error {
	if cache.fail() {
		return errors.New("cache write failed")
	}
	cache.revisions = append(cache.revisions, state)
	return nil
}

type fakeBatchGitHub struct {
	prs          map[int]githubchecks.PullRequestView
	comments     map[int64]string
	commentPR    map[int64]int
	noMerge      bool
	queue        bool
	batch        githubchecks.PullRequestView
	creates      int
	updates      int
	failAt, call int
}

func newFakeBatchGitHub() *fakeBatchGitHub {
	return &fakeBatchGitHub{prs: map[int]githubchecks.PullRequestView{}, comments: map[int64]string{}, commentPR: map[int64]int{}}
}

var errFakeGitHub = errors.New("fake GitHub failure")

func (gh *fakeBatchGitHub) fail() bool {
	gh.call++
	return gh.failAt != 0 && gh.call == gh.failAt
}

func (gh *fakeBatchGitHub) PullRequest(_ context.Context, _ string, number int) (githubchecks.PullRequestView, error) {
	if gh.fail() {
		return githubchecks.PullRequestView{}, errFakeGitHub
	}
	return gh.prs[number], nil
}

func (gh *fakeBatchGitHub) IssueComment(_ context.Context, _ string, id int64) (BatchComment, error) {
	if gh.fail() {
		return BatchComment{}, errFakeGitHub
	}
	body, ok := gh.comments[id]
	if !ok {
		return BatchComment{}, errors.New("comment not found")
	}
	return BatchComment{Body: body, PullRequest: gh.commentPR[id]}, nil
}

func (gh *fakeBatchGitHub) AllowsMergeCommits(context.Context, string) (bool, error) {
	if gh.fail() {
		return false, errFakeGitHub
	}
	return !gh.noMerge, nil
}

func (gh *fakeBatchGitHub) UsesMergeQueue(context.Context, string, string) (bool, error) {
	if gh.fail() {
		return false, errFakeGitHub
	}
	return gh.queue, nil
}

func (gh *fakeBatchGitHub) BatchPullRequest(context.Context, string, string) (githubchecks.PullRequestView, error) {
	if gh.fail() {
		return githubchecks.PullRequestView{}, errFakeGitHub
	}
	return gh.batch, nil
}

func (gh *fakeBatchGitHub) CreatePullRequest(_ context.Context, _, branch, base, _, body string) (int, error) {
	if gh.fail() {
		return 0, errFakeGitHub
	}
	gh.creates++
	gh.batch = githubchecks.PullRequestView{Number: 777, State: "open", Body: body}
	gh.batch.Head.Ref, gh.batch.Base.Ref = branch, base
	return 777, nil
}

func (gh *fakeBatchGitHub) UpdatePullRequestBody(_ context.Context, _ string, _ int, body string) error {
	if gh.fail() {
		return errFakeGitHub
	}
	gh.updates++
	gh.batch.Body = body
	return nil
}

func mustBatch(t *testing.T, want LandOutcome) func(BatchResult, error) BatchResult {
	t.Helper()
	return func(result BatchResult, err error) BatchResult {
		t.Helper()
		if err != nil {
			t.Fatalf("batch error: %v", err)
		}
		if result.Outcome != want {
			t.Fatalf("outcome = %s (%s: %s %+v), want %s", result.Outcome, result.RefusalCode, result.Reason, result.Refusals, want)
		}
		return result
	}
}

func TestE2EBatchCleanSourcesMakeOnePullRequest(t *testing.T) {
	t.Parallel()
	fixture := newBatchFixture(t)
	first, n1 := fixture.source("c.go", "package a\n\nconst C = 1\n")
	second, n2 := fixture.source("d.go", "package a\n\nconst D = 1\n")
	ctx := context.Background()
	created := mustBatch(t, LandSuccess)(CreatePullRequestBatch(ctx, fixture.options(first, second)))
	if created.PullRequest != "acme/app#777" || fixture.gh.creates != 1 || !created.Changed {
		t.Fatalf("created = %+v, creates = %d", created, fixture.gh.creates)
	}
	heads := strings.Fields(bgit(t, fixture.work, "rev-list", "--first-parent", "--parents", fixture.base+"..origin/public-demo-batch-1"))
	if len(heads) != 6 || heads[2] != fixture.gh.prs[n2].Head.SHA || heads[5] != fixture.gh.prs[n1].Head.SHA {
		t.Fatalf("first-parent merges = %v, want each pinned head as a second parent", heads)
	}
	state, ok, err := ParseBatchBody(fixture.gh.batch.Body)
	if err != nil || !ok || len(state.Steps) != 2 || state.Steps[0].Reviewer != "opus@claude-code@reviewer" {
		t.Fatalf("body block = %+v ok=%v err=%v", state, ok, err)
	}
	again := mustBatch(t, LandSuccess)(CreatePullRequestBatch(ctx, fixture.options(first, second)))
	if again.Changed || fixture.gh.creates != 1 || fixture.gh.updates != 0 {
		t.Fatalf("rerun changed the batch: changed=%v creates=%d updates=%d", again.Changed, fixture.gh.creates, fixture.gh.updates)
	}
	if other := mustBatch(t, LandRefused)(CreatePullRequestBatch(ctx, fixture.options(first))); other.RefusalCode != BatchRefusalWorktree {
		t.Fatalf("different pins on a built batch: %s", other.RefusalCode)
	}
	if swapped := mustBatch(t, LandRefused)(CreatePullRequestBatch(ctx, fixture.options(second, first))); swapped.RefusalCode != BatchRefusalWorktree {
		t.Fatalf("reordered pins on a built batch: %s", swapped.RefusalCode)
	}
	if resumed := mustBatch(t, LandSuccess)(ResumePullRequestBatch(ctx, fixture.options())); resumed.Changed {
		t.Fatal("resuming a finished batch changed it")
	}
	fixture.gh.batch.Body = "edited by hand"
	mustBatch(t, LandSuccess)(CreatePullRequestBatch(ctx, fixture.options(first, second)))
	if fixture.gh.updates != 1 || !strings.Contains(fixture.gh.batch.Body, batchBlockBegin) {
		t.Fatalf("a hand-edited body was not regenerated: updates=%d", fixture.gh.updates)
	}
	fixture.gh.batch.State = "closed"
	if closed := mustBatch(t, LandRefused)(CreatePullRequestBatch(ctx, fixture.options(first, second))); closed.RefusalCode != BatchRefusalClosed {
		t.Fatalf("republishing a closed batch = %s", closed.RefusalCode)
	}
	fixture.gh.batch.State = "open"
	writeBatchFile(t, fixture.work, "local.go", "package a\n")
	bgit(t, fixture.work, "add", "local.go")
	bgit(t, fixture.work, "commit", "--quiet", "-m", "unrecorded local commit")
	if moved := mustBatch(t, LandRefused)(CreatePullRequestBatch(ctx, fixture.options(first, second))); moved.RefusalCode != BatchRefusalWorktree || !strings.Contains(moved.Reason, "past the recorded batch head") {
		t.Fatalf("rerun past the recorded head = %s %q", moved.RefusalCode, moved.Reason)
	}
}

func TestE2EBatchConflictStopsAndResumeContinues(t *testing.T) {
	t.Parallel()
	fixture := newBatchFixture(t)
	first, _ := fixture.source("a.go", "package a\n\nconst A = 2\n")
	second, _ := fixture.source("a.go", "package a\n\nconst A = 3\n")
	third, _ := fixture.source("e.go", "package a\n\nconst E = 1\n")
	ctx := context.Background()
	stopped := mustBatch(t, LandFindings)(CreatePullRequestBatch(ctx, fixture.options(first, second, third)))
	if stopped.RefusalCode != BatchRefusalConflict || !strings.Contains(stopped.Reason, "a.go") || stopped.State.Steps[1].State != BatchStateConflicted {
		t.Fatalf("stopped = %s %q", stopped.RefusalCode, stopped.Reason)
	}
	if fixture.gh.creates != 0 {
		t.Fatal("a stopped batch opened a pull request")
	}
	if inMerge := mustBatch(t, LandRefused)(ResumePullRequestBatch(ctx, fixture.options())); inMerge.RefusalCode != BatchRefusalWorktree {
		t.Fatalf("resume with the merge still conflicted: %s", inMerge.RefusalCode)
	}
	// Taking one side leaves the merge in progress with a clean tree.
	bgit(t, fixture.work, "checkout", "--ours", "a.go")
	bgit(t, fixture.work, "add", "a.go")
	if uncommitted := mustBatch(t, LandRefused)(ResumePullRequestBatch(ctx, fixture.options())); uncommitted.RefusalCode != BatchRefusalResolutionInvalid {
		t.Fatalf("uncommitted merge = %s", uncommitted.RefusalCode)
	}
	writeBatchFile(t, fixture.work, "a.go", "package a\n\nconst A = 5\n")
	bgit(t, fixture.work, "add", "a.go")
	bgit(t, fixture.work, "commit", "--quiet", "--no-edit")
	resumed := mustBatch(t, LandSuccess)(ResumePullRequestBatch(ctx, fixture.options()))
	if resumed.State.Steps[1].State != BatchStateResolved || resumed.State.Steps[2].State != BatchStateMerged {
		t.Fatalf("resumed steps = %+v", resumed.State.Steps)
	}
}

func TestE2EBatchResumeRefusesAResolutionThatIsNotTheRecordedMerge(t *testing.T) {
	t.Parallel()
	fixture := newBatchFixture(t)
	first, _ := fixture.source("a.go", "package a\n\nconst A = 2\n")
	second, _ := fixture.source("a.go", "package a\n\nconst A = 3\n")
	ctx := context.Background()
	mustBatch(t, LandFindings)(CreatePullRequestBatch(ctx, fixture.options(first, second)))
	bgit(t, fixture.work, "merge", "--abort")
	writeBatchFile(t, fixture.work, "a.go", "package a\n\nconst A = 9\n")
	bgit(t, fixture.work, "commit", "--quiet", "-am", "not a merge")
	if refused := mustBatch(t, LandRefused)(ResumePullRequestBatch(ctx, fixture.options())); refused.RefusalCode != BatchRefusalResolutionInvalid {
		t.Fatalf("refusal = %s", refused.RefusalCode)
	}
}

func TestE2EBatchPublishRefusesARejectedPush(t *testing.T) {
	t.Parallel()
	fixture := newBatchFixture(t)
	first, _ := fixture.source("c.go", "package a\n\nconst C = 1\n")
	// Someone else already pushed the batch branch name.
	bgit(t, fixture.work, "fetch", "--quiet", "origin")
	_, foreign := fixture.source("y.go", "package a\n")
	bgit(t, fixture.work, "fetch", "--quiet", "origin")
	bgit(t, fixture.work, "push", "--quiet", "origin", fixture.gh.prs[foreign].Head.SHA+":refs/heads/public-demo-batch-1")
	if moved := mustBatch(t, LandRefused)(CreatePullRequestBatch(context.Background(), fixture.options(first))); moved.RefusalCode != BatchRefusalMoved {
		t.Fatalf("refusal = %s", moved.RefusalCode)
	}
}

func TestE2EBatchRefusesBadPinsTogetherAndCreatesNothing(t *testing.T) {
	t.Parallel()
	fixture := newBatchFixture(t)
	moved, movedPR := fixture.source("c.go", "package a\n\nconst C = 1\n")
	view := fixture.gh.prs[movedPR]
	view.Head.SHA = strings.Repeat("e", 40)
	fixture.gh.prs[movedPR] = view
	stale, stalePR := fixture.source("d.go", "package a\n\nconst D = 1\n")
	fixture.gh.comments[fixture.nextNote] = "**Review by opus@claude-code@reviewer**\nReviewed-Head: " + strings.Repeat("f", 40) + "\n"
	_ = stalePR
	draft, draftPR := fixture.source("e.go", "package a\n\nconst E = 1\n")
	view = fixture.gh.prs[draftPR]
	view.Draft = true
	fixture.gh.prs[draftPR] = view
	anonymous, _ := fixture.source("g.go", "package a\n\nconst G = 1\n")
	fixture.gh.comments[fixture.nextNote] = strings.Replace(fixture.gh.comments[fixture.nextNote], "opus@claude-code@reviewer", "unknown@unknown@unknown", 1)
	lane, _ := fixture.sourceFrom(fixture.base, "h.go", "package a\n", "lane-session")
	wrong, wrongPR := fixture.source("i.go", "package a\n")
	view = fixture.gh.prs[wrongPR]
	view.Base.Ref = "main"
	fixture.gh.prs[wrongPR] = view
	missing, _ := fixture.source("j.go", "package a\n")
	delete(fixture.gh.comments, fixture.nextNote)
	misbound, _ := fixture.source("p.go", "package a\n")
	fixture.gh.commentPR[fixture.nextNote] = 1
	operatorAsOther, _ := fixture.source("q.go", "package a\n")
	fixture.gh.comments[fixture.nextNote] = strings.Replace(fixture.gh.comments[fixture.nextNote], "opus@claude-code@reviewer", "sonnet@claude-code@operator", 1)
	parent, parentPR := fixture.source("k.go", "package a\n")
	child, _ := fixture.sourceFrom(fixture.gh.prs[parentPR].Head.SHA, "l.go", "package a\n", "reviewer")

	result := mustBatch(t, LandRefused)(CreatePullRequestBatch(context.Background(), fixture.options(moved, stale, draft, anonymous, lane, wrong, missing, misbound, operatorAsOther, parent, child)))
	codes := map[string]int{}
	for _, issue := range result.Refusals {
		codes[issue.Code]++
	}
	want := map[string]int{BatchRefusalSourceHeadMoved: 1, BatchRefusalSourceUnreviewed: 3, BatchRefusalSourceNotOpen: 1, BatchRefusalSourceSelfReview: 3, BatchRefusalSourceWrongBase: 1, BatchRefusalSourceAlreadyIncluded: 1}
	for code, count := range want {
		if codes[code] != count {
			t.Errorf("%s refused %d time(s), want %d: %+v", code, codes[code], count, result.Refusals)
		}
	}
	if fixture.gh.creates != 0 || len(fixture.cache.revisions) != 0 {
		t.Fatal("a refused batch created state")
	}
	if out := bgit(t, fixture.work, "ls-remote", "origin", "public-demo-batch-1"); out != "" {
		t.Fatalf("a refused batch pushed: %s", out)
	}

	landed, landedPR := fixture.source("m.go", "package a\n")
	bgit(t, fixture.work, "fetch", "--quiet", "origin")
	bgit(t, fixture.work, "push", "--quiet", "origin", fixture.gh.prs[landedPR].Head.SHA+":refs/heads/public-demo")
	bgit(t, fixture.work, "merge", "--quiet", "--ff-only", fixture.gh.prs[landedPR].Head.SHA)
	if already := mustBatch(t, LandRefused)(CreatePullRequestBatch(context.Background(), fixture.options(landed))); already.Refusals[0].Code != BatchRefusalSourceAlreadyLanded {
		t.Fatalf("landed pin = %+v", already.Refusals)
	}
}

func TestE2EBatchRefusesNonMergeTargetsAndBadWorktrees(t *testing.T) {
	t.Parallel()
	fixture := newBatchFixture(t)
	pin, _ := fixture.source("c.go", "package a\n")
	ctx := context.Background()
	fixture.gh.noMerge = true
	if refused := mustBatch(t, LandRefused)(CreatePullRequestBatch(ctx, fixture.options(pin))); refused.RefusalCode != BatchRefusalMergeCommitsDisabled {
		t.Fatalf("no merge commits: %s", refused.RefusalCode)
	}
	fixture.gh.noMerge, fixture.gh.queue = false, true
	if refused := mustBatch(t, LandRefused)(CreatePullRequestBatch(ctx, fixture.options(pin))); refused.RefusalCode != BatchRefusalTargetMergeQueue {
		t.Fatalf("merge queue: %s", refused.RefusalCode)
	}
	fixture.gh.queue = false
	if refused := mustBatch(t, LandRefused)(CreatePullRequestBatch(ctx, fixture.options())); refused.RefusalCode != BatchRefusalPinInvalid {
		t.Fatalf("no pins: %s", refused.RefusalCode)
	}
	if refused := mustBatch(t, LandRefused)(CreatePullRequestBatch(ctx, fixture.options("510@zz=nope"))); refused.RefusalCode != BatchRefusalPinInvalid {
		t.Fatalf("bad pin: %s", refused.RefusalCode)
	}
	cases := map[string]func(*BatchOptions){
		"no worktree": func(o *BatchOptions) { o.Worktree = "" },
		"unmanaged": func(o *BatchOptions) {
			o.checkout = func(string) (string, string, string, error) { return "", "", "", errors.New("no manifest") }
		},
		"foreign repository": func(o *BatchOptions) {
			o.checkout = func(string) (string, string, string, error) {
				return "acme/other", "public-demo-batch-1", "public-demo", nil
			}
		},
		"wrong base": func(o *BatchOptions) {
			o.checkout = func(string) (string, string, string, error) { return "acme/app", "public-demo-batch-1", "main", nil }
		},
		"wrong branch": func(o *BatchOptions) {
			o.checkout = func(string) (string, string, string, error) { return "acme/app", "feature", "public-demo", nil }
		},
		"missing directory": func(o *BatchOptions) { o.Worktree = filepath.Join(t.TempDir(), "gone") },
	}
	for name, mutate := range cases {
		for _, verb := range []func(context.Context, BatchOptions) (BatchResult, error){CreatePullRequestBatch, ResumePullRequestBatch} {
			options := fixture.options(pin)
			mutate(&options)
			if refused := mustBatch(t, LandRefused)(verb(ctx, options)); refused.RefusalCode != BatchRefusalWorktree {
				t.Errorf("%s: refusal = %s", name, refused.RefusalCode)
			}
		}
	}
	writeBatchFile(t, fixture.work, "dirty.go", "package a\n")
	if refused := mustBatch(t, LandRefused)(CreatePullRequestBatch(ctx, fixture.options(pin))); refused.RefusalCode != BatchRefusalWorktree || !strings.Contains(refused.Reason, "uncommitted") {
		t.Fatalf("dirty: %s %q", refused.RefusalCode, refused.Reason)
	}
	os.Remove(filepath.Join(fixture.work, "dirty.go"))
	fixture.advanceTarget("n.go", "package a\n")
	if refused := mustBatch(t, LandRefused)(CreatePullRequestBatch(ctx, fixture.options(pin))); refused.RefusalCode != BatchRefusalWorktree || !strings.Contains(refused.Reason, "not origin/public-demo") {
		t.Fatalf("stale worktree: %s %q", refused.RefusalCode, refused.Reason)
	}
	for _, verb := range []func(context.Context, BatchOptions) (BatchResult, error){ResumePullRequestBatch} {
		if refused := mustBatch(t, LandRefused)(verb(ctx, fixture.options())); refused.RefusalCode != BatchRefusalNotFound {
			t.Errorf("no batch: %s", refused.RefusalCode)
		}
	}
}

// failingRunner fails its Nth Run call and delegates the rest.
type failingRunner struct {
	runner.Runner
	failAt int
	calls  *int
}

func (failing failingRunner) Run(ctx context.Context, dir, name string, args ...string) (runner.Result, error) {
	*failing.calls++
	if *failing.calls == failing.failAt {
		return runner.Result{ExitCode: 128, Stderr: "injected"}, errors.New("injected failure")
	}
	return failing.Runner.Run(ctx, dir, name, args...)
}

// TestE2EBatchEveryGitOrGitHubFailureIsReportedNotSwallowed fails each git
// and GitHub call of a full create, conflict, resume, add, delta and status
// journey in turn, and requires the verb to report it.
func TestE2EBatchEveryGitOrGitHubFailureIsReportedNotSwallowed(t *testing.T) {
	t.Parallel()
	type step func(*batchFixture, BatchOptions) (BatchResult, error)
	journey := func(fixture *batchFixture) ([]string, []step) {
		first, _ := fixture.source("a.go", "package a\n\nconst A = 2\n")
		second, _ := fixture.source("a.go", "package a\n\nconst A = 3\n")
		third, _ := fixture.source("d.go", "package a\n")
		return []string{first, second, third}, []step{
			func(f *batchFixture, o BatchOptions) (BatchResult, error) {
				o.Pins = o.Pins[:2]
				return CreatePullRequestBatch(context.Background(), o)
			},
			func(f *batchFixture, o BatchOptions) (BatchResult, error) {
				writeBatchFile(f.t, f.work, "a.go", "package a\n\nconst A = 5\n")
				bgit(f.t, f.work, "commit", "--quiet", "-am", "resolve")
				return ResumePullRequestBatch(context.Background(), o)
			},
			func(f *batchFixture, o BatchOptions) (BatchResult, error) {
				f.gh.batch.Body = "edited by hand"
				o.Pins = o.Pins[:2]
				return CreatePullRequestBatch(context.Background(), o)
			},
		}
	}
	for stepIndex := 0; stepIndex < 3; stepIndex++ {
		for _, failing := range []string{"git", "GitHub", "cache"} {
			for failAt := 1; ; failAt++ {
				fixture := newBatchFixture(t)
				pins, steps := journey(fixture)
				calls := 0
				options := fixture.options(pins...)
				for i := 0; i < stepIndex; i++ {
					if _, err := steps[i](fixture, options); err != nil {
						t.Fatalf("setup step %d: %v", i, err)
					}
				}
				switch failing {
				case "GitHub":
					fixture.gh.call, fixture.gh.failAt = 0, failAt
				case "cache":
					fixture.cache.call, fixture.cache.failAt = 0, failAt
				default:
					options.run = failingRunner{Runner: runner.New(), failAt: failAt, calls: &calls}
				}
				result, err := steps[stepIndex](fixture, options)
				if (failing == "git" && calls < failAt) || (failing == "GitHub" && fixture.gh.call < failAt) || (failing == "cache" && fixture.cache.call < failAt) {
					break
				}
				if err == nil && result.Outcome == LandSuccess {
					t.Errorf("step %d, %s call %d failed but the verb succeeded: %+v", stepIndex, failing, failAt, result)
				}
			}
		}
	}
}

func TestE2EBatchCreateRefusesAStoppedBatch(t *testing.T) {
	t.Parallel()
	fixture := newBatchFixture(t)
	first, _ := fixture.source("a.go", "package a\n\nconst A = 2\n")
	second, _ := fixture.source("a.go", "package a\n\nconst A = 3\n")
	ctx := context.Background()
	mustBatch(t, LandFindings)(CreatePullRequestBatch(ctx, fixture.options(first, second)))
	writeBatchFile(t, fixture.work, "a.go", "package a\n\nconst A = 4\n")
	bgit(t, fixture.work, "commit", "--quiet", "-am", "resolve")
	if refused := mustBatch(t, LandRefused)(CreatePullRequestBatch(ctx, fixture.options(first, second))); refused.RefusalCode != BatchRefusalWorktree {
		t.Fatalf("create on a stopped batch = %s", refused.RefusalCode)
	}
}

func TestE2EBatchGitTimeoutIsApplied(t *testing.T) {
	t.Parallel()
	fixture := newBatchFixture(t)
	options := fixture.options()
	options.Timeout = time.Minute
	if out, err := options.git().out(context.Background(), "rev-parse", "HEAD"); err != nil || len(out) != 40 {
		t.Fatalf("rev-parse = %q %v", out, err)
	}
}

//nolint:paralleltest // Replaces PATH with a fake gh for the production adapter.
func TestE2EBatchProductionGitHubAdapterTalksToGH(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n  *pulls/5*) echo '{\"number\":5,\"state\":\"open\"}' ;;\n  *) echo '[]' ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	adapter := DefaultBatchGitHub()
	view, err := adapter.PullRequest(context.Background(), "acme/app", 5)
	if err != nil || view.Number != 5 {
		t.Fatalf("view = %+v err = %v", view, err)
	}
	if queued, err := adapter.UsesMergeQueue(context.Background(), "acme/app", "public-demo"); err != nil || queued {
		t.Fatalf("merge queue = %v %v", queued, err)
	}
}
