//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
)

// fakePromoteGitHub adds a pull request store to the commit comments.
type fakePromoteGitHub struct {
	*fakeIntegrationGitHub
	number     int
	body       string
	creates    int
	updates    int
	failUpdate bool
}

func (gh *fakePromoteGitHub) fail() bool {
	gh.calls++
	return gh.calls == gh.failAt
}

func (gh *fakePromoteGitHub) DefaultBranch(context.Context, string) (string, error) {
	if gh.fail() {
		return "", errFakeGitHub
	}
	return "main", nil
}

func (gh *fakePromoteGitHub) OpenPullRequest(context.Context, string, string, string) (int, string, error) {
	if gh.fail() {
		return 0, "", errFakeGitHub
	}
	return gh.number, gh.body, nil
}

func (gh *fakePromoteGitHub) CreatePullRequest(_ context.Context, _, _, _, _, body string) (int, error) {
	if gh.fail() {
		return 0, errFakeGitHub
	}
	gh.number, gh.body = 42, body
	gh.creates++
	return gh.number, nil
}

func (gh *fakePromoteGitHub) UpdatePullRequestBody(_ context.Context, _ string, _ int, body string) error {
	if gh.fail() || gh.failUpdate {
		return errFakeGitHub
	}
	gh.body = body
	gh.updates++
	return nil
}

func (fixture *integrationFixture) promoteOptions(github *fakePromoteGitHub) PromoteOptions {
	return PromoteOptions{Repository: "acme/app", Integration: "public-demo", Worktree: fixture.work, GitHub: github, run: runner.New(),
		checkout: func(string) (string, error) { return "acme/app", nil }}
}

// pushLanes merges and pushes reviewed lanes; it returns their pins.
func (fixture *integrationFixture) pushLanes(pins ...string) {
	fixture.t.Helper()
	mustIntegration(fixture.t, LandSuccess)(MergeIntegration(context.Background(), fixture.options(pins...)))
}

func mustPromote(t *testing.T, want LandOutcome) func(PromoteResult, error) PromoteResult {
	t.Helper()
	return func(result PromoteResult, err error) PromoteResult {
		t.Helper()
		if err != nil {
			t.Fatalf("promote error: %v", err)
		}
		if result.Outcome != want {
			t.Fatalf("outcome = %s (%s: %s), want %s", result.Outcome, result.RefusalCode, result.Reason, want)
		}
		return result
	}
}

func TestE2EPromoteOpensOnePullRequestAndAnEmptyDelta(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newIntegrationFixture(t)
	first, firstSHA := fixture.lane(fixture.base, "b.go", "package a\n", "reviewer")
	second, _ := fixture.lane(fixture.base, "c.go", "package a\n", "reviewer")
	fixture.pushLanes(first, second)
	github := &fakePromoteGitHub{fakeIntegrationGitHub: fixture.gh}
	delta := mustPromote(t, LandSuccess)(DeltaBatch(ctx, fixture.promoteOptions(github)))
	if len(delta.Delta) != 0 || len(delta.Lanes) != 2 || delta.To != "main" || !strings.Contains(delta.Reason, "integration delta: empty") {
		t.Fatalf("delta = %+v", delta)
	}
	promoted := mustPromote(t, LandSuccess)(PromoteIntegration(ctx, fixture.promoteOptions(github)))
	if promoted.PullRequest != "acme/app#42" || github.creates != 1 || !promoted.Changed {
		t.Fatalf("promoted = %+v", promoted)
	}
	for _, want := range []string{promoteMarker, "`lane-1`", "`" + firstSHA + "`", "wb pr batch delta acme/app --target public-demo --to main --overlap", "wb pr land acme/app#<number>"} {
		if !strings.Contains(github.body, want) {
			t.Errorf("body lacks %q:\n%s", want, github.body)
		}
	}
	again := mustPromote(t, LandSuccess)(PromoteIntegration(ctx, fixture.promoteOptions(github)))
	if again.Changed || github.creates != 1 || github.updates != 0 {
		t.Fatalf("rerun changed something: %+v", again)
	}
	github.body, github.failUpdate = "edited", true
	if _, err := PromoteIntegration(ctx, fixture.promoteOptions(github)); err == nil {
		t.Fatal("a failed body refresh was swallowed")
	}
	github.failUpdate = false
	mustPromote(t, LandSuccess)(PromoteIntegration(ctx, fixture.promoteOptions(github)))
	if github.updates != 1 || !strings.Contains(github.body, promoteMarker) {
		t.Fatal("an edited body was not refreshed")
	}
	if text := PromoteText(promoted); !strings.Contains(text, "pull request: acme/app#42") || !strings.Contains(text, "lane lane-1@") {
		t.Fatalf("text = %s", text)
	}
}

