package prwatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// writeFakeGH puts a fake `gh` on PATH ahead of any real one, so no test in
// this package ever reaches the real GitHub API — per herdr-session-transport's
// brief, all GitHub access goes through the existing gh-api helper and seams
// orchestrate.ReadPullRequest/WaitForPullRequestChecks already use.
func writeFakeGH(t *testing.T, script string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// fakeGHCase renders one repository's worth of endpoint responses as a shell
// if-chain fragment, matched by the raw REST endpoint orchestrate's
// `gh api` calls use. It covers exactly the reads Evaluate's call into
// orchestrate.WaitForPullRequestChecks performs: pull-request identity
// (asked more than once), the exact target head, candidate-contains-target
// ancestry, branch-protection policy (answered 403 so a caller under
// AllowUnfenced treats it as an authoritatively unavailable policy rather
// than fetching real branch-protection JSON), check runs, Actions runs, and
// commit statuses.
func fakeGHCase(repository, pr, head, target, targetHeadSHA, checkRunsJSON string) string {
	return `
if [ "$1" = api ] && echo "$2" | grep -q 'repos/` + repository + `/pulls/` + pr + `'; then
  echo '{"number":` + pr + `,"state":"open","draft":false,"head":{"ref":"candidate","sha":"` + head + `","repo":{"full_name":"` + repository + `"}},"base":{"ref":"` + target + `","sha":""}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'repos/` + repository + `/git/ref/heads/` + target + `'; then
  echo '{"object":{"sha":"` + targetHeadSHA + `"}}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/` + repository + `/branches/` + target + `' ]; then
  echo 'gh: Upgrade to access branch protection (HTTP 403)' >&2
  exit 1
fi
if [ "$1" = api ] && echo "$2" | grep -q 'repos/` + repository + `/compare/` + targetHeadSHA + `...` + head + `'; then
  echo '{"status":"ahead","base_commit":{"sha":"` + targetHeadSHA + `"},"merge_base_commit":{"sha":"` + targetHeadSHA + `"}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'repos/` + repository + `/commits/` + head + `/check-runs?per_page=100'; then
  echo '` + checkRunsJSON + `'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'repos/` + repository + `/actions/runs?head_sha=` + head + `'; then
  echo '{"total_count":0,"workflow_runs":[]}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'repos/` + repository + `/commits/` + head + `/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
`
}

func fakeGHScript(cases ...string) string {
	return "#!/bin/sh\n" + strings.Join(cases, "\n") + "\necho \"unexpected gh args: $*\" >&2\nexit 30\n"
}

const (
	fakeHeadA       = "1111111111111111111111111111111111111a"
	fakeTargetHeadA = "2222222222222222222222222222222222222b"
	fakeHeadB       = "3333333333333333333333333333333333333c"
	fakeTargetHeadB = "4444444444444444444444444444444444444d"
)

// TestEvaluatePassesWithNoApplicableChecksUnderAllowUnfenced proves Evaluate
// reports orchestrate's own "passed" verdict unchanged when a registered pull
// request's checks and required-check policy are both empty under an
// explicit AllowUnfenced — the same receipt `wb ci wait --allow-unfenced`
// would produce, never a reimplemented interpretation.
func TestEvaluatePassesWithNoApplicableChecksUnderAllowUnfenced(t *testing.T) {
	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "3", fakeHeadA, "main", fakeTargetHeadA, `{"total_count":0,"check_runs":[]}`)))
	binding := worktrees.RegisteredPullRequestBinding{
		Task: "task-a", ClaimID: "claim-a", Repository: "acme/app", PullRequest: 3,
		URL: "https://github.com/acme/app/pull/3", RecordedAt: time.Now().UTC(),
	}
	outcome, err := Evaluate(context.Background(), binding, EvaluateOptions{
		Slice: 5 * time.Second, CheckPollInterval: 50 * time.Millisecond,
		StableRereadDelay: time.Millisecond, AllowUnfenced: true,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if outcome.Status != orchestrate.PullRequestWaitPassed {
		t.Fatalf("outcome = %+v, want passed", outcome)
	}
	if outcome.Task != "task-a" || outcome.ClaimID != "claim-a" || outcome.Repository != "acme/app" ||
		outcome.PullRequest != 3 || outcome.URL != binding.URL {
		t.Fatalf("outcome identity = %+v, want it copied from the binding", outcome)
	}
	if outcome.Head != fakeHeadA || outcome.Target != "main" {
		t.Fatalf("outcome head/target = %q/%q, want the pull request's own current identity", outcome.Head, outcome.Target)
	}
	if outcome.EvaluatedAt.IsZero() {
		t.Fatal("outcome.EvaluatedAt is zero")
	}
}

// TestEvaluateReturnsFailedWhenAGitHubCheckFails proves a failing observed
// check reaches Outcome.Status as "failed" — orchestrate's own terminal
// failure verdict, reported without a second interpretation layer — and that
// Evaluate itself returns no Go error for a plain failed verdict.
func TestEvaluateReturnsFailedWhenAGitHubCheckFails(t *testing.T) {
	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "9", fakeHeadA, "main", fakeTargetHeadA,
		`{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"failure","app":{"id":1}}]}`)))
	binding := worktrees.RegisteredPullRequestBinding{
		Task: "task-b", ClaimID: "claim-b", Repository: "acme/app", PullRequest: 9,
		URL: "https://github.com/acme/app/pull/9",
	}
	outcome, err := Evaluate(context.Background(), binding, EvaluateOptions{
		Slice: 5 * time.Second, CheckPollInterval: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if outcome.Status != orchestrate.PullRequestWaitFailed {
		t.Fatalf("outcome = %+v, want failed", outcome)
	}
	if outcome.Reason == "" {
		t.Fatal("failed outcome carries no reason")
	}
}

// TestEvaluateReturnsPendingWhenChecksHaveNotSettled proves a still-running
// check keeps the outcome "pending" once the bounded evaluation slice
// expires, exactly as a bounded `wb ci wait` slice would report it — the
// watcher's caller re-evaluates a pending outcome on its own next poll rather
// than this call blocking indefinitely.
func TestEvaluateReturnsPendingWhenChecksHaveNotSettled(t *testing.T) {
	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "11", fakeHeadA, "main", fakeTargetHeadA,
		`{"total_count":1,"check_runs":[{"name":"CI","status":"in_progress","conclusion":"","app":{"id":1}}]}`)))
	binding := worktrees.RegisteredPullRequestBinding{
		Task: "task-c", ClaimID: "claim-c", Repository: "acme/app", PullRequest: 11,
		URL: "https://github.com/acme/app/pull/11",
	}
	outcome, err := Evaluate(context.Background(), binding, EvaluateOptions{
		Slice: 300 * time.Millisecond, CheckPollInterval: 50 * time.Millisecond, AllowUnfenced: true,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if outcome.Status != orchestrate.PullRequestWaitPending {
		t.Fatalf("outcome = %+v, want pending", outcome)
	}
}

// TestEvaluateRejectsBindingMissingIdentifiers proves Evaluate refuses a
// binding with no repository or pull-request number before making any
// GitHub call at all — there is no PATH override in this test, so a call
// through to a real `gh` would fail loudly rather than silently succeed.
func TestEvaluateRejectsBindingMissingIdentifiers(t *testing.T) {
	t.Parallel()
	for _, binding := range []worktrees.RegisteredPullRequestBinding{
		{Task: "task-d", Repository: "", PullRequest: 1},
		{Task: "task-e", Repository: "acme/app", PullRequest: 0},
	} {
		if _, err := Evaluate(context.Background(), binding, EvaluateOptions{}); err == nil {
			t.Fatalf("Evaluate(%+v) = nil error, want a refusal", binding)
		}
	}
}

// TestEvaluateReturnsErrorWhenPullRequestReadFails proves a GitHub read
// failure surfaces as a Go error naming the task and pull request, not a
// silently empty Outcome.
func TestEvaluateReturnsErrorWhenPullRequestReadFails(t *testing.T) {
	writeFakeGH(t, fakeGHScript()) // no case at all: every call falls through
	binding := worktrees.RegisteredPullRequestBinding{
		Task: "task-f", ClaimID: "claim-f", Repository: "acme/app", PullRequest: 5,
	}
	if _, err := Evaluate(context.Background(), binding, EvaluateOptions{}); err == nil {
		t.Fatal("Evaluate = nil error, want the unreadable pull request to surface")
	} else if !strings.Contains(err.Error(), "task-f") {
		t.Fatalf("Evaluate error = %v, want it to name the task", err)
	}
}

// TestEvaluateReturnsErrorWhenSliceExceedsForegroundCeiling proves Evaluate
// surfaces orchestrate's own option-validation error unchanged, rather than
// silently clamping an out-of-range EvaluateOptions.Slice.
func TestEvaluateReturnsErrorWhenSliceExceedsForegroundCeiling(t *testing.T) {
	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "6", fakeHeadA, "main", fakeTargetHeadA, `{"total_count":0,"check_runs":[]}`)))
	binding := worktrees.RegisteredPullRequestBinding{
		Task: "task-g", ClaimID: "claim-g", Repository: "acme/app", PullRequest: 6,
	}
	_, err := Evaluate(context.Background(), binding, EvaluateOptions{
		Slice: orchestrate.MaxForegroundCheckWaitSlice + time.Minute,
	})
	if err == nil {
		t.Fatal("Evaluate = nil error, want the oversized slice refused")
	}
}

