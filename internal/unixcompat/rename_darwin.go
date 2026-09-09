//go:build darwin

package unix

import "golang.org/x/sys/unix"

const RENAME_EXCL = unix.RENAME_EXCL

var RenameatxNp = unix.RenameatxNp
