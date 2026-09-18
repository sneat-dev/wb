package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIdentifyManagedCheckoutActiveTerminalAndUnmanaged encodes
// IdentifyManagedCheckout's three outcomes directly: an active claim (active
// is true), a terminal claim (active is false, the safe-to-relocate case),
// and a linked worktree with no WB task identity at all (ok is false).
func TestIdentifyManagedCheckoutActiveTerminalAndUnmanaged(t *testing.T) {
	fixture := newGitFixture(t)

	activeCreated, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "id-active", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create active: %v", err)
	}
	active := activeCreated[0].WorktreeDir

	terminalCreated, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "id-terminal", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create terminal: %v", err)
	}
	terminal := terminalCreated[0].WorktreeDir
	if _, err := LogFinalize(context.Background(), LogFinalizeOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: terminal, Result: "success", Apply: true,
	}); err != nil {
		t.Fatalf("finalize terminal: %v", err)
	}

	unmanaged := filepath.Join(fixture.projectsRoot, "unmanaged-worktree")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "id-unmanaged", unmanaged)

	task, isActive, ok, err := IdentifyManagedCheckout(fixture.projectsRoot, active)
	if err != nil || !ok || !isActive || task != "id-active" {
		t.Fatalf("active checkout = task=%q active=%v ok=%v err=%v", task, isActive, ok, err)
	}

	task, isActive, ok, err = IdentifyManagedCheckout(fixture.projectsRoot, terminal)
	if err != nil || !ok || isActive || task != "id-terminal" {
		t.Fatalf("terminal checkout = task=%q active=%v ok=%v err=%v", task, isActive, ok, err)
	}

	_, _, ok, err = IdentifyManagedCheckout(fixture.projectsRoot, unmanaged)
	if err != nil || ok {
		t.Fatalf("unmanaged checkout = ok=%v err=%v, want ok=false and no error", ok, err)
	}
}

// TestRelocateCheckoutRefusesWhenDestinationAlreadyExists covers
// RelocateCheckout's own destination-occupied refusal, distinct from
// Relocate's: the checkout stays exactly in place and is reported
// ineligible, never an error.
func TestRelocateCheckoutRefusesWhenDestinationAlreadyExists(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "dest-exists", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	worktree := created[0].WorktreeDir
	destination := filepath.Join(t.TempDir(), "already-there")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := RelocateCheckout(context.Background(), RelocateCheckoutOptions{
		ProjectsRoot: fixture.projectsRoot, CanonicalDir: fixture.canonical,
		Source: worktree, Destination: destination, To: "shared",
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if result.Eligible {
		t.Fatalf("a checkout whose destination already exists must not be eligible: %#v", result)
	}
	if !strings.Contains(result.Reason, "destination already exists") {
		t.Fatalf("reason = %q, want it to name the occupied destination", result.Reason)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("the checkout must remain in place: %v", err)
	}
}

// TestRelocateCheckoutRefusesWhenTaskLockHeld covers the task-lock refusal:
// a concurrent operation already holding the checkout's task lock leaves
// RelocateCheckout reporting the checkout ineligible, not erroring.
func TestRelocateCheckoutRefusesWhenTaskLockHeld(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "lock-relocate", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	worktree := created[0].WorktreeDir
	destination := filepath.Join(t.TempDir(), "moved-elsewhere")

	operation, err := prepareOperationRoot(fixture.home, "lock-relocate", nil)
	if err != nil {
		t.Fatalf("prepare operation root: %v", err)
	}
	defer operation.close()
	lock, err := acquireLockAt(operation.Directory, "lock-relocate")
	if err != nil {
		t.Fatalf("acquire task lock: %v", err)
	}
	defer func() { _ = lock.release() }()

	result, err := RelocateCheckout(context.Background(), RelocateCheckoutOptions{
		ProjectsRoot: fixture.projectsRoot, CanonicalDir: fixture.canonical,
		Source: worktree, Destination: destination, To: "shared",
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if result.Eligible {
		t.Fatalf("a checkout whose task lock is held must not be eligible: %#v", result)
	}
	if !strings.Contains(result.Reason, "task lock held") {
		t.Fatalf("reason = %q, want it to name the held task lock", result.Reason)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("the checkout must remain in place: %v", err)
	}
}

// TestReverseRelocationRefusesWhenDestinationAlreadyExists covers
// ReverseRelocation's own pre-move guard: it must never overwrite whatever
// already occupies the restoration path.
func TestReverseRelocationRefusesWhenDestinationAlreadyExists(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "reverse-exists", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	destination := created[0].WorktreeDir
	source := filepath.Join(t.TempDir(), "already-there")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}

	err = ReverseRelocation(context.Background(), fixture.projectsRoot, fixture.canonical, destination, source, time.Now())
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want a refusal naming the occupied reversal destination", err)
	}
	if _, statErr := os.Stat(destination); statErr != nil {
		t.Fatalf("the checkout must remain at destination: %v", statErr)
	}
}
