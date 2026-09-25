//go:build darwin

package filewrite

import unix "github.com/sneat-dev/wb/internal/unixcompat"

// renameNoReplaceSyscall is Darwin's no-replace rename mechanics
// (renameatx_np's RENAME_EXCL), moved here from
// internal/worktrees (task-9 PR-3 review-756 N1) so RenameNoReplace owns
// the syscall itself instead of running a caller-supplied closure.
func renameNoReplaceSyscall(fromDirectoryFD int, fromName string, toDirectoryFD int, toName string) error {
	return unix.RenameatxNp(fromDirectoryFD, fromName, toDirectoryFD, toName, unix.RENAME_EXCL)
}
