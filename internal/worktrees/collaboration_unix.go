//go:build !windows

package worktrees

import "os"

func lockCollaborationLegacyJournal(directory *os.File) (func(), error) {
	return lockLocalWorkLog(directory)
}
