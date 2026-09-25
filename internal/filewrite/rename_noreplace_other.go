//go:build !darwin && !linux

package filewrite

import "fmt"

// renameNoReplaceSyscall has no atomic no-replace rename mechanics on
// this platform, moved here from internal/worktrees (task-9 PR-3
// review-756 N1) so RenameNoReplace owns the syscall itself instead of
// running a caller-supplied closure.
func renameNoReplaceSyscall(_ int, _ string, _ int, _ string) error {
	return fmt.Errorf("atomic no-replace rename is unsupported on this platform")
}
