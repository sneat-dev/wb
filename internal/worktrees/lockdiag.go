package worktrees

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// LockOwnerState classifies the owner of a task's `.lock` without acquiring
// it. It exists so a refusal can name the remedy instead of only naming the
// obstacle: an operator told "task is locked" cannot tell a peer operation
// running right now from one a watchdog killed hours ago, and those two have
// opposite correct responses (wait vs. recover).
type LockOwnerState string

const (
	// LockOwnerNone means no `.lock` was present.
	LockOwnerNone LockOwnerState = ""
	// LockOwnerLive means the recorded PID is running, or its liveness could
	// not be established beyond doubt. Recovery must not be suggested.
	LockOwnerLive LockOwnerState = "live"
	// LockOwnerDead means the recorded PID is conclusively gone (ESRCH), so
	// the lock is a recoverable remnant of an interrupted operation.
	LockOwnerDead LockOwnerState = "dead"
	// LockOwnerUnreadable means a `.lock` exists but does not carry the exact
	// operation/PID metadata WB writes, so no claim about its owner is
	// possible. Recovery is not offered, because `--resume-interrupted`
	// validates that same metadata and would refuse too.
	LockOwnerUnreadable LockOwnerState = "unreadable"
)

// diagnoseTaskLock reports who owns taskRoot's `.lock`, read-only. It never
// opens the lock for writing, never takes it, and never mutates anything, so
// it is safe to run during a plain listing. Any doubt resolves to
// LockOwnerLive: refusing to recover a lock that might still be held is the
// safe direction, and matches interruptedTaskLockPID's own posture of
// accepting only a conclusively dead owner.
func diagnoseTaskLock(taskRoot, task string) (LockOwnerState, int) {
	fd, err := unix.Open(taskRoot+string(os.PathSeparator)+".lock", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return LockOwnerNone, 0
	}
	file := os.NewFile(uintptr(fd), "wb-lock-diagnostic")
	if file == nil {
		_ = unix.Close(fd)
		return LockOwnerUnreadable, 0
	}
	defer func() { _ = file.Close() }()

	contents, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(contents) > 4096 {
		return LockOwnerUnreadable, 0
	}
	// Exactly the shape acquireOperationLock writes and
	// interruptedTaskLockPID accepts: "operation=<task>\npid=<n>\n".
	lines := strings.Split(string(contents), "\n")
	if len(lines) != 3 || lines[2] != "" || lines[0] != "operation="+task {
		return LockOwnerUnreadable, 0
	}
	pid, convErr := strconv.Atoi(strings.TrimPrefix(lines[1], "pid="))
	if convErr != nil || pid <= 0 || lines[1] != fmt.Sprintf("pid=%d", pid) {
		return LockOwnerUnreadable, 0
	}
	// Only ESRCH proves the owner is gone. EPERM means a live process owned
	// by another user; any other error is ambiguous. Both stay "live".
	if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
		return LockOwnerDead, pid
	}
	return LockOwnerLive, pid
}

// lockedReason renders the refusal for a locked task. resumeCommand is the
// exact command that can recover a dead-owner lock; callers that are not
// themselves able to recover pass the cleanup command that can.
func lockedReason(entry ListResult, resumeCommand string) string {
	switch entry.LockOwner {
	case LockOwnerDead:
		return fmt.Sprintf(
			"task is locked by an interrupted operation whose owner PID %d is gone; recover it with `%s`",
			entry.LockOwnerPID, resumeCommand,
		)
	case LockOwnerUnreadable:
		return "task is locked and its .lock does not carry WB's operation/PID metadata, " +
			"so its owner cannot be established; inspect it before removing anything by hand"
	case LockOwnerLive:
		return fmt.Sprintf("task is locked by an operation that is still running (PID %d)", entry.LockOwnerPID)
	default:
		return "task is locked by an active or interrupted operation"
	}
}

// resumeInterruptedCommand is the exact recovery invocation for one task.
func resumeInterruptedCommand(task string) string {
	return "wb worktree cleanup " + task + " --resume-interrupted"
}
