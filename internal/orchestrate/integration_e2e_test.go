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

	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/runner"
)

// integrationFixture is an origin with main and an integration branch
// public-demo, lane branches pushed with no pull request, an integration
// worktree, and in-memory commit comments.
type integrationFixture struct {
	t        *testing.T
	origin   string
	work     string
	base     string
	gh       *fakeIntegrationGitHub
	verified int
	report   quality.VerificationReport
	nextLane int
}

type fakeIntegrationGitHub struct {
	comments map[int64]CommitComment
	failAt   int
	calls    int
}

var errFakeGitHub = errors.New("fake GitHub failure")

func (gh *fakeIntegrationGitHub) CommitComment(_ context.Context, _ string, id int64) (CommitComment, error) {
	gh.calls++
	if gh.calls == gh.failAt {
		return CommitComment{}, errFakeGitHub
	}
	comment, ok := gh.comments[id]
	if !ok {
		return CommitComment{}, fmt.Errorf("comment %d: 404", id)
	}
	return comment, nil
}

func igit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "user.name=WB Test", "-c", "user.email=wb@example.test", "-c", "commit.gpgsign=false"}, args...)...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeIntegrationFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// identify gives a clone the verbs run git in its own committer identity:
// the CI runner has no global one.
func identify(t *testing.T, dir string) {
	t.Helper()
	igit(t, dir, "config", "user.name", "WB Test")
	igit(t, dir, "config", "user.email", "wb@example.test")
	igit(t, dir, "config", "commit.gpgsign", "false")
}

func newIntegrationFixture(t *testing.T) *integrationFixture {
	t.Helper()
	root := t.TempDir()
	origin, seed := filepath.Join(root, "origin.git"), filepath.Join(root, "seed")
	igit(t, root, "init", "--quiet", "--bare", "--initial-branch=main", origin)
	igit(t, origin, "config", "uploadpack.allowReachableSHA1InWant", "true")
	igit(t, root, "clone", "--quiet", origin, seed)
	writeIntegrationFile(t, seed, "a.go", "package a\n\nconst A = 1\n")
	igit(t, seed, "add", "-A")
	igit(t, seed, "commit", "--quiet", "-m", "base")
	igit(t, seed, "push", "--quiet", "origin", "HEAD:main", "HEAD:public-demo")
	fixture := &integrationFixture{t: t, origin: origin, work: filepath.Join(root, "work"),
		gh: &fakeIntegrationGitHub{comments: map[int64]CommitComment{}}, report: quality.VerificationReport{Status: quality.StatusPassed}}
	fixture.base = igit(t, seed, "rev-parse", "HEAD")
	igit(t, root, "clone", "--quiet", "--branch", "public-demo", origin, fixture.work)
	igit(t, fixture.work, "switch", "--quiet", "-c", "merge-1")
	identify(t, fixture.work)
	return fixture
}

func (fixture *integrationFixture) options(pins ...string) IntegrationOptions {
	return IntegrationOptions{
		Repository: "acme/app", Integration: "public-demo", Worktree: fixture.work, Pins: pins,
		GitHub: fixture.gh, run: runner.New(),
		checkout:    func(string) (string, error) { return "acme/app", nil },
		laneSession: func(string, string, string) string { return "lane-session" },
		operator: func() ReviewerIdentity {
			return ReviewerIdentity{Model: "opus", Harness: "claude-code", Session: "operator"}
		},
		verify: func(context.Context, string, string, []quality.Check, time.Duration) quality.VerificationReport {
			fixture.verified++
			return fixture.report
		},
	}
}

