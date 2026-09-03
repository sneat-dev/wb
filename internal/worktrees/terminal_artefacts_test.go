package worktrees

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	testRetiredStage = ".wb-retired-stage-6b0995eef65f84dace22d24df2644b32"
	testRetiredLock  = ".wb-retired-lock-6b0995eef65f84dace22d24df2644b33"
)

func newTerminalArtefactTask(t *testing.T) (worktreesRoot, task, taskPath string) {
	t.Helper()
	worktreesRoot = filepath.Join(t.TempDir(), "worktrees")
	task = "finished-task"
	taskPath = filepath.Join(worktreesRoot, task)
	if err := os.MkdirAll(taskPath, 0o755); err != nil {
		t.Fatal(err)
	}
	return worktreesRoot, task, taskPath
}

func TestPurgeTerminalArtefactsRemovesEmptyStageAndInertLock(t *testing.T) {
	worktreesRoot, task, taskPath := newTerminalArtefactTask(t)
	if err := os.Mkdir(filepath.Join(taskPath, testRetiredStage), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskPath, testRetiredLock), []byte("operation=finished-task\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	purged := purgeTerminalArtefacts(worktreesRoot, task)
	if len(purged) != 2 {
		t.Fatalf("purged = %#v, want the stage and the lock", purged)
	}
	kinds := map[string]bool{}
	for _, artefact := range purged {
		kinds[artefact.Kind] = true
		if artefact.Task != task || artefact.WorktreesRoot != worktreesRoot {
			t.Fatalf("purged artefact %#v lost its task identity", artefact)
		}
	}
	if !kinds["retired_stage"] || !kinds["retired_lock"] {
		t.Fatalf("purged kinds = %#v", kinds)
	}
	for _, name := range []string{testRetiredStage, testRetiredLock} {
		if _, err := os.Lstat(filepath.Join(taskPath, name)); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", name, err)
		}
	}
}

func TestPurgeTerminalArtefactsKeepsANonEmptyStageAsAuditedBacklog(t *testing.T) {
	worktreesRoot, task, taskPath := newTerminalArtefactTask(t)
	stage := filepath.Join(taskPath, testRetiredStage)
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "evidence"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if purged := purgeTerminalArtefacts(worktreesRoot, task); len(purged) != 0 {
		t.Fatalf("purged = %#v, want nothing: a non-empty stage needs audited recovery", purged)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("non-empty stage was touched: %v", err)
	}
}

func TestPurgeTerminalArtefactsLeavesEverythingWhileAnOperationHoldsTheTask(t *testing.T) {
	worktreesRoot, task, taskPath := newTerminalArtefactTask(t)
	if err := os.Mkdir(filepath.Join(taskPath, testRetiredStage), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskPath, ".lock"), []byte("operation=finished-task\npid=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if purged := purgeTerminalArtefacts(worktreesRoot, task); len(purged) != 0 {
		t.Fatalf("purged = %#v, want nothing while a lock is present", purged)
	}
	if _, err := os.Stat(filepath.Join(taskPath, testRetiredStage)); err != nil {
		t.Fatalf("stage removed under a live operation lock: %v", err)
	}
}

func TestPurgeTerminalArtefactsNeverFollowsOrRemovesForeignShapes(t *testing.T) {
	worktreesRoot, task, taskPath := newTerminalArtefactTask(t)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(taskPath, testRetiredStage)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// A directory wearing the lock name is not a lock WB ever wrote.
	if err := os.Mkdir(filepath.Join(taskPath, testRetiredLock), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(taskPath, "acme"), 0o755); err != nil {
		t.Fatal(err)
	}

	if purged := purgeTerminalArtefacts(worktreesRoot, task); len(purged) != 0 {
		t.Fatalf("purged = %#v, want nothing", purged)
	}
	for _, name := range []string{testRetiredStage, testRetiredLock, "acme"} {
		if _, err := os.Lstat(filepath.Join(taskPath, name)); err != nil {
			t.Fatalf("%s was removed: %v", name, err)
		}
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("symlink target was followed and removed: %v", err)
	}
}

func TestPurgeTerminalArtefactsIsSilentOnAVanishedTask(t *testing.T) {
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	if purged := purgeTerminalArtefacts(worktreesRoot, "never-existed"); len(purged) != 0 {
		t.Fatalf("purged = %#v, want nothing", purged)
	}
}
