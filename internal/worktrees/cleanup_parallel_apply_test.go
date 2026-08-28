package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// addRepositoryToFixture gives a fixture a second (third, ...) canonical clone
// under the same projects root and its own bare origin. Parallel apply is
// bounded by the repository, so every test below needs a fleet whose tasks do
// not all share one clone — the shape the founder's fleet actually has (86
// removals spread over 34 repositories).
func addRepositoryToFixture(t *testing.T, fixture *gitFixture, repository string) string {
	t.Helper()
	root := filepath.Dir(fixture.projectsRoot)
	remote := filepath.Join(root, repository+"-remote.git")
	gitTest(t, root, "init", "--bare", "--initial-branch=main", remote)
	canonical := filepath.Join(fixture.projectsRoot, "acme", repository)
	gitTest(t, root, "clone", remote, canonical)
	configureGitUser(t, canonical)
	if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("# "+repository+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, canonical, "add", "README.md")
	gitTest(t, canonical, "commit", "-m", "initial")
	gitTest(t, canonical, "push", "-u", "origin", "main")
	return canonical
}

// prepareMergedTaskInRepositories is prepareMergedTaskInFixture for a task that
// spans an explicit set of repositories, which is the only way to build the
// coordinated multi-repository task that makes lock ordering matter.
func prepareMergedTaskInRepositories(t *testing.T, fixture *gitFixture, task string, repositories ...string) ([]CreateResult, []string) {
	t.Helper()
	slugs := make([]string, 0, len(repositories))
	for _, repository := range repositories {
		slugs = append(slugs, "acme/"+repository)
	}
	created, err := Create(context.Background(), slugs, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    task, WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	heads := make([]string, 0, len(created))
	for _, result := range created {
		if err := os.WriteFile(filepath.Join(result.WorktreeDir, "feature.txt"), []byte(task+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitTest(t, result.WorktreeDir, "add", "feature.txt")
		gitTest(t, result.WorktreeDir, "commit", "-m", "feature")
		heads = append(heads, gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD"))
		gitTest(t, result.WorktreeDir, "push", "-u", "origin", result.Branch)
		canonical := filepath.Join(fixture.projectsRoot, "acme", filepath.Base(result.Repository))
		gitTest(t, canonical, "merge", "--no-ff", result.Branch, "-m", "merge feature")
		gitTest(t, canonical, "push", "origin", "main")
	}
	return created, heads
}

var (
	errSyntheticSlowTaskFailure = errors.New("synthetic failure in the slow task")
	errSyntheticFastTaskFailure = errors.New("synthetic failure in the fast task")
)

// overlapDeadline is generous on purpose. A test that must observe two things
// happening at once can only fail by waiting, and this machine routinely runs
// dozens of agent processes — a tight deadline would report a busy laptop as a
// serial apply. When apply really is concurrent the wait is instant, so the
// cost of generosity is paid only by a genuine regression.
const overlapDeadline = 30 * time.Second

// exclusionWindow is the opposite risk: a test that must observe two things
// *not* happening at once fails by not waiting long enough. Unserialised, the
// second task reaches the same seam within milliseconds, so a window this wide
// is far past what a broken implementation needs — it is sized for load, not
// for the mechanism.
const exclusionWindow = 3 * time.Second

var testMergedAt = time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)

// rendezvous blocks every arriving caller until `participants` of them are
// inside it at once, or until the deadline passes. A deadline that passes is
// the assertion: it means the phase under test never ran that many things
// concurrently.
type rendezvous struct {
	mu           sync.Mutex
	count        int
	participants int
	release      chan struct{}
	timeout      time.Duration
	// hold keeps every participant inside after the barrier opens, so a
	// late-comer that should not exist is still counted. Without it a ceiling
	// assertion races the very over-subscription it is looking for.
	hold     time.Duration
	timedOut bool
	peak     int
}

func newRendezvous(participants int, timeout time.Duration) *rendezvous {
	return &rendezvous{participants: participants, release: make(chan struct{}), timeout: timeout}
}

func (r *rendezvous) holding(hold time.Duration) *rendezvous {
	r.hold = hold
	return r
}

func (r *rendezvous) arrive() {
	r.mu.Lock()
	r.count++
	if r.count > r.peak {
		r.peak = r.count
	}
	reached := r.count >= r.participants
	if reached {
		select {
		case <-r.release:
		default:
			close(r.release)
		}
	}
	r.mu.Unlock()
	select {
	case <-r.release:
	case <-time.After(r.timeout):
		r.mu.Lock()
		r.timedOut = true
		r.mu.Unlock()
	}
	if r.hold > 0 {
		time.Sleep(r.hold)
	}
	r.mu.Lock()
	r.count--
	r.mu.Unlock()
}

func (r *rendezvous) reached() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.timedOut
}

func (r *rendezvous) highWater() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peak
}

// The apply phase is where a fleet sweep spends its wall clock: 262 candidates
// were inspected in about two minutes and the 86 removals that followed took
// ten, one at a time. Two tasks in two different canonical repositories share
// no Git writer, so nothing about Git requires them to wait for each other.
func TestCleanupAppliesDifferentRepositoriesConcurrently(t *testing.T) {
	fixture := newGitFixture(t)
	addRepositoryToFixture(t, fixture, "lib")
	_, appHeads := prepareMergedTaskInRepositories(t, fixture, "task-in-app", "app")
	_, libHeads := prepareMergedTaskInRepositories(t, fixture, "task-in-lib", "lib")
	installMergedPullRequestFixtures(t, append(appHeads, libHeads...), testMergedAt)

	// The owner directory is shared by every repository under it, and macOS's
	// capability guard freezes it around each sandboxed Git call. A sweep that
	// applies two of its repositories at once must give it back exactly as it
	// found it.
	owner := filepath.Join(fixture.projectsRoot, "acme")
	ownerMode := modeOfDirectory(t, owner)

	overlap := newRendezvous(2, overlapDeadline)
	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot:                 fixture.projectsRoot,
		Tasks:                        []string{"task-in-app", "task-in-lib"},
		Apply:                        true,
		Workers:                      4,
		Now:                          func() time.Time { return testMergedAt.Add(time.Hour) },
		beforeCleanupWorktreeRemoval: func(string) { overlap.arrive() },
	})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	for _, task := range []string{"task-in-app", "task-in-lib"} {
		if !cleanupApplied(outcome, task) {
			t.Fatalf("task %s was not applied: %#v", task, outcome.Results)
		}
	}
	if got := modeOfDirectory(t, owner); got != ownerMode {
		t.Fatalf("concurrent apply left the shared owner directory at %v, want its original %v", got, ownerMode)
	}
	if !overlap.reached() {
		t.Fatalf("apply never ran two repositories at once: the removal of one task waited %s alone "+
			"(high-water concurrency %d, want 2)", overlapDeadline, overlap.highWater())
	}

}