// lane pushes a branch off from changing file and reviews its tip with a
// commit comment by reviewerSession; it returns the pin and the lane SHA.
func (fixture *integrationFixture) lane(from, file, contents, reviewerSession string) (string, string) {
	t := fixture.t
	fixture.nextLane++
	branch := "lane-" + strconv.Itoa(fixture.nextLane)
	dir := filepath.Join(t.TempDir(), "lane")
	igit(t, filepath.Dir(dir), "clone", "--quiet", fixture.origin, dir)
	igit(t, dir, "switch", "--quiet", "-c", branch, from)
	writeIntegrationFile(t, dir, file, contents)
	igit(t, dir, "add", "-A")
	igit(t, dir, "commit", "--quiet", "-m", branch)
	igit(t, dir, "push", "--quiet", "origin", "HEAD:"+branch)
	sha := igit(t, dir, "rev-parse", "HEAD")
	id := int64(100 + fixture.nextLane)
	fixture.gh.comments[id] = CommitComment{CommitID: sha, Body: fmt.Sprintf("**Review by opus@codex@%s**\n\nLGTM\n\nReviewed-Head: %s\n", reviewerSession, sha)}
	return pinFor(branch, sha, id), sha
}

func pinFor(branch, sha string, id int64) string {
	return fmt.Sprintf("%s@%s=https://github.com/acme/app/commit/%s#commitcomment-%d", branch, sha[:12], sha, id)
}

func (fixture *integrationFixture) advance(branch, file, contents string) string {
	t := fixture.t
	dir := filepath.Join(t.TempDir(), "advance")
	igit(t, filepath.Dir(dir), "clone", "--quiet", "--branch", branch, fixture.origin, dir)
	writeIntegrationFile(t, dir, file, contents)
	igit(t, dir, "add", "-A")
	igit(t, dir, "commit", "--quiet", "-m", "advance "+branch)
	igit(t, dir, "push", "--quiet", "origin", "HEAD:"+branch)
	return igit(t, dir, "rev-parse", "HEAD")
}

func mustIntegration(t *testing.T, want LandOutcome) func(IntegrationResult, error) IntegrationResult {
	t.Helper()
	return func(result IntegrationResult, err error) IntegrationResult {
		t.Helper()
		if err != nil {
			t.Fatalf("integration error: %v", err)
		}
		if result.Outcome != want {
			t.Fatalf("outcome = %s (%s: %s), want %s", result.Outcome, result.RefusalCode, result.Reason, want)
		}
		return result
	}
}

func TestE2EIntegrationMergeChecksAndPushesReviewedLanes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newIntegrationFixture(t)
	first, firstSHA := fixture.lane(fixture.base, "b.go", "package a\n", "reviewer")
	second, secondSHA := fixture.lane(fixture.base, "c.go", "package a\n", "reviewer")
	result := mustIntegration(t, LandSuccess)(MergeIntegration(ctx, fixture.options(first, second)))
	if !result.Pushed || fixture.verified != 1 || len(result.Lanes) != 2 || result.Lanes[1].State != LaneStateMerged {
		t.Fatalf("result = %+v verified = %d", result, fixture.verified)
	}
	pushed := igit(t, fixture.origin, "rev-parse", "public-demo")
	if pushed != result.Head {
		t.Fatalf("origin public-demo = %s, want %s", pushed, result.Head)
	}
	parents := strings.Fields(igit(t, fixture.origin, "rev-list", "--first-parent", "--parents", fixture.base+".."+pushed))
	// Two merge commits, newest first: <merge> <first parent> <second parent>.
	if len(parents) != 6 || parents[2] != secondSHA || parents[5] != firstSHA {
		t.Fatalf("first-parent history = %v", parents)
	}
	message := igit(t, fixture.origin, "log", "-1", "--format=%B", pushed)
	for _, want := range []string{"Merge lane-2 (" + secondSHA[:12] + ") into public-demo", "Wb-Lane: lane-2@" + secondSHA,
		"Wb-Review: https://github.com/acme/app/commit/" + secondSHA + "#commitcomment-102", "Wb-Review-Digest: ", "Wb-Reviewer: opus@codex@reviewer",
		"Wb-Merged-By: opus@claude-code@operator"} {
		if !strings.Contains(message, want) {
			t.Errorf("merge message lacks %q:\n%s", want, message)
		}
	}
	if trailers := igit(t, fixture.origin, "log", "-1", "--format=%(trailers:key=Wb-Lane,valueonly)", pushed); trailers != "lane-2@"+secondSHA {
		t.Fatalf("git does not read the Wb-Lane trailer: %q", trailers)
	}
	again := mustIntegration(t, LandSuccess)(MergeIntegration(ctx, fixture.options(first, second)))
	if again.Changed || again.Pushed || fixture.verified != 1 || again.Lanes[0].State != LaneStateAlreadyMerged {
		t.Fatalf("rerun changed something: %+v verified = %d", again, fixture.verified)
	}
}