// TestPollReturnsErrorWhenBindingsCannotBeListed proves Poll surfaces a
// binding-listing failure rather than reporting an empty, misleadingly clean
// result set.
func TestPollReturnsErrorWhenBindingsCannotBeListed(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A regular file where a directory component is expected makes
	// wbhome.Resolve's path resolution fail outright.
	if _, err := Poll(context.Background(), filepath.Join(blocker, "projects"), EvaluateOptions{}); err == nil {
		t.Fatal("Poll = nil error, want the unresolvable projects root refused")
	}
}

// TestPollEvaluatesOnlyRegisteredBindingsAcrossRepositories is the
// end-to-end proof behind daemon-watches-only-registered-prs: two active
// Work Log claims exist across two repositories, only one of them ever
// records a pull-request binding via worktrees.RecordClaimPullRequestBinding,
// and Poll must discover and evaluate exactly that one — the other
// repository's `gh` endpoints are never wired into the fake at all, so any
// attempt to read it fails the fake script's fallback rather than silently
// succeeding, proving there is no fleet-wide scan.
func TestPollEvaluatesOnlyRegisteredBindingsAcrossRepositories(t *testing.T) {
	projectsRoot := newTwoRepoFixture(t)
	ctx := context.Background()

	created, err := worktrees.Create(ctx, []string{"acme/app"}, worktrees.CreateOptions{
		ProjectsRoot: projectsRoot, Operation: "watch-registered", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create registered claim: %v", err)
	}
	registered := created[0]

	createdOther, err := worktrees.Create(ctx, []string{"acme/other"}, worktrees.CreateOptions{
		ProjectsRoot: projectsRoot, Operation: "watch-unregistered", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create unregistered claim: %v", err)
	}
	unregistered := createdOther[0]
	_ = unregistered

	task, claimID, err := worktrees.RecordClaimPullRequestBinding(projectsRoot, registered.WorktreeDir, worktrees.ClaimPullRequestBinding{
		Repository: "acme/app", PullRequest: 21, URL: "https://github.com/acme/app/pull/21",
	})
	if err != nil {
		t.Fatalf("RecordClaimPullRequestBinding: %v", err)
	}

	// Only acme/app is wired into the fake: acme/other has no case at all, so
	// any read against it falls through to the fallback "unexpected gh args"
	// exit — which must never happen, because acme/other never got a binding.
	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "21", fakeHeadA, "main", fakeTargetHeadA, `{"total_count":0,"check_runs":[]}`)))

	results, err := Poll(ctx, projectsRoot, EvaluateOptions{
		Slice: 5 * time.Second, CheckPollInterval: 50 * time.Millisecond,
		StableRereadDelay: time.Millisecond, AllowUnfenced: true,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %#v, want exactly the one registered binding (never the unregistered acme/other claim)", results)
	}
	result := results[0]
	if result.Err != nil {
		t.Fatalf("result.Err = %v, want the registered binding to evaluate cleanly", result.Err)
	}
	if result.Binding.Task != task || result.Binding.ClaimID != claimID {
		t.Fatalf("result.Binding = %+v, want task=%s claim=%s", result.Binding, task, claimID)
	}
	if result.Outcome.Status != orchestrate.PullRequestWaitPassed {
		t.Fatalf("result.Outcome = %+v, want passed", result.Outcome)
	}
}