// Requirement six: whichever order tasks finish in, the report reads in walk
// order. Here the lexically first task is deliberately the slowest to fail, so
// a report assembled as results arrive would put the second task's diagnostic
// first.
func TestCleanupParallelApplyReportsInWalkOrderNotCompletionOrder(t *testing.T) {
	fixture := newGitFixture(t)
	addRepositoryToFixture(t, fixture, "lib")
	_, slowHeads := prepareMergedTaskInRepositories(t, fixture, "aaa-slow-failure", "app")
	_, fastHeads := prepareMergedTaskInRepositories(t, fixture, "zzz-fast-failure", "lib")
	installMergedPullRequestFixtures(t, append(slowHeads, fastHeads...), testMergedAt)

	reportDir := t.TempDir()
	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot,
		Tasks:        []string{"aaa-slow-failure", "zzz-fast-failure"},
		Apply:        true,
		Workers:      4,
		ReportDir:    reportDir,
		Now:          func() time.Time { return testMergedAt.Add(time.Hour) },
		afterCleanupWorktreeRemoval: func(worktree string) error {
			if strings.Contains(worktree, "aaa-slow-failure") {
				time.Sleep(750 * time.Millisecond)
				return errSyntheticSlowTaskFailure
			}
			return errSyntheticFastTaskFailure
		},
	})
	if err != nil {
		t.Fatalf("one failing task must not abort a sweep: %v", err)
	}
	var reported []string
	for _, diagnostic := range outcome.Diagnostics {
		reported = append(reported, diagnostic.Task)
	}
	want := []string{"aaa-slow-failure", "zzz-fast-failure"}
	if len(reported) != len(want) {
		t.Fatalf("want one diagnostic per failing task %v, got %v", want, reported)
	}
	for index := range want {
		if reported[index] != want[index] {
			t.Fatalf("diagnostics report completion order %v, want walk order %v", reported, want)
		}
	}
	// Results are the other half of the report and must not be reordered either.
	var tasks []string
	for _, result := range outcome.Results {
		tasks = append(tasks, result.Task)
	}
	for index := 1; index < len(tasks); index++ {
		if tasks[index-1] > tasks[index] {
			t.Fatalf("results left walk order: %v", tasks)
		}
	}
	contents, readErr := os.ReadFile(filepath.Join(reportDir, "cleanup.json"))
	if readErr != nil {
		t.Fatalf("read cleanup report: %v", readErr)
	}
	var report cleanupReport
	if err := json.Unmarshal(contents, &report); err != nil {
		t.Fatalf("decode cleanup report: %v", err)
	}
	if got, want := report.Tasks, []string{"aaa-slow-failure", "zzz-fast-failure"}; !slices.Equal(got, want) {
		t.Fatalf("report task selection = %v, want %v", got, want)
	}
	if len(report.Diagnostics) != len(want) || report.Diagnostics[0].Task != want[0] || report.Diagnostics[1].Task != want[1] {
		t.Fatalf("report diagnostics order = %#v, want %v", report.Diagnostics, want)
	}
}

