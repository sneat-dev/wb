//go:build !darwin && !linux

package filewrite

import (
	"fmt"
	"os"
)

// renameNoReplaceSyscall has no atomic no-replace rename mechanics on
// this platform, moved here from internal/worktrees (task-9 PR-3
// review-756 N1) so RenameNoReplace owns the syscall itself instead of
// running a caller-supplied closure.
func renameNoReplaceSyscall(_ int, _ string, _ int, _ string) error {
	return fmt.Errorf("atomic no-replace rename is unsupported on this platform")
}

// publishImmutableTemporaryAt cannot publish on platforms without an atomic
// no-replace rename. Keep the same competing-writer readback contract.
func publishImmutableTemporaryAt(directory *os.File, temporary, name string, content []byte, idempotent bool, inj *Injector) (bool, error) {
	err := RenameNoReplace(int(directory.Fd()), temporary, int(directory.Fd()), name, inj)
	return immutablePublicationError(directory, name, content, idempotent, err)
}
