//go:build linux

package worktrees

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLandlockInstallPinsCallerThroughImmediateExecBoundary(t *testing.T) {
	var installedOn int
	if err := pinOSThreadThroughLandlock(func() error {
		installedOn = unix.Gettid()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	defer runtime.UnlockOSThread()
	for range 100 {
		runtime.Gosched()
		if got := unix.Gettid(); got != installedOn {
			t.Fatalf("caller migrated from Linux task %d to %d before exec", installedOn, got)
		}
	}
}

// Landlock restriction is irreversible for a process. Run the assertion in a
// fresh test binary so the parent suite remains unrestricted.
//
// That child still exits through the normal test-binary path, which — under
// this package's CI configuration (go test -coverprofile) — tries to flush
// its own coverage counters and runs every t.TempDir cleanup registered so
// far. Both are filesystem writes outside any authorized Landlock root once
// restrictWithLandlock has run (t.TempDir's own directory included: Landlock
// grants beneath a directory, never covers removing that directory from its
// own unauthorized parent), so both fail — collapsing a passing assertion
// into a failing process for reasons that have nothing to do with what the
// test actually checks. Call landlockChildExit after the real assertions
// pass instead of returning normally: it calls os.Exit directly, which skips
// registered cleanups and the coverage flush the same way a hard kill would.
// This process is disposable — nothing downstream depends on either.
func TestLandlockCapabilityUsesRetainedRootAfterPathSwap(t *testing.T) {
	if os.Getenv("WB_LANDLOCK_RETAINED_ROOT_CHILD") == "1" {
		testLandlockCapabilityUsesRetainedRootAfterPathSwap(t)
		return
	}
	if err := platformGitFilesystemCapabilityAvailable(); err != nil {
		t.Skipf("Landlock unavailable on this kernel: %v", err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestLandlockCapabilityUsesRetainedRootAfterPathSwap$")
	command.Env = append(os.Environ(), "WB_LANDLOCK_RETAINED_ROOT_CHILD=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("retained-root Landlock child failed: %v\n%s", err, output)
	}
}

func testLandlockCapabilityUsesRetainedRootAfterPathSwap(t *testing.T) {
	container := t.TempDir()
	rootPath := filepath.Join(container, "capability-root")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := openAbsoluteDirectoryNoFollow(rootPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	capability, err := newGitFilesystemCapability(gitFilesystemCapabilityRoot{path: rootPath, directory: held})
	if err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	heldPath := filepath.Join(external, "held-root")
	if err := os.Rename(rootPath, heldPath); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(external, "replacement-root")
	if err := os.Mkdir(replacement, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(replacement, rootPath); err != nil {
		t.Fatal(err)
	}
	if err := restrictWithLandlock(capability); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(heldPath, "allowed"), []byte("held descriptor remains authorized\n"), 0o600); err != nil {
		t.Fatalf("write through moved retained root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "must-not-write"), []byte("replacement\n"), 0o600); err == nil {
		t.Fatal("path replacement received write authority after Landlock installation")
	}
	if _, err := os.Lstat(filepath.Join(replacement, "must-not-write")); !os.IsNotExist(err) {
		t.Fatalf("replacement root was mutated: %v", err)
	}
	landlockChildExit()
}

// TestLandlockCapabilityAllowsDevNullWrite pins a real incident: Git
// routinely redirects to /dev/null to discard output, and a Landlock
// ruleset that can't grant that write breaks every Git invocation outright —
// even one scoped to an authorized write root. The macOS sandbox backend
// grants the equivalent literal allowance (see sandboxProfile in
// git_capability_darwin.go); this pins the Landlock side of the same
// guarantee.
func TestLandlockCapabilityAllowsDevNullWrite(t *testing.T) {
	if os.Getenv("WB_LANDLOCK_DEVNULL_CHILD") == "1" {
		testLandlockCapabilityAllowsDevNullWrite(t)
		return
	}
	if err := platformGitFilesystemCapabilityAvailable(); err != nil {
		t.Skipf("Landlock unavailable on this kernel: %v", err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestLandlockCapabilityAllowsDevNullWrite$")
	command.Env = append(os.Environ(), "WB_LANDLOCK_DEVNULL_CHILD=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("dev-null Landlock child failed: %v\n%s", err, output)
	}
}

func testLandlockCapabilityAllowsDevNullWrite(t *testing.T) {
	rootPath := t.TempDir()
	held, err := openAbsoluteDirectoryNoFollow(rootPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	capability, err := newGitFilesystemCapability(gitFilesystemCapabilityRoot{path: rootPath, directory: held})
	if err != nil {
		t.Fatal(err)
	}
	if err := restrictWithLandlock(capability); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open /dev/null for writing under Landlock: %v", err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString("landlock allows /dev/null\n"); err != nil {
		t.Fatalf("write /dev/null under Landlock: %v", err)
	}
	landlockChildExit()
}

// landlockChildExit ends a Landlock-restricted child test process the same
// way a hard kill would: immediately, skipping every registered t.Cleanup
// and the coverage runtime's own exit-time counter flush. Both are
// filesystem writes outside any root the test authorized, and both are
// unnecessary — this process only ever existed to run one assertion under a
// real, irreversible restriction and report whether it held; nothing
// downstream reads its coverage contribution or expects its temp files gone.
func landlockChildExit() {
	os.Exit(0)
}