func TestE2EBatchAddGivesAPinUnderAnEarlierPinNoMergeOfItsOwn(t *testing.T) {
	t.Parallel()
	fixture := newIntegrationFixture(t)
	lower, lowerSHA := fixture.lane(fixture.base, "b.go", "package a\n", "reviewer")
	upper, upperSHA := fixture.lane(lowerSHA, "c.go", "package a\n", "reviewer")
	result := mustIntegration(t, LandSuccess)(AddToBatch(context.Background(), fixture.options(upper, lower)))
	if result.Lanes[0].State != LaneStateMerged || result.Lanes[0].SHA != upperSHA || result.Lanes[1].State != LaneStateAlreadyMerged {
		t.Fatalf("lanes = %+v", result.Lanes)
	}
	if !strings.Contains(result.Reason, "merged 1 lane(s), 1 already merged") {
		t.Fatalf("reason = %s", result.Reason)
	}
}

func TestE2EBatchAddMergesWithoutPushingAndNamesTheNextCommand(t *testing.T) {
	t.Parallel()
	fixture := newIntegrationFixture(t)
	pin, _ := fixture.lane(fixture.base, "b.go", "package a\n", "reviewer")
	result := mustIntegration(t, LandSuccess)(AddToBatch(context.Background(), fixture.options(pin)))
	if result.Pushed || igit(t, fixture.origin, "rev-parse", "public-demo") != fixture.base || !strings.Contains(result.Command, "wb integration merge acme/app public-demo") {
		t.Fatalf("result = %+v", result)
	}
	if fixture.verified != 0 {
		t.Fatal("batch add ran the local checks")
	}
}

