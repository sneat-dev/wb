package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

//nolint:paralleltest // A concurrent fork can inherit the held flock until exec, delaying its release on Linux.
func TestCleanupLockAdaptersPreserveDescriptorOwnership(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worktreesRoot := filepath.Join(root, "worktrees")
	if err := os.MkdirAll(filepath.Join(worktreesRoot, "task"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := acquireCleanupTaskAt(worktreesRoot, "task")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.validateHeldLock(); err != nil {
		t.Fatal(err)
	}
	if err := first.lock.release(); err != nil {
		t.Fatal(err)
	}
	first.close()
	for name, descriptor := range map[string]*os.File{"lock": first.lock.file, "task": first.task, "worktrees": first.worktrees} {
		if _, err := descriptor.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("released %s descriptor remains open: %v", name, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(worktreesRoot, "task", ".lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("released lock still occupies active name: %v", err)
	}
	second, err := acquireCleanupTaskAtReclaimingInterrupted(worktreesRoot, "task", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.lock.release(); err != nil {
		t.Fatal(err)
	}
	second.close()
	created, err := acquireCleanupTaskAtOrCreate(worktreesRoot, "other")
	if err != nil {
		t.Fatal(err)
	}
	if err := created.lock.release(); err != nil {
		t.Fatal(err)
	}
	created.close()
	if _, err := interruptedTaskLockPID(nil, "task"); err == nil {
		t.Fatal("missing lock descriptor accepted")
	}
}

func TestCleanupLockPrepareTaskRefusesOccupiedNamespace(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	path := filepath.Join(home, "worktrees")
	if err := os.WriteFile(path, []byte("preserve occupant"), 0600); err != nil {
		t.Fatal(err)
	}
	task, err := cleanupLockPorts().PrepareTask(home, "task")
	if err == nil || task != nil {
		t.Fatalf("occupied namespace prepared: %+v %v", task, err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "preserve occupant" {
		t.Fatalf("namespace occupant changed: %q %v", contents, err)
	}
}