// TestPollContinuesPastOneBindingsEvaluateError proves one unreachable
// registered pull request never stops Poll from evaluating the rest: it
// still returns a PollResult per binding, with the error contained to the
// one that hit it.
func TestPollContinuesPastOneBindingsEvaluateError(t *testing.T) {
	projectsRoot := newTwoRepoFixture(t)
	ctx := context.Background()

	createdOK, err := worktrees.Create(ctx, []string{"acme/app"}, worktrees.CreateOptions{
		ProjectsRoot: projectsRoot, Operation: "watch-ok", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create first claim: %v", err)
	}
	createdBroken, err := worktrees.Create(ctx, []string{"acme/other"}, worktrees.CreateOptions{
		ProjectsRoot: projectsRoot, Operation: "watch-broken", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create second claim: %v", err)
	}

	taskOK, claimOK, err := worktrees.RecordClaimPullRequestBinding(projectsRoot, createdOK[0].WorktreeDir, worktrees.ClaimPullRequestBinding{
		Repository: "acme/app", PullRequest: 1, URL: "https://github.com/acme/app/pull/1",
	})
	if err != nil {
		t.Fatalf("RecordClaimPullRequestBinding (ok): %v", err)
	}
	taskBroken, claimBroken, err := worktrees.RecordClaimPullRequestBinding(projectsRoot, createdBroken[0].WorktreeDir, worktrees.ClaimPullRequestBinding{
		Repository: "acme/other", PullRequest: 2, URL: "https://github.com/acme/other/pull/2",
	})
	if err != nil {
		t.Fatalf("RecordClaimPullRequestBinding (broken): %v", err)
	}

	// acme/app is wired into the fake and evaluates cleanly; acme/other has
	// no case at all, so its pull-request read fails through the fallback.
	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "1", fakeHeadA, "main", fakeTargetHeadA, `{"total_count":0,"check_runs":[]}`)))

	results, err := Poll(ctx, projectsRoot, EvaluateOptions{
		Slice: 5 * time.Second, CheckPollInterval: 50 * time.Millisecond,
		StableRereadDelay: time.Millisecond, AllowUnfenced: true,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v, want both registered bindings represented", results)
	}
	byTask := map[string]PollResult{}
	for _, result := range results {
		byTask[result.Binding.Task] = result
	}
	ok, present := byTask[taskOK]
	if !present || ok.Err != nil || ok.Binding.ClaimID != claimOK || ok.Outcome.Status != orchestrate.PullRequestWaitPassed {
		t.Fatalf("ok result = %+v, present=%t", ok, present)
	}
	broken, present := byTask[taskBroken]
	if !present || broken.Err == nil || broken.Binding.ClaimID != claimBroken {
		t.Fatalf("broken result = %+v, present=%t, want a populated Err and the binding preserved", broken, present)
	}
}