// modeOfDirectory is the platform-neutral half of the owner-directory
// assertion above; only macOS mutates the mode, but the guarantee is not
// platform-specific.
func modeOfDirectory(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// Git allows one writer per clone: `worktree remove`, `update-ref -d` and the
// ref updates a push implies all mutate the same .git and fail on each other's
// lock files. Two tasks in one repository must therefore still take turns, and
// this is the only thing that keeps a repository with fourteen finished tasks
// — the founder's largest group — correct rather than merely fast.
func TestCleanupSerialisesApplyWithinOneRepository(t *testing.T) {
	fixture := newGitFixture(t)
	_, firstHeads := prepareMergedTaskInRepositories(t, fixture, "same-repo-one", "app")
	_, secondHeads := prepareMergedTaskInRepositories(t, fixture, "same-repo-two", "app")
	installMergedPullRequestFixtures(t, append(firstHeads, secondHeads...), testMergedAt)

	collision := newRendezvous(2, exclusionWindow)
	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot:                 fixture.projectsRoot,
		Tasks:                        []string{"same-repo-one", "same-repo-two"},
		Apply:                        true,
		Workers:                      4,
		Now:                          func() time.Time { return testMergedAt.Add(time.Hour) },
		beforeCleanupWorktreeRemoval: func(string) { collision.arrive() },
	})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	for _, task := range []string{"same-repo-one", "same-repo-two"} {
		if !cleanupApplied(outcome, task) {
			t.Fatalf("task %s was not applied: %#v", task, outcome.Results)
		}
	}
	if collision.reached() {
		t.Fatal("two tasks in one canonical clone were inside the removal window at once; " +
			"Git allows a single writer per clone and their ref updates will collide")
	}
}

// A coordinated task holds every repository it spans for its whole
// transaction. Two such tasks over the same pair of repositories are the shape
// that deadlocks if each takes its locks in its own order, and no timeout in
// the scheduler would recover it. The sorted acquisition order in
// planCleanupApply is what makes the cycle unconstructible.
func TestCleanupAppliesCoordinatedMultiRepositoryTasksWithoutDeadlock(t *testing.T) {
	fixture := newGitFixture(t)
	addRepositoryToFixture(t, fixture, "lib")
	_, firstHeads := prepareMergedTaskInRepositories(t, fixture, "coordinated-one", "app", "lib")
	_, secondHeads := prepareMergedTaskInRepositories(t, fixture, "coordinated-two", "app", "lib")
	installMergedPullRequestFixtures(t, append(firstHeads, secondHeads...), testMergedAt)

	type completion struct {
		outcome CleanupOutcome
		err     error
	}
	done := make(chan completion, 1)
	go func() {
		outcome, err := Cleanup(context.Background(), CleanupOptions{
			ProjectsRoot: fixture.projectsRoot,
			Tasks:        []string{"coordinated-one", "coordinated-two"},
			Apply:        true,
			Workers:      4,
			Now:          func() time.Time { return testMergedAt.Add(time.Hour) },
		})
		done <- completion{outcome: outcome, err: err}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("cleanup: %v", result.err)
		}
		for _, task := range []string{"coordinated-one", "coordinated-two"} {
			if !cleanupApplied(result.outcome, task) {
				t.Fatalf("task %s was not applied: %#v", task, result.outcome.Results)
			}
		}
	case <-time.After(90 * time.Second):
		t.Fatal("two coordinated tasks over the same two repositories deadlocked; " +
			"each holds one repository and waits for the other")
	}
}

