package worktrees

import (
	"os"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// secureHelperOps varies only fallible operations between authorization phases.
// Each helper still validates its own inherited descriptors and write roots.
type secureHelperOps struct {
	chdir  func(int) error
	getwd  func() (string, error)
	retain func(...*os.File) error
}

func defaultSecureHelperOps() secureHelperOps {
	return secureHelperOps{
		chdir:  unix.Fchdir,
		getwd:  os.Getwd,
		retain: retainDescriptorsAcrossGitExec,
	}
}
