//go:build !windows

package lifecyclehooks

import (
	"os"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func syncDirectory(path string) error {
	return syncDirectoryInjected(path, nil)
}

// syncDirectoryInjected is syncDirectory's test seam (task-9 PR-8 review B1):
// every production call site reaches it only through syncDirectory or a nil
// Injector, so production behaviour is unchanged. A test passes its own
// Injector to reach the open/dir-sync failure branches deterministically.
func syncDirectoryInjected(path string, inj *filewrite.Injector) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return filewrite.SyncDir(directory, inj)
}