func TestE2EBatchAddRefusesEveryBadPinTogether(t *testing.T) {
	t.Parallel()
	fixture := newIntegrationFixture(t)
	good, goodSHA := fixture.lane(fixture.base, "b.go", "package a\n", "reviewer")
	_, otherSHA := fixture.lane(fixture.base, "c.go", "package a\n", "reviewer")
	fixture.gh.comments[900] = CommitComment{CommitID: goodSHA, Body: "**Review by opus@codex@reviewer**\nReviewed-Head: " + otherSHA + "\n"}
	fixture.gh.comments[901] = CommitComment{CommitID: otherSHA, Body: "**Review by opus@codex@reviewer**\nReviewed-Head: " + goodSHA + "\n"}
	fixture.gh.comments[902] = CommitComment{CommitID: goodSHA, Body: "**Review by opus@codex@lane-session**\nReviewed-Head: " + goodSHA + "\n"}
	fixture.gh.comments[903] = CommitComment{CommitID: goodSHA, Body: "**Review by opus@codex@operator**\nReviewed-Head: " + goodSHA + "\n"}
	fixture.gh.comments[904] = CommitComment{CommitID: goodSHA, Body: "Reviewed-Head: " + goodSHA + "\n"}
	pins := []string{
		pinFor("lane-2", otherSHA, 102),                                             // valid; it fetches otherSHA for the next pin
		pinFor("lane-1", otherSHA, 102),                                             // the SHA is not on lane-1
		pinFor("missing", goodSHA, 101),                                             // no such branch
		pinFor("lane-1", goodSHA, 900),                                              // Reviewed-Head names another commit
		pinFor("lane-1", goodSHA, 901),                                              // the comment is on another commit
		pinFor("lane-1", goodSHA, 902),                                              // the lane author reviewed it
		pinFor("lane-1", goodSHA, 903),                                              // the operator reviewed it
		pinFor("lane-1", goodSHA, 904),                                              // no reviewer identity
		pinFor("lane-1", goodSHA, 999),                                              // the comment does not exist
		"lane-1@0000000=https://github.com/acme/app/commit/0000000#commitcomment-1", // no such commit
		good,
	}
	result := mustIntegration(t, LandRefused)(AddToBatch(context.Background(), fixture.options(pins...)))
	want := []string{BatchRefusalSourceNotOnBranch, BatchRefusalSourceNotOnBranch, BatchRefusalSourceUnreviewed, BatchRefusalSourceUnreviewed,
		BatchRefusalSourceSelfReview, BatchRefusalSourceSelfReview, BatchRefusalSourceSelfReview, BatchRefusalSourceUnreviewed, BatchRefusalSourceNotOnBranch}
	if len(result.Refusals) != len(want) {
		t.Fatalf("refusals = %+v", result.Refusals)
	}
	for i, code := range want {
		if result.Refusals[i].Code != code {
			t.Errorf("refusal %d = %s, want %s (%s)", i, result.Refusals[i].Code, code, result.Refusals[i].Reason)
		}
	}
	if head := igit(t, fixture.work, "rev-parse", "HEAD"); head != fixture.base {
		t.Fatalf("a refused add moved HEAD to %s", head)
	}
}

func TestE2EBatchConflictStopsAndARerunContinues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newIntegrationFixture(t)
	first, _ := fixture.lane(fixture.base, "a.go", "package a\n\nconst A = 2\n", "reviewer")
	second, secondSHA := fixture.lane(fixture.base, "a.go", "package a\n\nconst A = 3\n", "reviewer")
	third, _ := fixture.lane(fixture.base, "d.go", "package a\n", "reviewer")
	options := fixture.options(first, second, third)
	stopped := mustIntegration(t, LandFindings)(AddToBatch(ctx, options))
	if stopped.RefusalCode != BatchRefusalConflict || stopped.Lanes[1].State != LaneStateConflicted || strings.Join(stopped.Lanes[1].ConflictPaths, ",") != "a.go" {
		t.Fatalf("stopped = %+v", stopped)
	}
	if !strings.Contains(stopped.Command, "wb pr batch add acme/app --target public-demo") || !strings.Contains(stopped.Command, "'"+second+"'") {
		t.Fatalf("rerun command = %s", stopped.Command)
	}
	// A rerun before the resolution is committed is refused.
	if refused := mustIntegration(t, LandRefused)(AddToBatch(ctx, options)); refused.RefusalCode != BatchRefusalWorktree {
		t.Fatalf("mid-merge rerun = %+v", refused)
	}
	writeIntegrationFile(t, fixture.work, "a.go", "package a\n\nconst A = 5\n")
	igit(t, fixture.work, "add", "a.go")
	igit(t, fixture.work, "commit", "--quiet", "--no-edit")
	if lane := igit(t, fixture.work, "log", "-1", "--format=%(trailers:key=Wb-Lane,valueonly)"); lane != "lane-2@"+secondSHA {
		t.Fatalf("the resolution lost its trailers: %q", lane)
	}
	resumed := mustIntegration(t, LandSuccess)(AddToBatch(ctx, options))
	states := []string{resumed.Lanes[0].State, resumed.Lanes[1].State, resumed.Lanes[2].State}
	if strings.Join(states, ",") != "already-merged,already-merged,merged" {
		t.Fatalf("states = %v", states)
	}
}

