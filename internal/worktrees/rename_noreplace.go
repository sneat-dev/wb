package worktrees

import "github.com/sneat-dev/wb/internal/filewrite"

// renameNoReplace is a thin delegate to filewrite.RenameNoReplace (task-9
// PR-3 review-756 N1): the per-OS no-replace rename mechanics (renameat2's
// RENAME_NOREPLACE on Linux, renameatx_np's RENAME_EXCL on Darwin) used to
// live here in three separate build-tagged files; they moved into
// internal/filewrite so RenameNoReplace performs the syscall itself
// instead of running a caller-supplied closure. This delegate exists
// because renameNoReplace is also called from call sites here that are
// not a temp-file write-and-publish sequence (worktrees.go's swap-rename
// helpers), which this task does not otherwise touch.
func renameNoReplace(fromDirectoryFD int, fromName string, toDirectoryFD int, toName string) error {
	return filewrite.RenameNoReplace(fromDirectoryFD, fromName, toDirectoryFD, toName, nil)
}
