package prwatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// writeFakeGH puts a fake `gh` on PATH ahead of any real one, and resets the
// per-user observer cache directory, so every tick in this package's tests
// is hermetic and never reaches the real GitHub API.
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

// fakeGHCase renders one pull request's worth of endpoint responses as a
// shell if-chain fragment: identity (state/merged), its head's check runs,
// commit statuses, and an unprotected/no-rules branch policy. It never wires
// up `git/ref/heads/*` or `repos/.../compare/...` — prsnapshot.Observe has no
// reason to call either, since it enforces no target-branch freshness fence
// and no candidate-contains-target ancestry check (herdr-session-transport
// Plan Task 6 review round 2, point 3) — so a test whose fake gh has no
// fallback for those endpoints still passes only if that stays true.
func fakeGHCase(repository, pr, state string, merged bool, head, target, checkRunsJSON string) string {
	mergedJSON := "false"
	if merged {
		mergedJSON = "true"
	}
	return `
if [ "$1" = api ] && echo "$2" | grep -q 'repos/` + repository + `/pulls/` + pr + `$'; then
  echo '{"number":` + pr + `,"state":"` + state + `","draft":false,"merged":` + mergedJSON + `,"mergeable_state":"unknown","html_url":"https://example.invalid/` + pr + `","head":{"ref":"candidate","sha":"` + head + `","repo":{"full_name":"` + repository + `"}},"base":{"ref":"` + target + `","sha":""}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'repos/` + repository + `/commits/` + head + `/check-runs?per_page=100'; then
  echo '` + checkRunsJSON + `'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'repos/` + repository + `/commits/` + head + `/status?per_page=100'; then
  echo '{"total_count":0,"statuses":[]}'
  exit 0
fi
if [ "$1" = api ] && [ "$2" = 'repos/` + repository + `/branches/` + target + `' ]; then
  echo '{"protected":false,"protection":{}}'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'repos/` + repository + `/rules/branches/` + target + `'; then
  echo '[]'
  exit 0
fi
if [ "$1" = api ] && echo "$2" | grep -q 'repos/` + repository + `/actions/runs?head_sha=` + head + `'; then
  echo '{"total_count":0,"workflow_runs":[]}'
  exit 0
fi
`
}

func fakeGHScript(cases ...string) string {
	return "#!/bin/sh\n" + strings.Join(cases, "\n") + "\necho \"unexpected gh args: $*\" >&2\nexit 30\n"
}

const (
	passingChecks = `{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"success","app":{"id":1}}]}`
	failingChecks = `{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"failure","app":{"id":1}}]}`
	pendingChecks = `{"total_count":1,"check_runs":[{"name":"CI","status":"in_progress","conclusion":"","app":{"id":1}}]}`

	fakeHeadA = "1111111111111111111111111111111111111a"
	fakeHeadB = "2222222222222222222222222222222222222b"
)

func fixedBinding(task, claimID, repository string, pr int) worktrees.RegisteredPullRequestBinding {
	return worktrees.RegisteredPullRequestBinding{
		Task: task, ClaimID: claimID, Repository: repository, PullRequest: pr,
		URL: "https://github.com/" + repository + "/pull/" + strconv.Itoa(pr),
	}
}

func newFixedClockWatcher(at time.Time) *Watcher {
	w := NewWatcher()
	w.Now = func() time.Time { return at }
	return w
}

// TestWatcherEvaluateChecksPassedNeedsTwoConsecutiveIdenticalTicks pins the
// coordinator's two-observation rule: a single green tick is never Terminal,
// and only a second, identical (same Kind, same head) tick confirms it.
func TestWatcherEvaluateChecksPassedNeedsTwoConsecutiveIdenticalTicks(t *testing.T) {
	binding := fixedBinding("task-a", "claim-a", "acme/app", 1)
	w := NewWatcher()

	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "1", "open", false, fakeHeadA, "main", passingChecks)))
	first, err := w.Evaluate(context.Background(), binding)
	if err != nil {
		t.Fatalf("Evaluate (tick 1): %v", err)
	}
	if first.Kind != KindChecksPassed || first.Terminal {
		t.Fatalf("tick 1 = %+v, want checks-passed, not yet terminal", first)
	}

	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "1", "open", false, fakeHeadA, "main", passingChecks)))
	second, err := w.Evaluate(context.Background(), binding)
	if err != nil {
		t.Fatalf("Evaluate (tick 2): %v", err)
	}
	if second.Kind != KindChecksPassed || !second.Terminal {
		t.Fatalf("tick 2 = %+v, want checks-passed and terminal", second)
	}
}