func TestE2EDeltaShowsOnlyResolutionsFixesAndOverlap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newIntegrationFixture(t)
	first, _ := fixture.lane(fixture.base, "a.go", "package a\n\nconst A = 2\n", "reviewer")
	second, _ := fixture.lane(fixture.base, "a.go", "package a\n\nconst A = 3\n", "reviewer")
	options := fixture.options(first, second)
	mustIntegration(t, LandFindings)(AddToBatch(ctx, options))
	writeIntegrationFile(t, fixture.work, "a.go", "package a\n\nconst A = 5\n")
	igit(t, fixture.work, "commit", "--quiet", "-a", "--no-edit")
	writeIntegrationFile(t, fixture.work, "fix.go", "package a\n")
	igit(t, fixture.work, "add", "fix.go")
	igit(t, fixture.work, "commit", "--quiet", "-m", "fix")
	fixture.pushLanes()
	github := &fakePromoteGitHub{fakeIntegrationGitHub: fixture.gh}
	promote := fixture.promoteOptions(github)
	promote.To, promote.Overlap = "main", true
	result := mustPromote(t, LandSuccess)(DeltaBatch(ctx, promote))
	kinds := map[string][]BatchDelta{}
	for _, part := range result.Delta {
		kinds[part.Kind] = append(kinds[part.Kind], part)
	}
	if len(kinds[BatchDeltaResolution]) != 1 || strings.Join(kinds[BatchDeltaResolution][0].Paths, ",") != "a.go" || !strings.Contains(kinds[BatchDeltaResolution][0].Diff, "A = 5") {
		t.Fatalf("resolutions = %+v", kinds[BatchDeltaResolution])
	}
	if len(kinds[BatchDeltaFix]) != 1 || strings.Join(kinds[BatchDeltaFix][0].Paths, ",") != "fix.go" {
		t.Fatalf("fixes = %+v", kinds[BatchDeltaFix])
	}
	if len(kinds[BatchDeltaOverlap]) != 2 || kinds[BatchDeltaOverlap][0].Lane == "" {
		t.Fatalf("overlap = %+v", kinds[BatchDeltaOverlap])
	}
	if !strings.Contains(PromoteText(result), "--- resolution ") || !strings.Contains(result.Reason, "part(s)") {
		t.Fatalf("text = %s", PromoteText(result))
	}
}

func TestE2EUnrecordedMergesAreShownInFullAndRefusePromotion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newIntegrationFixture(t)
	reviewed, reviewedSHA := fixture.lane(fixture.base, "b.go", "package a\n", "reviewer")
	fixture.pushLanes(reviewed)
	// Someone pushes a plain merge of an unreviewed branch.
	_, unreviewedSHA := fixture.lane(fixture.base, "x.go", "package a\n\nconst X = 1\n", "reviewer")
	dir := filepath.Join(t.TempDir(), "hand")
	igit(t, filepath.Dir(dir), "clone", "--quiet", "--branch", "public-demo", fixture.origin, dir)
	identify(t, dir)
	igit(t, dir, "merge", "--quiet", "--no-ff", "--no-edit", unreviewedSHA)
	igit(t, dir, "push", "--quiet", "origin", "HEAD:public-demo")
	github := &fakePromoteGitHub{fakeIntegrationGitHub: fixture.gh}
	delta := mustPromote(t, LandFindings)(DeltaBatch(ctx, fixture.promoteOptions(github)))
	if delta.RefusalCode != BatchRefusalUnrecordedMerge || len(delta.Unrecorded) != 1 || len(delta.Delta) != 1 || delta.Delta[0].Kind != BatchDeltaUnrecorded || !strings.Contains(delta.Delta[0].Diff, "X = 1") {
		t.Fatalf("delta = %+v", delta)
	}
	if !strings.Contains(PromoteText(delta), "unrecorded merge ") {
		t.Fatalf("text = %s", PromoteText(delta))
	}
	refused := mustPromote(t, LandRefused)(PromoteIntegration(ctx, fixture.promoteOptions(github)))
	if refused.RefusalCode != BatchRefusalUnrecordedMerge || github.creates != 0 {
		t.Fatalf("refused = %+v", refused)
	}
	// An edited lane review no longer binds either.
	edited := newIntegrationFixture(t)
	pin, sha := edited.lane(edited.base, "b.go", "package a\n", "reviewer")
	edited.pushLanes(pin)
	comment := edited.gh.comments[101]
	comment.Body += "\nedited later"
	edited.gh.comments[101] = comment
	editedGitHub := &fakePromoteGitHub{fakeIntegrationGitHub: edited.gh}
	if result := mustPromote(t, LandFindings)(DeltaBatch(ctx, edited.promoteOptions(editedGitHub))); len(result.Unrecorded) != 1 {
		t.Fatalf("edited review: %+v (%s)", result, sha)
	}
	_ = reviewedSHA
}

