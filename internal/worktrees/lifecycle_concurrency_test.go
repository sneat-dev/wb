package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// requireUnprivilegedTaskWalkTest guards the tests below, which express a
// concurrent retirement through directory permissions that root ignores.
func requireUnprivilegedTaskWalkTest(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permissions this test depends on")
	}
}

// A fleet with more than one agent on it retires tasks while a sweep is walking
// them, so the walk must cost that one task and never the sweep. Before this,
// a single task directory that went unreadable between the parent listing and
// its own read returned an error from listLayout, which discarded every other
// task already verified in that run — the founder saw a fourteen-minute
// --all-merged sweep thrown away by one directory a concurrent cleanup had
// legitimately removed. `wb worktree orphans` already refuses to let one
// unreadable directory hide the fleet; this is the same guarantee for the
// inventory that cleanup itself selects from.
func TestListSurvivesATaskDirectoryItCannotRead(t *testing.T) {
	requireUnprivilegedTaskWalkTest(t)
	fixture := newGitFixture(t)
	if _, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "survivor", WorkLog: WorkLogOptions{Model: "unknown"},
	}); err != nil {
		t.Fatal(err)
	}

	// 0o111 leaves the lock stat able to resolve a path inside the directory
	// and fails only the directory read, which is the exact branch a task
	// retired mid-walk trips.
	unreadable := filepath.Join(fixture.home, "worktrees", "retired-mid-walk")
	if err := os.MkdirAll(unreadable, 0o111); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o755) })

	outcome, err := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot})
	if err != nil {
		t.Fatalf("one unreadable task directory aborted the whole sweep: %v", err)
	}
	if !hasTaskResult(outcome, "survivor") {
		t.Fatalf("an unreadable sibling hid a healthy task: %#v", outcome.Results)
	}
	if !hasDiagnosticFor(outcome, "retired-mid-walk") {
		t.Fatalf("an unreadable task must be reported, not silently dropped: %#v", outcome.Diagnostics)
	}
}

// The lock stat runs before the directory read and had its own fatal, so it
// needs its own regression: a task whose permissions defeat even that stat
// must still cost only itself.
func TestListSurvivesATaskWhoseLockCannotBeInspected(t *testing.T) {
	requireUnprivilegedTaskWalkTest(t)
	fixture := newGitFixture(t)
	if _, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "survivor", WorkLog: WorkLogOptions{Model: "unknown"},
	}); err != nil {
		t.Fatal(err)
	}

	sealed := filepath.Join(fixture.home, "worktrees", "sealed-mid-walk")
	if err := os.MkdirAll(sealed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o755) })

	outcome, err := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot})
	if err != nil {
		t.Fatalf("one unstattable task lock aborted the whole sweep: %v", err)
	}
	if !hasTaskResult(outcome, "survivor") {
		t.Fatalf("a sealed sibling hid a healthy task: %#v", outcome.Results)
	}
	if !hasDiagnosticFor(outcome, "sealed-mid-walk") {
		t.Fatalf("a sealed task must be reported, not silently dropped: %#v", outcome.Diagnostics)
	}
}

// A task that is simply gone is the state cleanup converges on, so it is
// success rather than a diagnostic and must stay silent.
func TestVanishedDuringWalkRecognisesOnlyAMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never-created")
	if _, err := os.ReadDir(missing); !vanishedDuringWalk(err) {
		t.Fatalf("a removed task directory must read as vanished, got %v", err)
	}
	readable := t.TempDir()
	if _, err := os.ReadDir(readable); vanishedDuringWalk(err) {
		t.Fatalf("a readable directory must not read as vanished, got %v", err)
	}
}

func hasTaskResult(outcome ListOutcome, task string) bool {
	for _, result := range outcome.Results {
		if result.Task == task {
			return true
		}
	}
	return false
}

func hasDiagnosticFor(outcome ListOutcome, task string) bool {
	for _, diagnostic := range outcome.Diagnostics {
		if diagnostic.Task == task || strings.Contains(diagnostic.Path, task) {
			return true
		}
	}
	return false
}
