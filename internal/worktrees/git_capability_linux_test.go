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
}
