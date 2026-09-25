//go:build linux

package filewrite

import unix "github.com/sneat-dev/wb/internal/unixcompat"

// renameNoReplaceSyscall is Linux's no-replace rename mechanics
// (renameat2's RENAME_NOREPLACE), moved here from
// internal/worktrees (task-9 PR-3 review-756 N1) so RenameNoReplace owns
// the syscall itself instead of running a caller-supplied closure.
func renameNoReplaceSyscall(fromDirectoryFD int, fromName string, toDirectoryFD int, toName string) error {
	return unix.Renameat2(fromDirectoryFD, fromName, toDirectoryFD, toName, unix.RENAME_NOREPLACE)
}
