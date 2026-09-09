//go:build darwin

package worktrees

import "github.com/sneat-dev/wb/internal/unixcompat"

func renameNoReplace(fromFD int, from string, toFD int, to string) error {
	return unix.RenameatxNp(fromFD, from, toFD, to, unix.RENAME_EXCL)
}
