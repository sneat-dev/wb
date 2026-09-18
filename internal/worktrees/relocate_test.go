package worktrees

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestRelocateMovesManagedSharedWorktreeToLocalAndPreservesClaim(t *testing.T) {
	fixture := newGitFixture(t)
	configHome := t.TempDir()
	shared := filepath.Join(t.TempDir(), "shared-worktrees")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "wb", "worktrees.yaml"), []byte("version: 1\nworktrees:\n  root: "+shared+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "relocate-layout", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Create selected the configured root, so move it to local first. This also
	// exercises a worktree whose current placement differs from today's config.
	sharedWorktree := created[0].WorktreeDir
	plan, err := Relocate(context.Background(), RelocateOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocate-layout", To: "local"})
	if err != nil || len(plan.Results) != 1 || !plan.Results[0].Eligible || plan.Results[0].Applied {
		t.Fatalf("local relocation plan = %#v, err=%v", plan, err)
	}
	if _, err := os.Stat(sharedWorktree); err != nil {
		t.Fatalf("dry run changed shared worktree: %v", err)
	}
	applied, err := Relocate(context.Background(), RelocateOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocate-layout", To: "local", Apply: true})
	if err != nil || len(applied.Results) != 1 || !applied.Results[0].Applied || applied.Results[0].ClaimID == "" || applied.Results[0].ReceiptPath == "" {
		t.Fatalf("local relocation apply = %#v, err=%v", applied, err)
	}
	localWorktree := filepath.Join(fixture.canonical, ".worktrees", "relocate-layout")
	if _, err := os.Stat(sharedWorktree); !os.IsNotExist(err) {
		t.Fatalf("shared source remains after relocation: %v", err)
	}
	if _, err := Guard(context.Background(), localWorktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil {
		t.Fatalf("guard after relocation: %v", err)
	}
	listed, err := List(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocate-layout"})
	if err != nil || len(listed) != 1 || listed[0].WorktreeDir != localWorktree {
		t.Fatalf("list after relocation = %#v, err=%v", listed, err)
	}
	back, err := Relocate(context.Background(), RelocateOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocate-layout", To: "shared", Apply: true})
	if err != nil || len(back.Results) != 1 || !back.Results[0].Applied || back.Results[0].ReceiptPath == applied.Results[0].ReceiptPath {
		t.Fatalf("shared relocation apply = %#v, err=%v", back, err)
	}
	if _, err := Guard(context.Background(), sharedWorktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil {
		t.Fatalf("guard after shared relocation: %v", err)
	}
	again, err := Relocate(context.Background(), RelocateOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocate-layout", To: "local", Apply: true})
	if err != nil || len(again.Results) != 1 || !again.Results[0].Applied {
		t.Fatalf("second local relocation apply = %#v, err=%v", again, err)
	}
	retry, err := Relocate(context.Background(), RelocateOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocate-layout", To: "local", Apply: true})
	if err != nil || len(retry.Results) != 1 || !retry.Results[0].AlreadyThere || retry.Results[0].Applied || retry.Results[0].ReceiptPath != again.Results[0].ReceiptPath {
		t.Fatalf("relocation retry = %#v, err=%v", retry, err)
	}
}

func TestRelocateFinalizesIntentAfterMoveCrashWindow(t *testing.T) {
	fixture := newGitFixture(t)
	configHome := t.TempDir()
	shared := filepath.Join(t.TempDir(), "shared-worktrees")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "wb", "worktrees.yaml"), []byte("version: 1\nworktrees:\n  root: "+shared+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "relocate-crash-window", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	source := created[0].WorktreeDir
	_, err = Relocate(context.Background(), RelocateOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "relocate-crash-window", To: "local", Apply: true,
		afterWorktreeMoveBeforeReceipt: func() error { return errors.New("simulated process interruption") },
	})
	if err == nil || !strings.Contains(err.Error(), "simulated process interruption") {
		t.Fatalf("crash-window relocation error = %v", err)
	}
	destination := filepath.Join(fixture.canonical, ".worktrees", "relocate-crash-window")
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source remains after simulated interruption: %v", err)
	}
	if _, err := Guard(context.Background(), destination, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil {
		t.Fatalf("guard must corroborate moved checkout through intent: %v", err)
	}
	listed, err := List(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocate-crash-window"})
	if err != nil || len(listed) != 1 || listed[0].WorktreeDir != destination {
		t.Fatalf("list must discover interrupted relocation = %#v, err=%v", listed, err)
	}
	retry, err := Relocate(context.Background(), RelocateOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocate-crash-window", To: "local", Apply: true})
	if err != nil || len(retry.Results) != 1 || !retry.Results[0].Applied || !retry.Results[0].Finalized || retry.Results[0].ReceiptPath == "" {
		t.Fatalf("retry must finalize moved intent = %#v, err=%v", retry, err)
	}
	if _, err := Guard(context.Background(), destination, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil {
		t.Fatalf("guard after finalization: %v", err)
	}
}

func TestRelocateRefusesExternalButNeverDirtyWorktrees(t *testing.T) {
	if eligible, reason := relocationEligibility(ListResult{External: true, Clean: true}); eligible || reason == "" {
		t.Fatalf("external relocation eligibility = %t, %q", eligible, reason)
	}
	// The founder's decision (2026-09-18): uncommitted changes and unpushed
	// commits are preserved by the rename and are never a reason to refuse
	// a relocation, the same principle REQ: clone-migration-refusals already
	// applies to a clone move.
	if eligible, reason := relocationEligibility(ListResult{Clean: false}); !eligible || reason != "" {
		t.Fatalf("dirty relocation eligibility = %t, %q, want eligible with no refusal", eligible, reason)
	}
}

// TestReverseRelocationRestoresClaimResolution proves ReverseRelocation
// appends to the same Work Log relocation journal Relocate does (an intent
// before the move, a receipt after it verifies): after relocating a checkout
// and then reversing that exact relocation, the claim's resolved current
// location (resolveRelocationChain, walking every completed relocation
// receipt from the claim's immutable frozen path) is back at the original
// path — not just the checkout's physical directory, which a bare move
// would also restore, but the claim's own durable record of where it is.
func TestReverseRelocationRestoresClaimResolution(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "reverse-relocation", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// newGitFixture selects repository-local store mode, so Create placed
	// this in-clone; switch to the central default so there is a shared
	// destination to relocate to and back from.
	original := created[0].WorktreeDir
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "wb", "worktrees.yaml"), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	applied, err := Relocate(context.Background(), RelocateOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "reverse-relocation", To: "shared", Apply: true,
	})
	if err != nil || len(applied.Results) != 1 || !applied.Results[0].Applied {
		t.Fatalf("relocate = %#v, err=%v", applied, err)
	}
	relocated := applied.Results[0].Destination
	if _, err := os.Stat(relocated); err != nil {
		t.Fatalf("relocated checkout missing: %v", err)
	}

	resolution, err := wbhome.Resolve(fixture.projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	home := resolution.Write.Home
	claim, _, _, err := activeWorkLogClaim(home, relocated)
	if err != nil {
		t.Fatalf("claim lookup before reversal: %v", err)
	}

	if err := ReverseRelocation(context.Background(), fixture.projectsRoot, fixture.canonical, relocated, original, time.Now().UTC()); err != nil {
		t.Fatalf("reverse relocation: %v", err)
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("original path not restored: %v", err)
	}
	if _, err := os.Stat(relocated); !os.IsNotExist(err) {
		t.Fatalf("relocated path still exists: %v", err)
	}

	resolved, err := resolveRelocationChain(home, claim)
	if err != nil {
		t.Fatalf("resolve relocation chain: %v", err)
	}
	if filepath.Clean(resolved.worktree) != filepath.Clean(original) {
		t.Fatalf("resolved worktree = %q, want %q (the claim's own record, not just the directory)", resolved.worktree, original)
	}

	if _, err := Guard(context.Background(), original, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil {
		t.Fatalf("guard after reversal: %v", err)
	}
}

// TestRelocateRefusesABusyCheckout covers the founder's per-checkout safety
// checks (2026-09-18): a live process whose current working directory sits
// inside a specific checkout must refuse relocating that checkout, naming
// the PID and command, even though the checkout's Work Log claim is
// terminal (the safe, finished-task case) and would otherwise be relocated.
// Linux-only: /proc has no equivalent on other kernels, which is exactly
// what BusyProcessCheckSupported signals.
func TestRelocateRefusesABusyCheckout(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("busy-process detection only runs on linux")
	}
	fixture := newGitFixture(t)
	configHome := t.TempDir()
	shared := filepath.Join(t.TempDir(), "shared-worktrees")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "wb", "worktrees.yaml"), []byte("version: 1\nworktrees:\n  root: "+shared+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "busy-relocate", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir

	// Finished task: the safe case that would otherwise be relocated.
	if _, err := LogFinalize(context.Background(), LogFinalizeOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Result: "success", Apply: true,
	}); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	cmd := exec.Command("sleep", "60")
	cmd.Dir = worktree
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	plan, err := Relocate(context.Background(), RelocateOptions{ProjectsRoot: fixture.projectsRoot, Task: "busy-relocate", To: "local"})
	if err != nil || len(plan.Results) != 1 {
		t.Fatalf("relocate plan = %#v, err=%v", plan, err)
	}
	result := plan.Results[0]
	if result.Eligible {
		t.Fatalf("busy checkout must not be eligible for relocation: %#v", result)
	}
	if !strings.Contains(result.Reason, "process") || !strings.Contains(result.Reason, "working directory") {
		t.Fatalf("reason = %q, want it to name the busy process", result.Reason)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("busy checkout must remain in place: %v", err)
	}
}
