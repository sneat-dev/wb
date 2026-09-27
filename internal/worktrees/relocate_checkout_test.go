package worktrees

import (
	"context"
	"encoding/json"
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

//nolint:paralleltest // newGitFixture calls t.Setenv while constructing an isolated real-Git fixture
func TestRelocateCheckoutRefusesUnclaimedAndBusyCheckouts(t *testing.T) {
	fixture := newGitFixture(t)
	unmanaged := filepath.Join(fixture.projectsRoot, "unmanaged-relocation")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "unmanaged-relocation", unmanaged)
	destination := filepath.Join(t.TempDir(), "destination")
	options := RelocateCheckoutOptions{ProjectsRoot: fixture.projectsRoot, CanonicalDir: fixture.canonical, Source: unmanaged, Destination: destination, To: "shared", Apply: true}
	result, err := RelocateCheckout(context.Background(), options)
	if err != nil || result.Eligible || result.Applied || !strings.Contains(result.Reason, "Work Log claim is not corroborated") {
		t.Fatalf("unclaimed checkout = (%#v, %v)", result, err)
	}
	if _, err := os.Stat(unmanaged); err != nil {
		t.Fatalf("unclaimed checkout moved: %v", err)
	}

	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "busy-relocation", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	options.Source = created[0].WorktreeDir
	gitDir := gitTestOutput(t, options.Source, "rev-parse", "--absolute-git-dir")
	marker := filepath.Join(gitDir, "MERGE_HEAD")
	if err := os.WriteFile(marker, []byte(strings.Repeat("0", 40)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = RelocateCheckout(context.Background(), options)
	if err != nil || result.Eligible || result.Applied || !strings.Contains(result.Reason, "merge is in progress") {
		t.Fatalf("busy checkout = (%#v, %v)", result, err)
	}
	if _, err := os.Stat(options.Source); err != nil {
		t.Fatalf("busy checkout moved: %v", err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("destination created despite refusal: %v", err)
	}
}

//nolint:paralleltest // newGitFixture calls t.Setenv while constructing an isolated real-Git fixture
func TestRelocateCheckoutPlanKeepsSourceAndWritesNoReceipt(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "plan-relocation", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := created[0].WorktreeDir
	destination := filepath.Join(fixture.canonical, ".worktrees", "plan-relocation-destination")
	result, err := RelocateCheckout(context.Background(), RelocateCheckoutOptions{
		ProjectsRoot: fixture.projectsRoot, CanonicalDir: fixture.canonical, Source: source, Destination: destination, To: "local",
	})
	if err != nil || !result.Eligible || result.Applied || result.ReceiptPath != "" {
		t.Fatalf("relocation plan = (%#v, %v)", result, err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("plan moved source: %v", err)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("plan created destination: %v", err)
	}
}

func TestRelocateCheckoutApplyMovesExactCheckoutAndRecordsReceipt(t *testing.T) {
	fixture := newGitFixture(t)
	configHome := t.TempDir()
	sharedRoot := filepath.Join(t.TempDir(), "shared-worktrees")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "wb", "worktrees.yaml"), []byte("version: 1\nworktrees:\n  root: "+sharedRoot+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "direct-checkout-move",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := created[0].WorktreeDir
	destination := filepath.Join(fixture.canonical, ".worktrees", "direct-checkout-move")
	if !strings.HasPrefix(source, sharedRoot+string(filepath.Separator)) || source == destination {
		t.Fatalf("created checkout %q is not in the configured shared root", source)
	}
	result, err := RelocateCheckout(context.Background(), RelocateCheckoutOptions{
		ProjectsRoot: fixture.projectsRoot, CanonicalDir: fixture.canonical,
		Source: source, Destination: destination, To: "local", Apply: true,
	})
	if err != nil || !result.Eligible || !result.Applied || !result.Repaired || result.ReceiptPath == "" {
		t.Fatalf("direct checkout relocation = (%#v, %v), want repaired, receipted move", result, err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source still exists after exact move: %v", err)
	}
	if err := VerifyClonePlacement(context.Background(), fixture.canonical, []string{destination}); err != nil {
		t.Fatalf("moved checkout is not registered and usable: %v", err)
	}
	claim, _, _, err := activeWorkLogClaim(fixture.home, destination)
	if err != nil || claim.Task != "direct-checkout-move" || claim.Repository != "acme/app" {
		t.Fatalf("claim after exact move = (%#v, %v)", claim, err)
	}
	content, err := os.ReadFile(result.ReceiptPath)
	if err != nil {
		t.Fatalf("relocation receipt is not durable: %v", err)
	}
	var receipt workLogRelocationReceipt
	if err := json.Unmarshal(content, &receipt); err != nil {
		t.Fatalf("relocation receipt is not valid JSON: %v", err)
	}
	if receipt.Source != source || receipt.Destination != destination {
		t.Fatalf("receipt describes %q -> %q, want %q -> %q", receipt.Source, receipt.Destination, source, destination)
	}
	if receipt.Type != workLogRelocationType || receipt.To != "local" ||
		receipt.DestinationRoot != filepath.Join(fixture.canonical, ".worktrees") ||
		receipt.DestinationRelative != "direct-checkout-move" {
		t.Fatalf("receipt placement = %#v", receipt)
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
