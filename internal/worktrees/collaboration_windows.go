//go:build windows

package worktrees

import (
	"fmt"
	"os"
)

func lockCollaborationLegacyJournal(*os.File) (func(), error) {
	return nil, fmt.Errorf("legacy owner observation requires an effective journal lock on this platform")
}
