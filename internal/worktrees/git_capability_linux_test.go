//go:build linux

package worktrees

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Landlock restriction is irreversible for a process. Run the assertion in a
// fresh test binary so the parent suite remains unrestricted.
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
	// Every directory the test itself needs is created above, before this
	// call — authorizeCoverageScratchDir repoints TMPDIR for the rest of the
	// process, so anything created after it (nothing here but the coverage
	// runtime's own exit-time scratch dir) must not overlap replacement,
	// which deliberately stays unauthorized.
	roots := []gitFilesystemCapabilityRoot{{path: rootPath, directory: held}}
	roots = append(roots, authorizeCoverageScratchDir(t)...)
	capability, err := newGitFilesystemCapability(roots...)
	if err != nil {
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
	roots := []gitFilesystemCapabilityRoot{{path: rootPath, directory: held}}
	roots = append(roots, authorizeCoverageScratchDir(t)...)
	capability, err := newGitFilesystemCapability(roots...)
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
}

// authorizeCoverageScratchDir exists only for these two tests: both
// self-exec as a fresh child process before applying Landlock's irreversible
// restriction, so the "fresh process" is still the coverage-instrumented
// test binary this package's CI run builds (go test -coverprofile). At exit
// that binary's coverage runtime needs to write its own counters, which —
// confirmed on CI, setting GOCOVERDIR did not change this — goes through
// os.MkdirTemp("", "gocoverdir"), i.e. os.TempDir(), regardless of
// GOCOVERDIR. Once Landlock is active that write is denied like any other
// outside an authorized root, collapsing an otherwise-passing assertion into
// a failing process and taking t.TempDir's own cleanup down with it.
//
// This creates a directory dedicated to that scratch write and points
// TMPDIR at it for the remainder of the process, so the coverage runtime's
// later os.MkdirTemp call lands somewhere already authorized. Call it only
// after every directory the test itself needs already exists: t.TempDir and
// os.MkdirTemp both honor TMPDIR, so anything created after this point —
// deliberately nothing but the coverage runtime's own exit-time scratch dir
// — lands under the same authorized directory too.
func authorizeCoverageScratchDir(t *testing.T) []gitFilesystemCapabilityRoot {
	t.Helper()
	scratch := t.TempDir()
	if err := os.Setenv("TMPDIR", scratch); err != nil {
		t.Fatal(err)
	}
	held, err := openAbsoluteDirectoryNoFollow(scratch, false)
	if err != nil {
		t.Fatal(err)
	}
	return []gitFilesystemCapabilityRoot{{path: scratch, directory: held}}
}
