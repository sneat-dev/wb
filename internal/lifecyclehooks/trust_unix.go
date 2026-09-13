//go:build !windows

package lifecyclehooks

import (
	"fmt"
	"os"
	"syscall"
)

func validateTrustedExecutable(_ string, info os.FileInfo) error {
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("run must resolve to an executable file")
	}
	return validateTrustedOwnerAndMode(info, "run")
}

func validateTrustedControlFile(_ string, info os.FileInfo, purpose string) error {
	return validateTrustedOwnerAndMode(info, purpose)
}

func validateTrustedOwnerAndMode(info os.FileInfo, purpose string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect %s owner: unsupported file metadata %T", purpose, info.Sys())
	}
	owner := int(stat.Uid)
	if owner != 0 && owner != os.Geteuid() {
		return fmt.Errorf("%s must be owned by the current user or root (uid %d)", purpose, owner)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s must not be writable by group or other users", purpose)
	}
	return nil
}
