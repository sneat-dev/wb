package worktrees

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeLock(t *testing.T, taskRoot, contents string) {
	t.Helper()
	if err := os.MkdirAll(taskRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskRoot, ".lock"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnoseTaskLockClassifiesOwner(t *testing.T) {
	// A PID that cannot be running: allocate one, reap it, and confirm.
	dead := deadPID(t)

	for _, tc := range []struct {
		name     string
		contents string
		want     LockOwnerState
		wantPID  int
	}{
		{"live self", fmt.Sprintf("operation=task\npid=%d\n", os.Getpid()), LockOwnerLive, os.Getpid()},
		{"dead owner", fmt.Sprintf("operation=task\npid=%d\n", dead), LockOwnerDead, dead},
		{"empty file", "", LockOwnerUnreadable, 0},
		{"wrong task", fmt.Sprintf("operation=other\npid=%d\n", dead), LockOwnerUnreadable, 0},
		{"missing pid line", "operation=task\n", LockOwnerUnreadable, 0},
		{"non numeric pid", "operation=task\npid=abc\n", LockOwnerUnreadable, 0},
		{"negative pid", "operation=task\npid=-1\n", LockOwnerUnreadable, 0},
		{"padded pid rejected", "operation=task\npid=007\n", LockOwnerUnreadable, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "task")
			writeLock(t, root, tc.contents)
			state, pid := diagnoseTaskLock(root, "task")
			if state != tc.want || pid != tc.wantPID {
				t.Fatalf("diagnoseTaskLock = (%q, %d), want (%q, %d)", state, pid, tc.want, tc.wantPID)
			}
		})
	}
}

func TestDiagnoseTaskLockAbsentLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "task")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if state, pid := diagnoseTaskLock(root, "task"); state != LockOwnerNone || pid != 0 {
		t.Fatalf("absent lock = (%q, %d), want (%q, 0)", state, pid, LockOwnerNone)
	}
}

// A dead-owner lock must never be reported as merely "locked": that phrasing
// is what sent an operator to `rm -f` on WB-internal state when an audited
// `--resume-interrupted` recovery existed the whole time.
func TestLockedReasonNamesTheRemedyOnlyWhenRecoverable(t *testing.T) {
	cmd := resumeInterruptedCommand("my-task")
	if cmd != "wb worktree cleanup my-task --resume-interrupted" {
		t.Fatalf("resumeInterruptedCommand = %q", cmd)
	}

	deadReason := lockedReason(ListResult{Locked: true, LockOwner: LockOwnerDead, LockOwnerPID: 4242}, cmd)
	for _, want := range []string{"4242", "--resume-interrupted", "my-task"} {
		if !contains(deadReason, want) {
			t.Fatalf("dead-owner reason %q missing %q", deadReason, want)
		}
	}

	// A live owner must NOT advertise recovery — recovering a held lock would
	// race a running operation.
	liveReason := lockedReason(ListResult{Locked: true, LockOwner: LockOwnerLive, LockOwnerPID: 99}, cmd)
	if contains(liveReason, "--resume-interrupted") {
		t.Fatalf("live-owner reason must not suggest recovery: %q", liveReason)
	}
	if !contains(liveReason, "still running") {
		t.Fatalf("live-owner reason should say it is running: %q", liveReason)
	}

	// Unreadable metadata must not advertise recovery either: the recovery
	// path validates that same metadata and would refuse.
	unreadable := lockedReason(ListResult{Locked: true, LockOwner: LockOwnerUnreadable}, cmd)
	if contains(unreadable, "--resume-interrupted") {
		t.Fatalf("unreadable reason must not suggest recovery: %q", unreadable)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// deadPID returns a PID that is conclusively not running.
func deadPID(t *testing.T) int {
	t.Helper()
	proc, err := os.StartProcess("/bin/sh", []string{"sh", "-c", "exit 0"}, &os.ProcAttr{})
	if err != nil {
		t.Skipf("cannot spawn a throwaway process: %v", err)
	}
	state, err := proc.Wait()
	if err != nil || !state.Exited() {
		t.Skipf("throwaway process did not exit cleanly: %v", err)
	}
	return proc.Pid
}