// newTwoRepoFixture establishes a hermetic WB projects root with two local
// bare-remote-backed repositories (acme/app, acme/other), the minimum
// worktrees.Create needs to record a real active Work Log claim per
// repository. No network access occurs: both remotes are local bare
// repositories under the test's own temp directory.
func newTwoRepoFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	projectsRoot := filepath.Join(root, "projects")
	t.Setenv(wbhome.EnvOverride, projectsRoot)
	t.Setenv(wbhome.EnvMigrationCompat, "")

	for _, repository := range []string{"acme/app", "acme/other"} {
		remote := filepath.Join(root, strings.ReplaceAll(repository, "/", "-")+".git")
		runFixtureGit(t, root, "init", "--bare", "--initial-branch=main", remote)
		canonical := filepath.Join(projectsRoot, repository)
		if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
			t.Fatal(err)
		}
		runFixtureGit(t, root, "clone", remote, canonical)
		runFixtureGit(t, canonical, "config", "user.email", "wb-test@example.com")
		runFixtureGit(t, canonical, "config", "user.name", "wb-test")
		if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("# "+repository+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runFixtureGit(t, canonical, "add", "README.md")
		runFixtureGit(t, canonical, "commit", "-m", "initial")
		runFixtureGit(t, canonical, "push", "-u", "origin", "main")
	}

	resolved, err := filepath.EvalSymlinks(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(wbhome.EnvOverride, resolved)
	return resolved
}

func runFixtureGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
	}
}