// TestWatcherEvaluateChecksFailedNeedsTwoConsecutiveIdenticalTicksToo proves
// the same two-observation rule applies to a failing verdict, not only a
// passing one.
func TestWatcherEvaluateChecksFailedNeedsTwoConsecutiveIdenticalTicksToo(t *testing.T) {
	binding := fixedBinding("task-b", "claim-b", "acme/app", 2)
	w := NewWatcher()

	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "2", "open", false, fakeHeadA, "main", failingChecks)))
	first, err := w.Evaluate(context.Background(), binding)
	if err != nil {
		t.Fatalf("Evaluate (tick 1): %v", err)
	}
	if first.Kind != KindChecksFailed || first.Terminal {
		t.Fatalf("tick 1 = %+v, want checks-failed, not yet terminal", first)
	}

	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "2", "open", false, fakeHeadA, "main", failingChecks)))
	second, err := w.Evaluate(context.Background(), binding)
	if err != nil {
		t.Fatalf("Evaluate (tick 2): %v", err)
	}
	if second.Kind != KindChecksFailed || !second.Terminal {
		t.Fatalf("tick 2 = %+v, want checks-failed and terminal", second)
	}
	if second.Reason == "" {
		t.Fatal("terminal failed outcome carries no reason")
	}
}

// TestWatcherEvaluateChecksPendingIsNeverTerminal proves a still-running
// check stays checks-pending, and stays non-terminal, tick after tick.
func TestWatcherEvaluateChecksPendingIsNeverTerminal(t *testing.T) {
	binding := fixedBinding("task-c", "claim-c", "acme/app", 3)
	w := NewWatcher()
	for tick := 0; tick < 2; tick++ {
		writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "3", "open", false, fakeHeadA, "main", pendingChecks)))
		outcome, err := w.Evaluate(context.Background(), binding)
		if err != nil {
			t.Fatalf("Evaluate (tick %d): %v", tick, err)
		}
		if outcome.Kind != KindChecksPending || outcome.Terminal {
			t.Fatalf("tick %d = %+v, want checks-pending, never terminal", tick, outcome)
		}
	}
}

// TestWatcherEvaluateMergedIsTerminalImmediatelyAndNeverFailed pins the
// coordinator's blocking fix: a merged pull request is Terminal on its very
// first observation, and it must never be reported as checks-failed even
// though its underlying check data (deliberately, in this test) is red.
func TestWatcherEvaluateMergedIsTerminalImmediatelyAndNeverFailed(t *testing.T) {
	binding := fixedBinding("task-d", "claim-d", "acme/app", 4)
	w := NewWatcher()
	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "4", "closed", true, fakeHeadA, "main", failingChecks)))
	outcome, err := w.Evaluate(context.Background(), binding)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if outcome.Kind != KindMerged {
		t.Fatalf("Kind = %q, want merged (never checks-failed, even with a red underlying check)", outcome.Kind)
	}
	if !outcome.Terminal {
		t.Fatal("a merged outcome must be terminal on its first observation")
	}
}

// TestWatcherEvaluateClosedWithoutMergeIsTerminalImmediatelyAndNeverFailed
// is TestWatcherEvaluateMergedIsTerminalImmediatelyAndNeverFailed's sibling
// for a closed-not-merged pull request.
func TestWatcherEvaluateClosedWithoutMergeIsTerminalImmediatelyAndNeverFailed(t *testing.T) {
	binding := fixedBinding("task-e", "claim-e", "acme/app", 5)
	w := NewWatcher()
	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "5", "closed", false, fakeHeadA, "main", failingChecks)))
	outcome, err := w.Evaluate(context.Background(), binding)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if outcome.Kind != KindClosed {
		t.Fatalf("Kind = %q, want closed (never checks-failed, even with a red underlying check)", outcome.Kind)
	}
	if !outcome.Terminal {
		t.Fatal("a closed outcome must be terminal on its first observation")
	}
}

