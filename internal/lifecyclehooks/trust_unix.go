//go:build !windows

package lifecyclehooks

import (
	"fmt"
	"os"
	"syscall"
)

func trustedExecutableOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect executable owner: unsupported file metadata %T", info.Sys())
	}
	owner := int(stat.Uid)
	if owner != 0 && owner != os.Geteuid() {
		return fmt.Errorf("run must be owned by the current user or root (uid %d)", owner)
	}
	return nil
}