func TestE2EIntegrationMergeFailedCheckPushesNothing(t *testing.T) {
	t.Parallel()
	fixture := newIntegrationFixture(t)
	pin, _ := fixture.lane(fixture.base, "b.go", "package a\n", "reviewer")
	fixture.report = quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{Check: quality.CheckTest, Status: quality.StatusFailed, Detail: "boom"}}}
	options := fixture.options(pin)
	options.Checks = []quality.Check{quality.CheckTest}
	result := mustIntegration(t, LandFindings)(MergeIntegration(context.Background(), options))
	if result.RefusalCode != IntegrationRefusalCheckFailed || result.Pushed || igit(t, fixture.origin, "rev-parse", "public-demo") != fixture.base {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(IntegrationText(result), "check test failed: boom") {
		t.Fatalf("text = %s", IntegrationText(result))
	}
}

func TestE2EIntegrationMergeNeverForcePushesOverAMovedBranch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newIntegrationFixture(t)
	pin, _ := fixture.lane(fixture.base, "b.go", "package a\n", "reviewer")
	mustIntegration(t, LandSuccess)(AddToBatch(ctx, fixture.options(pin)))
	moved := fixture.advance("public-demo", "z.go", "package a\n")
	// The pushed-without-pins path pushes what add merged; origin moved first.
	options := fixture.options()
	options.verify = func(context.Context, string, string, []quality.Check, time.Duration) quality.VerificationReport {
		return quality.VerificationReport{Status: quality.StatusPassed}
	}
	igit(t, fixture.work, "fetch", "--quiet", "origin")
	result, err := MergeIntegration(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	// HEAD no longer contains origin/public-demo, so the worktree is refused
	// before any push.
	if result.RefusalCode != BatchRefusalWorktree {
		t.Fatalf("result = %+v", result)
	}
	if igit(t, fixture.origin, "rev-parse", "public-demo") != moved {
		t.Fatal("origin was overwritten")
	}
	// A push race after the check: origin moves while the checks run.
	race := newIntegrationFixture(t)
	racePin, _ := race.lane(race.base, "b.go", "package a\n", "reviewer")
	raceOptions := race.options(racePin)
	raceOptions.verify = func(context.Context, string, string, []quality.Check, time.Duration) quality.VerificationReport {
		race.advance("public-demo", "z.go", "package a\n")
		return quality.VerificationReport{Status: quality.StatusPassed}
	}
	raced := mustIntegration(t, LandRefused)(MergeIntegration(ctx, raceOptions))
	if raced.RefusalCode != IntegrationRefusalMoved || raced.Pushed {
		t.Fatalf("raced = %+v", raced)
	}
}

func TestE2EIntegrationMergeWithNothingNewRunsNoCheck(t *testing.T) {
	t.Parallel()
	fixture := newIntegrationFixture(t)
	result := mustIntegration(t, LandSuccess)(MergeIntegration(context.Background(), fixture.options()))
	if result.Pushed || result.Changed || fixture.verified != 0 || !strings.Contains(result.Reason, "nothing to check or push") {
		t.Fatalf("result = %+v verified = %d", result, fixture.verified)
	}
}

func TestE2EIntegrationRefusesAnUnusableWorktree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newIntegrationFixture(t)
	pin, _ := fixture.lane(fixture.base, "b.go", "package a\n", "reviewer")
	for name, mutate := range map[string]func(*IntegrationOptions){
		"no worktree": func(o *IntegrationOptions) { o.Worktree = "" },
		"not managed": func(o *IntegrationOptions) {
			o.checkout = func(string) (string, error) { return "", errors.New("no manifest") }
		},
		"other repository": func(o *IntegrationOptions) { o.checkout = func(string) (string, error) { return "acme/other", nil } },
		"not a checkout":   func(o *IntegrationOptions) { o.Worktree = t.TempDir() },
		"missing branch":   func(o *IntegrationOptions) { o.Integration = "nowhere" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, verb := range []func(context.Context, IntegrationOptions) (IntegrationResult, error){AddToBatch, MergeIntegration} {
				options := fixture.options(pin)
				mutate(&options)
				if refused := mustIntegration(t, LandRefused)(verb(ctx, options)); refused.RefusalCode != BatchRefusalWorktree {
					t.Fatalf("refusal = %s (%s)", refused.RefusalCode, refused.Reason)
				}
			}
		})
	}
	dirty := newIntegrationFixture(t)
	writeIntegrationFile(t, dirty.work, "dirty.go", "package a\n")
	if refused := mustIntegration(t, LandRefused)(MergeIntegration(ctx, dirty.options())); !strings.Contains(refused.Reason, "uncommitted") {
		t.Fatalf("dirty: %q", refused.Reason)
	}
}