// The lock order is a property of the plan, not of the acquisition loop, so it
// is asserted where it is decided.
func TestPlanCleanupApplyOrdersAndDedupesEachTaskRepositories(t *testing.T) {
	worktreesRoot := "/wb/worktrees"
	result := func(task, repository string) CleanupResult {
		return CleanupResult{ListResult: ListResult{
			Task: task, Repository: "acme/" + repository,
			CanonicalDir:  "/projects/acme/" + repository,
			WorktreesRoot: worktreesRoot,
			WorktreeDir:   worktreesRoot + "/" + task + "/acme/" + repository,
		}, Eligible: true}
	}
	outcome := CleanupOutcome{Results: []CleanupResult{
		result("spanning", "zebra"),
		result("spanning", "alpha"),
		result("spanning", "middle"),
		result("spanning", "alpha"),
	}}
	entries := planCleanupApply(outcome)
	if len(entries) != 1 {
		t.Fatalf("want one task, got %d", len(entries))
	}
	want := []string{"/projects/acme/alpha", "/projects/acme/middle", "/projects/acme/zebra"}
	got := entries[0].repositories
	if len(got) != len(want) {
		t.Fatalf("repositories %v, want the sorted distinct set %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("repositories %v are not in one global order; want %v", got, want)
		}
	}
}

// Concurrency against one account is not the same resource as concurrency
// against local clones. Eight simultaneous branch deletions is the burst shape
// GitHub's secondary rate limiter answers with a 403, which would strand
// transactions rather than speed the sweep up.
func TestCleanupBoundsConcurrentRemoteBranchDeletions(t *testing.T) {
	previous := maxConcurrentRemoteBranchDeletions
	maxConcurrentRemoteBranchDeletions = 1
	t.Cleanup(func() { maxConcurrentRemoteBranchDeletions = previous })

	fixture := newGitFixture(t)
	addRepositoryToFixture(t, fixture, "lib")
	_, appHeads := prepareMergedTaskInRepositories(t, fixture, "remote-in-app", "app")
	_, libHeads := prepareMergedTaskInRepositories(t, fixture, "remote-in-lib", "lib")
	installMergedPullRequestFixtures(t, append(appHeads, libHeads...), testMergedAt)

	inFlight := newRendezvous(2, exclusionWindow)
	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot:                        fixture.projectsRoot,
		AllMerged:                           true,
		Apply:                               true,
		DeleteRemote:                        true,
		Workers:                             8,
		Now:                                 func() time.Time { return testMergedAt.Add(time.Hour) },
		beforeCleanupNetworkBranchOperation: func(string) { inFlight.arrive() },
	})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	deleted := 0
	for _, result := range outcome.Results {
		if result.RemoteDeleted {
			deleted++
		}
	}
	if deleted != 2 {
		t.Fatalf("want both remote branches deleted, got %d: %#v", deleted, outcome.Results)
	}
	if inFlight.reached() {
		t.Fatalf("%d remote branch deletions ran at once against a bound of %d",
			inFlight.highWater(), maxConcurrentRemoteBranchDeletions)
	}
}

// --parallel is a ceiling, not a target. A sweep must hold at most that many
// task locks and that many worktrees open at once, whatever the fleet's shape,
// or an --all-merged run over a large fleet is back to retaining everything it
// has ever touched.
func TestCleanupNamedTasksNeverExceedsTheWorkerCeiling(t *testing.T) {
	fixture := newGitFixture(t)
	heads := []string{}
	for _, repository := range []string{"lib", "tool"} {
		addRepositoryToFixture(t, fixture, repository)
	}
	for _, repository := range []string{"app", "lib", "tool"} {
		_, repositoryHeads := prepareMergedTaskInRepositories(t, fixture, "ceiling-"+repository, repository)
		heads = append(heads, repositoryHeads...)
	}
	installMergedPullRequestFixtures(t, heads, testMergedAt)

	// The barrier opens as soon as the ceiling is reached, then holds everyone
	// inside long enough that a worker beyond the ceiling would be counted.
	const ceiling = 2
	inFlight := newRendezvous(ceiling, overlapDeadline).holding(exclusionWindow)
	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot:                 fixture.projectsRoot,
		Tasks:                        []string{"ceiling-app", "ceiling-lib", "ceiling-tool"},
		Apply:                        true,
		Workers:                      ceiling,
		Now:                          func() time.Time { return testMergedAt.Add(time.Hour) },
		beforeCleanupWorktreeRemoval: func(string) { inFlight.arrive() },
	})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	for _, repository := range []string{"app", "lib", "tool"} {
		if !cleanupApplied(outcome, "ceiling-"+repository) {
			t.Fatalf("task ceiling-%s was not applied: %#v", repository, outcome.Results)
		}
	}
	if !inFlight.reached() {
		t.Fatalf("apply never reached --parallel %d: it peaked at %d", ceiling, inFlight.highWater())
	}
	if inFlight.highWater() > ceiling {
		t.Fatalf("%d tasks were inside the removal window at once against --parallel %d",
			inFlight.highWater(), ceiling)
	}
}
