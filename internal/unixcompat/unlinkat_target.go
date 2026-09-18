package unix

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// errUnknownDirectoryHandle is returned when a directory fd was never
// registered in the path table Open/Openat maintain — including one that
// was already closed. Callers must treat this as a hard failure, never as
// license to resolve the entry relative to the process's current directory.
var errUnknownDirectoryHandle = errors.New("unknown directory handle")

// resolveDirectoryEntryPath maps a registered directory path and an entry
// name to the absolute path an *at call acts on. It is deliberately
// platform-neutral (no windows-only API) so both the Windows compatibility
// adapter and its unit tests can exercise the exact same decision on any
// GOOS.
func resolveDirectoryEntryPath(directory, name string) (string, error) {
	if directory == "" {
		return "", errUnknownDirectoryHandle
	}
	return filepath.Join(directory, name), nil
}

// validateUnlinkatTarget enforces AT_REMOVEDIR semantics against the
// target's actual on-disk type, independent of the platform-specific removal
// call that follows it:
//   - AT_REMOVEDIR set and the target is not a directory: refused, so a
//     caller asking to remove a directory never ends up deleting a file
//     that happens to share its name.
//   - AT_REMOVEDIR unset and the target is a directory: refused, matching
//     POSIX unlink's EISDIR, so a plain-file removal never ends up deleting
//     a directory.
func validateUnlinkatTarget(info os.FileInfo, flags int) error {
	isDir := info.IsDir()
	switch {
	case flags&AT_REMOVEDIR != 0 && !isDir:
		return fmt.Errorf("unlinkat: AT_REMOVEDIR refuses non-directory %s", info.Name())
	case flags&AT_REMOVEDIR == 0 && isDir:
		return fmt.Errorf("unlinkat: refusing to remove directory %s without AT_REMOVEDIR", info.Name())
	default:
		return nil
	}
}