// TestWatcherEvaluateReportsHeadDriftThenConfirmsOnTheNewHead proves a head
// that moves between ticks is reported as head-drift rather than silently
// carrying an unconfirmed pass/fail verdict on the new commit forward, and
// that the Watcher's memory of the real classification underneath the drift
// still lets the very next matching tick confirm normally.
func TestWatcherEvaluateReportsHeadDriftThenConfirmsOnTheNewHead(t *testing.T) {
	binding := fixedBinding("task-f", "claim-f", "acme/app", 6)
	w := NewWatcher()

	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "6", "open", false, fakeHeadA, "main", pendingChecks)))
	tick1, err := w.Evaluate(context.Background(), binding)
	if err != nil {
		t.Fatalf("Evaluate (tick 1): %v", err)
	}
	if tick1.Kind != KindChecksPending || tick1.Head != fakeHeadA {
		t.Fatalf("tick 1 = %+v, want checks-pending on head A", tick1)
	}

	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "6", "open", false, fakeHeadB, "main", passingChecks)))
	tick2, err := w.Evaluate(context.Background(), binding)
	if err != nil {
		t.Fatalf("Evaluate (tick 2): %v", err)
	}
	if tick2.Kind != KindHeadDrift || tick2.Terminal || tick2.Head != fakeHeadB {
		t.Fatalf("tick 2 = %+v, want head-drift onto head B, never terminal", tick2)
	}
	if !strings.Contains(tick2.Reason, fakeHeadA) || !strings.Contains(tick2.Reason, fakeHeadB) {
		t.Fatalf("tick 2 reason = %q, want it to name both heads", tick2.Reason)
	}

	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "6", "open", false, fakeHeadB, "main", passingChecks)))
	tick3, err := w.Evaluate(context.Background(), binding)
	if err != nil {
		t.Fatalf("Evaluate (tick 3): %v", err)
	}
	if tick3.Kind != KindChecksPassed || !tick3.Terminal {
		t.Fatalf("tick 3 = %+v, want checks-passed and terminal: the real classification under the drift tick must still count", tick3)
	}
}

// TestWatcherEvaluateUnavailableNeverBreaksAStreak proves an operational
// GitHub read failure is reported as KindUnavailable, is never terminal, and
// — critically — never overwrites the Watcher's memory of the last real
// observation, so a transient blip between two good ticks does not restart
// the two-observation count.
func TestWatcherEvaluateUnavailableNeverBreaksAStreak(t *testing.T) {
	binding := fixedBinding("task-g", "claim-g", "acme/app", 7)
	w := NewWatcher()

	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "7", "open", false, fakeHeadA, "main", passingChecks)))
	tick1, err := w.Evaluate(context.Background(), binding)
	if err != nil {
		t.Fatalf("Evaluate (tick 1): %v", err)
	}
	if tick1.Kind != KindChecksPassed || tick1.Terminal {
		t.Fatalf("tick 1 = %+v, want checks-passed, not yet terminal", tick1)
	}

	writeFakeGH(t, fakeGHScript()) // no case at all: every call fails
	tick2, err := w.Evaluate(context.Background(), binding)
	if err != nil {
		t.Fatalf("Evaluate (tick 2): %v", err)
	}
	if tick2.Kind != KindUnavailable || tick2.Terminal {
		t.Fatalf("tick 2 = %+v, want unavailable, never terminal", tick2)
	}
	if tick2.Reason == "" {
		t.Fatal("unavailable outcome carries no reason")
	}

	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "7", "open", false, fakeHeadA, "main", passingChecks)))
	tick3, err := w.Evaluate(context.Background(), binding)
	if err != nil {
		t.Fatalf("Evaluate (tick 3): %v", err)
	}
	if tick3.Kind != KindChecksPassed || !tick3.Terminal {
		t.Fatalf("tick 3 = %+v, want checks-passed and terminal: the tick-2 blip must not have reset the streak", tick3)
	}
}

// TestEvaluateNeverConsultsTargetFreshnessOrAdvancement is a structural proof
// of the coordinator's point 3: the watcher never asks GitHub for the
// target branch's current head or for candidate-contains-target ancestry —
// fakeGHCase wires up no handler for either endpoint, so if Evaluate (via
// prsnapshot.Observe) ever called one, this test would fail on the fake's
// "unexpected gh args" fallback instead of passing cleanly. A target
// branch simply advancing past this pull request's base, or having no
// strict freshness fence, is therefore structurally not this watcher's
// concern: it never merges, so neither condition can ever surface as a CI
// failure here.
func TestEvaluateNeverConsultsTargetFreshnessOrAdvancement(t *testing.T) {
	binding := fixedBinding("task-h", "claim-h", "acme/app", 8)
	w := NewWatcher()
	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "8", "open", false, fakeHeadA, "main", passingChecks)))
	outcome, err := w.Evaluate(context.Background(), binding)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if outcome.Kind != KindChecksPassed {
		t.Fatalf("Kind = %q, want checks-passed", outcome.Kind)
	}
}

// TestWatcherEvaluateUsesTheInjectedClock proves EvaluatedAt comes from
// Watcher.Now, not a real timer — no test in this package depends on wall
// clock time.
func TestWatcherEvaluateUsesTheInjectedClock(t *testing.T) {
	binding := fixedBinding("task-i", "claim-i", "acme/app", 9)
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	w := newFixedClockWatcher(fixed)
	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "9", "open", false, fakeHeadA, "main", passingChecks)))
	outcome, err := w.Evaluate(context.Background(), binding)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !outcome.EvaluatedAt.Equal(fixed) {
		t.Fatalf("EvaluatedAt = %v, want the injected clock's %v", outcome.EvaluatedAt, fixed)
	}
}

