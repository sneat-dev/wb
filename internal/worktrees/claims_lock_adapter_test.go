package worktrees

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupLockAdaptersPreserveDescriptorOwnership(t *testing.T) {
	t.Parallel()
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
