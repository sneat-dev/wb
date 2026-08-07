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
//
// That child is still the coverage-instrumented test binary `go test
// -coverprofile` built (this package's CI run enables it): at exit it needs
// to flush its own coverage counters, which under Go's current coverage
// implementation means writing to a GOCOVERDIR-style directory. Once
// Landlock is active that write is denied exactly like any other outside an
// authorized root — collapsing what looks like a passing assertion into a
// failing process, and taking t.TempDir's own cleanup down with it. Grant
// the coverage directory as an extra write root before restricting so a
// coverage-instrumented run doesn't fail underneath a passing test.
func TestLandlockCapabilityUsesRetainedRootAfterPathSwap(t *testing.T) {
	if os.Getenv("WB_LANDLOCK_RETAINED_ROOT_CHILD") == "1" {
		testLandlockCapabilityUsesRetainedRootAfterPathSwap(t)
		return
	}
	if err := platformGitFilesystemCapabilityAvailable(); err != nil {
		t.Skipf("Landlock unavailable on this kernel: %v", err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestLandlockCapabilityUsesRetainedRootAfterPathSwap$")
	command.Env = append(landlockChildEnv(t), "WB_LANDLOCK_RETAINED_ROOT_CHILD=1")
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
	roots := []gitFilesystemCapabilityRoot{{path: rootPath, directory: held}}
	roots = append(roots, landlockCoverageWriteRoots(t)...)
	capability, err := newGitFilesystemCapability(roots...)
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
	command.Env = append(landlockChildEnv(t), "WB_LANDLOCK_DEVNULL_CHILD=1")
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
	roots = append(roots, landlockCoverageWriteRoots(t)...)
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

// landlockChildEnv returns the environment a Landlock-restricting child test
// process should start from: the parent's environment, plus a GOCOVERDIR
// pointed at a fresh directory the child can still authorize as one of its
// own capability write roots (see landlockCoverageWriteRoots) before it
// restricts itself. Without this, a coverage-instrumented test binary
// (go test -coverprofile, this package's CI configuration) fails to flush
// its own counters at exit purely because Landlock denies that write —
// nothing to do with the assertion the test actually makes.
func landlockChildEnv(t *testing.T) []string {
	t.Helper()
	coverDir := t.TempDir()
	return append(os.Environ(), "GOCOVERDIR="+coverDir)
}

// landlockCoverageWriteRoots authorizes GOCOVERDIR, if the parent set one via
// landlockChildEnv, as an additional Landlock write root. Returns nil outside
// a coverage-instrumented run (GOCOVERDIR unset), so a plain `go test`
// invocation is unaffected.
func landlockCoverageWriteRoots(t *testing.T) []gitFilesystemCapabilityRoot {
	t.Helper()
	coverDir := os.Getenv("GOCOVERDIR")
	if coverDir == "" {
		return nil
	}
	held, err := openAbsoluteDirectoryNoFollow(coverDir, false)
	if err != nil {
		t.Fatal(err)
	}
	return []gitFilesystemCapabilityRoot{{path: coverDir, directory: held}}
}
