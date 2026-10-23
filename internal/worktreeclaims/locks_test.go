package worktreeclaims

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openLockDirectory(t *testing.T) (*os.File, string) {
	t.Helper()
	path := testCanonicalTemp(t)
	directory, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	return directory, path
}
func TestClaimsOperationLockDescriptorAndRetirement(t *testing.T) {
	t.Parallel()
	directory, path := openLockDirectory(t)
	lock, err := AcquireLockAt(directory, "task", 12345)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLockAt(directory, "task", 12345); !errors.Is(err, ErrOperationLockHeld) {
		t.Fatalf("live holder: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(path, ".lock"))
	if err != nil || string(data) != "operation=task\npid=12345\n" {
		t.Fatalf("metadata %q: %v", data, err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, ".lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active entry: %v", err)
	}
	next, err := AcquireLockAt(directory, "task", 12345)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Release(); err != nil {
		t.Fatal(err)
	}
}
func TestClaimsOperationLockInterruptedAndPreserve(t *testing.T) {
	t.Parallel()
	directory, path := openLockDirectory(t)
	if err := os.WriteFile(filepath.Join(path, ".lock"), []byte("operation=task\npid=999999\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReclaimInterruptedLock(directory, false); err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("refusal: %v", err)
	}
	held, err := AcquireOperationLock(directory, true, 1)
	if err != nil || !held.ReclaimedInterrupted() || held.File() == nil {
		t.Fatalf("reclaim: %v", err)
	}
	held.Preserve()
	if _, err := os.Stat(filepath.Join(path, ".lock")); err != nil {
		t.Fatalf("preserved: %v", err)
	}
	recovered, err := ReclaimInterruptedLock(directory, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.Release(); err != nil {
		t.Fatal(err)
	}
}
func TestClaimsOperationLockIdentityAndMove(t *testing.T) {
	t.Parallel()
	directory, path := openLockDirectory(t)
	source := filepath.Join(path, "source")
	if err := os.WriteFile(source, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	identity, err := ExclusivelyOwnedLockIdentity(file)
	if err != nil {
		t.Fatal(err)
	}
	if !LockEntryStillMatches(directory, "source", identity) {
		t.Fatal("identity mismatch")
	}
	moved, err := MoveExpectedLockNoReplace(directory, "source", "target", identity)
	if err != nil {
		t.Fatal(err)
	}
	_ = moved.Close()
	if _, err := MoveExpectedLockNoReplace(directory, "source", "target", identity); err == nil {
		t.Fatal("accepted missing source")
	}
	if err := os.Link(filepath.Join(path, "target"), filepath.Join(path, "linked")); err != nil {
		t.Fatal(err)
	}
	linked, err := os.Open(filepath.Join(path, "linked"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = linked.Close() })
	if _, err := ExclusivelyOwnedLockIdentity(linked); err == nil {
		t.Fatal("accepted hard link")
	}
	if _, err := LockIdentity(nil); err == nil {
		t.Fatal("accepted nil descriptor")
	}
}
func TestClaimsWorkLogLockAndRecoveryRead(t *testing.T) {
	t.Parallel()
	home := testCanonicalTemp(t)
	valid := func(s string) bool { return s != "" && !strings.ContainsAny(s, "/\\") && s != ".." }
	run, err := OpenLockedWorkLogRun(home, "effort", "run", "claim", true, valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(run.Path, "claims"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(run.Path, "terminals"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.Path, "claims", "claim.json"), []byte(`{"id":"claim"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.Path, "terminals", "claim.json"), []byte(`{"done":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	claim, err := ReadWorkLogClaimAt[struct {
		ID string `json:"id"`
	}](run.Directory, "claim", valid)
	if err != nil || claim.ID != "claim" {
		t.Fatalf("claim: %+v %v", claim, err)
	}
	terminal, err := ReadWorkLogTerminalAt[struct {
		Done bool `json:"done"`
	}](run.Directory, "claim", valid)
	if err != nil || !terminal.Done {
		t.Fatalf("terminal: %+v %v", terminal, err)
	}
	run.Close()
	outbox, err := OpenWorkLogOutbox(home, "effort", true, valid)
	if err != nil {
		t.Fatal(err)
	}
	_ = outbox.Close()
}
func TestClaimsCleanupLockAndDiagnostics(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	worktreesRoot := filepath.Join(root, "worktrees")
	if err := os.MkdirAll(filepath.Join(worktreesRoot, "task"), 0700); err != nil {
		t.Fatal(err)
	}
	ports := CleanupLockPorts{PID: func() int { return 123 }, ProcessIsDead: func(pid int) bool { return pid == 123 }}
	task, err := ports.AcquireCleanupTaskAt(worktreesRoot, "task")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.ValidateHeldLock(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRecoveredCleanupLock(true, task); err != nil {
		t.Fatal(err)
	}
	if err := task.Lock.Release(); err != nil {
		t.Fatal(err)
	}
	PurgeTerminalTaskLockDebris(task)
	task.Close()
	if _, err := os.Stat(filepath.Join(worktreesRoot, "task", ".lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock remains: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreesRoot, "task", ".lock"), []byte("operation=task\npid=123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	state, pid := DiagnoseTaskLock(filepath.Join(worktreesRoot, "task"), "task", ports.ProcessIsDead)
	if state != LockOwnerDead || pid != 123 {
		t.Fatalf("diagnostic: %s %d", state, pid)
	}
	recovered, err := ports.AcquireCleanupTaskAtReclaimingInterruptedLock(worktreesRoot, "task")
	if err != nil {
		t.Fatal(err)
	}
	if pid, err := InterruptedTaskLockPID(recovered.Lock.file, "task", ports.ProcessIsDead); err != nil || pid != 123 {
		t.Fatalf("owner: %d %v", pid, err)
	}
	recovered.PreserveLock()
	recovered.Close()
	if got := LockedReason(state, pid, ResumeInterruptedCommand("task")); !strings.Contains(got, "--resume-interrupted") {
		t.Fatal(got)
	}
}
func TestClaimsRepositoryRegistrationLock(t *testing.T) {
	t.Parallel()
	directory, _ := openLockDirectory(t)
	now := time.Now
	held, err := AcquireRepositoryRegistrationLock(directory, func() error { return nil }, now, time.Sleep, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireRepositoryRegistrationLock(nil, func() error { return nil }, now, time.Sleep, time.Second); err == nil {
		t.Fatal("accepted nil common descriptor")
	}
}
