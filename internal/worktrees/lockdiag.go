package worktrees

import "github.com/sneat-dev/wb/internal/worktreeclaims"

type LockOwnerState = worktreeclaims.LockOwnerState

const (
	LockOwnerNone       = worktreeclaims.LockOwnerNone
	LockOwnerLive       = worktreeclaims.LockOwnerLive
	LockOwnerDead       = worktreeclaims.LockOwnerDead
	LockOwnerUnreadable = worktreeclaims.LockOwnerUnreadable
)

func diagnoseTaskLock(taskRoot, task string) (LockOwnerState, int) {
	return worktreeclaims.DiagnoseTaskLock(taskRoot, task, processIsDead)
}
func lockedReason(entry ListResult, resumeCommand string) string {
	return worktreeclaims.LockedReason(entry.LockOwner, entry.LockOwnerPID, resumeCommand)
}
func resumeInterruptedCommand(task string) string {
	return worktreeclaims.ResumeInterruptedCommand(task)
}
