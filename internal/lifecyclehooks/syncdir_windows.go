//go:build windows

package lifecyclehooks

import "github.com/sneat-dev/wb/internal/filewrite"

func syncDirectory(string) error { return nil }

// syncDirectoryInjected mirrors syncDirectory: directory sync is a no-op on
// Windows regardless of Injector, since Windows rejects Sync on a directory
// handle opened for read (task-9 PR-8 review B1).
func syncDirectoryInjected(string, *filewrite.Injector) error { return nil }
