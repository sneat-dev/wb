//go:build windows

package sessionmove

import "github.com/sneat-dev/wb/internal/unixcompat"

func openatWithIntFlags(dirfd int, name string, flags int, mode uint32) (int, error) {
	return unix.Openat(dirfd, name, flags, mode)
}