func TestE2EATargetRefreshIsNotUnrecordedAndAPromotedBranchHasNothingToPromote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newIntegrationFixture(t)
	pin, _ := fixture.lane(fixture.base, "b.go", "package a\n", "reviewer")
	fixture.pushLanes(pin)
	fixture.advance("main", "m.go", "package a\n")
	dir := filepath.Join(t.TempDir(), "refresh")
	igit(t, filepath.Dir(dir), "clone", "--quiet", "--branch", "public-demo", fixture.origin, dir)
	identify(t, dir)
	igit(t, dir, "merge", "--quiet", "--no-ff", "--no-edit", "origin/main")
	igit(t, dir, "push", "--quiet", "origin", "HEAD:public-demo")
	github := &fakePromoteGitHub{fakeIntegrationGitHub: fixture.gh}
	if delta := mustPromote(t, LandSuccess)(DeltaBatch(ctx, fixture.promoteOptions(github))); len(delta.Unrecorded) != 0 || len(delta.Delta) != 0 || len(delta.Lanes) != 1 {
		t.Fatalf("refresh delta = %+v", delta)
	}
	// main takes the whole integration branch: nothing is left to promote.
	igit(t, dir, "push", "--quiet", "origin", "HEAD:main")
	nothing := mustPromote(t, LandSuccess)(PromoteIntegration(ctx, fixture.promoteOptions(github)))
	if !strings.Contains(nothing.Reason, "nothing to promote") || github.creates != 0 {
		t.Fatalf("nothing = %+v", nothing)
	}
}

func TestE2EPinlessIntegrationMergeRefusesAnUnrecordedLocalMerge(t *testing.T) {
	t.Parallel()
	fixture := newIntegrationFixture(t)
	_, sha := fixture.lane(fixture.base, "b.go", "package a\n", "reviewer")
	igit(t, fixture.work, "fetch", "--quiet", "origin")
	igit(t, fixture.work, "merge", "--quiet", "--no-ff", "--no-edit", sha)
	refused := mustIntegration(t, LandRefused)(MergeIntegration(context.Background(), fixture.options()))
	if refused.RefusalCode != BatchRefusalUnrecordedMerge || fixture.verified != 0 {
		t.Fatalf("refused = %+v", refused)
	}
}

func TestE2EPromoteRefusesAnUnusableCheckout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newIntegrationFixture(t)
	github := &fakePromoteGitHub{fakeIntegrationGitHub: fixture.gh}
	for name, mutate := range map[string]func(*PromoteOptions){
		"bad integration":  func(o *PromoteOptions) { o.Integration = "-x" },
		"bad target":       func(o *PromoteOptions) { o.To = "a..b" },
		"no checkout":      func(o *PromoteOptions) { o.Worktree = "" },
		"other repository": func(o *PromoteOptions) { o.checkout = func(string) (string, error) { return "acme/other", nil } },
		"missing branch":   func(o *PromoteOptions) { o.Integration = "nowhere" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, verb := range []func(context.Context, PromoteOptions) (PromoteResult, error){DeltaBatch, PromoteIntegration} {
				options := fixture.promoteOptions(github)
				options.To = "main"
				mutate(&options)
				if refused := mustPromote(t, LandRefused)(verb(ctx, options)); refused.RefusalCode != BatchRefusalWorktree {
					t.Fatalf("refusal = %s (%s)", refused.RefusalCode, refused.Reason)
				}
			}
		})
	}
}

func TestE2EPromoteEveryGitOrGitHubFailureIsReportedNotSwallowed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, verb := range []func(context.Context, PromoteOptions) (PromoteResult, error){DeltaBatch, PromoteIntegration} {
		for _, failing := range []string{"git", "GitHub"} {
			for failAt := 1; ; failAt++ {
				fixture := newIntegrationFixture(t)
				first, _ := fixture.lane(fixture.base, "a.go", "package a\n\nconst A = 2\n", "reviewer")
				second, _ := fixture.lane(fixture.base, "a.go", "package a\n\nconst A = 3\n", "reviewer")
				mustIntegration(t, LandFindings)(AddToBatch(ctx, fixture.options(first, second)))
				writeIntegrationFile(t, fixture.work, "a.go", "package a\n\nconst A = 5\n")
				igit(t, fixture.work, "commit", "--quiet", "-a", "--no-edit")
				writeIntegrationFile(t, fixture.work, "fix.go", "package a\n")
				igit(t, fixture.work, "add", "fix.go")
				igit(t, fixture.work, "commit", "--quiet", "-m", "fix")
				fixture.pushLanes()
				github := &fakePromoteGitHub{fakeIntegrationGitHub: fixture.gh}
				options := fixture.promoteOptions(github)
				options.Overlap = true
				calls := 0
				fixture.gh.calls = 0
				if failing == "GitHub" {
					fixture.gh.failAt = failAt
				} else {
					options.run = failingIntegrationRunner{Runner: runner.New(), failAt: failAt, calls: &calls}
				}
				result, err := verb(ctx, options)
				if (failing == "git" && calls < failAt) || (failing == "GitHub" && fixture.gh.calls < failAt) {
					break
				}
				// A failed comment read makes a lane unrecorded, which is a
				// finding or refusal, never success.
				if err == nil && result.Outcome == LandSuccess {
					t.Errorf("%s failure at call %d was swallowed: %+v", failing, failAt, result)
				}
			}
		}
	}
}

func TestE2EPromotionAdaptersReportFailedCalls(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readIssueComment(ctx, "acme/app", 1); err == nil {
		t.Fatal("a cancelled comment read succeeded")
	}
	if !errors.Is(errFakeGitHub, errFakeGitHub) {
		t.Fatal("unreachable")
	}
}