func TestE2EIntegrationGHReportsAFailedCall(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := integrationGH(ctx, "api", "repos/acme/app/comments/1"); err == nil {
		t.Fatal("a cancelled gh call succeeded")
	}
}

type failingIntegrationRunner struct {
	runner.Runner
	failAt int
	calls  *int
}

func (failing failingIntegrationRunner) Run(ctx context.Context, dir, name string, args ...string) (runner.Result, error) {
	*failing.calls++
	if *failing.calls == failing.failAt {
		return runner.Result{ExitCode: 128, Stderr: "injected"}, errors.New("injected failure")
	}
	return failing.Runner.Run(ctx, dir, name, args...)
}

// TestE2EIntegrationEveryGitOrGitHubFailureIsReportedNotSwallowed fails each
// git and GitHub call of a conflict, a rerun and a push in turn, and requires
// every failure to surface as an error or a refusal, never as success.
func TestE2EIntegrationEveryGitOrGitHubFailureIsReportedNotSwallowed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	journey := func(fixture *integrationFixture) []func(IntegrationOptions) (IntegrationResult, error) {
		first, _ := fixture.lane(fixture.base, "a.go", "package a\n\nconst A = 2\n", "reviewer")
		second, _ := fixture.lane(fixture.base, "a.go", "package a\n\nconst A = 3\n", "reviewer")
		third, _ := fixture.lane(fixture.base, "d.go", "package a\n", "reviewer")
		return []func(IntegrationOptions) (IntegrationResult, error){
			func(o IntegrationOptions) (IntegrationResult, error) {
				o.Pins = []string{first, second}
				return AddToBatch(ctx, o)
			},
			func(o IntegrationOptions) (IntegrationResult, error) {
				writeIntegrationFile(fixture.t, fixture.work, "a.go", "package a\n\nconst A = 5\n")
				igit(fixture.t, fixture.work, "commit", "--quiet", "-am", "resolve")
				o.Pins = []string{first, second, third}
				return MergeIntegration(ctx, o)
			},
			func(o IntegrationOptions) (IntegrationResult, error) {
				return MergeIntegration(ctx, o)
			},
		}
	}
	for stepIndex := 0; stepIndex < 3; stepIndex++ {
		for _, failing := range []string{"git", "GitHub"} {
			for failAt := 1; ; failAt++ {
				fixture := newIntegrationFixture(t)
				steps := journey(fixture)
				options := fixture.options()
				for i := 0; i < stepIndex; i++ {
					if _, err := steps[i](options); err != nil {
						t.Fatalf("setup step %d: %v", i, err)
					}
				}
				calls := 0
				if failing == "GitHub" {
					fixture.gh.calls, fixture.gh.failAt = 0, failAt
				} else {
					options.run = failingIntegrationRunner{Runner: runner.New(), failAt: failAt, calls: &calls}
				}
				result, err := steps[stepIndex](options)
				reached := calls >= failAt || fixture.gh.calls >= failAt
				if !reached {
					break
				}
				if err == nil && result.Outcome == LandSuccess {
					t.Errorf("step %d: %s failure at call %d was swallowed: %+v", stepIndex, failing, failAt, result)
				}
			}
		}
	}
}