// TestEvaluateRejectsBindingMissingIdentifiers proves Evaluate refuses a
// binding with no repository or pull-request number before making any
// GitHub call at all.
func TestEvaluateRejectsBindingMissingIdentifiers(t *testing.T) {
	t.Parallel()
	w := NewWatcher()
	for _, binding := range []worktrees.RegisteredPullRequestBinding{
		{Task: "task-j", Repository: "", PullRequest: 1},
		{Task: "task-k", Repository: "acme/app", PullRequest: 0},
	} {
		if _, err := w.Evaluate(context.Background(), binding); err == nil {
			t.Fatalf("Evaluate(%+v) = nil error, want a refusal", binding)
		}
	}
}

// TestTickReturnsErrorWhenBindingsCannotBeListed proves Tick surfaces a
// binding-listing failure rather than reporting an empty, misleadingly clean
// result set.
func TestTickReturnsErrorWhenBindingsCannotBeListed(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := NewWatcher()
	if _, err := w.Tick(context.Background(), filepath.Join(blocker, "projects")); err == nil {
		t.Fatal("Tick = nil error, want the unresolvable projects root refused")
	}
}

// TestTickEvaluatesOnlyRegisteredBindingsAcrossRepositories is the
// end-to-end proof behind daemon-watches-only-registered-prs: two active
// Work Log claims exist across two repositories, only one of them ever
// records a pull-request binding via worktrees.RecordClaimPullRequestBinding,
// and Tick must discover and evaluate exactly that one — the other
// repository's `gh` endpoints are never wired into the fake at all, so any
// attempt to read it falls through to the fallback rather than silently
// succeeding, proving there is no fleet-wide scan.
func TestTickEvaluatesOnlyRegisteredBindingsAcrossRepositories(t *testing.T) {
	projectsRoot := newTwoRepoFixture(t)
	ctx := context.Background()

	created, err := worktrees.Create(ctx, []string{"acme/app"}, worktrees.CreateOptions{
		ProjectsRoot: projectsRoot, Operation: "watch-registered", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create registered claim: %v", err)
	}
	registered := created[0]

	if _, err := worktrees.Create(ctx, []string{"acme/other"}, worktrees.CreateOptions{
		ProjectsRoot: projectsRoot, Operation: "watch-unregistered", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	}); err != nil {
		t.Fatalf("create unregistered claim: %v", err)
	}

	task, claimID, err := worktrees.RecordClaimPullRequestBinding(projectsRoot, registered.WorktreeDir, worktrees.ClaimPullRequestBinding{
		Repository: "acme/app", PullRequest: 21, URL: "https://github.com/acme/app/pull/21",
	})
	if err != nil {
		t.Fatalf("RecordClaimPullRequestBinding: %v", err)
	}

	// Only acme/app is wired into the fake: acme/other has no case at all, so
	// any read against it falls through to the fallback "unexpected gh args"
	// exit — which must never happen, because acme/other never got a binding.
	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "21", "open", false, fakeHeadA, "main", passingChecks)))

	w := NewWatcher()
	results, err := w.Tick(ctx, projectsRoot)
	if err != nil {
		t.Fatalf("Tick: %v", err)
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
	if result.Outcome.Kind != KindChecksPassed {
		t.Fatalf("result.Outcome = %+v, want checks-passed", result.Outcome)
	}
}

// TestTickReportsUnavailableForOneBindingWithoutStoppingTheRest proves one
// unreachable registered pull request never stops Tick from evaluating the
// rest: it still returns a PollResult per binding, with Kind=unavailable (and
// no Err) contained to the one that hit it.
func TestTickReportsUnavailableForOneBindingWithoutStoppingTheRest(t *testing.T) {
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
	writeFakeGH(t, fakeGHScript(fakeGHCase("acme/app", "1", "open", false, fakeHeadA, "main", passingChecks)))

	w := NewWatcher()
	results, err := w.Tick(ctx, projectsRoot)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v, want both registered bindings represented", results)
	}
	byTask := map[string]PollResult{}
	for _, result := range results {
		byTask[result.Binding.Task] = result
	}
	ok, present := byTask[taskOK]
	if !present || ok.Err != nil || ok.Binding.ClaimID != claimOK || ok.Outcome.Kind != KindChecksPassed {
		t.Fatalf("ok result = %+v, present=%t", ok, present)
	}
	broken, present := byTask[taskBroken]
	if !present || broken.Err != nil || broken.Binding.ClaimID != claimBroken || broken.Outcome.Kind != KindUnavailable {
		t.Fatalf("broken result = %+v, present=%t, want Kind=unavailable and no Err", broken, present)
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
