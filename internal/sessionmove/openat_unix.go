//go:build !windows

package sessionmove

import "github.com/sneat-dev/wb/internal/unixcompat"

// openatWithIntFlags keeps the injectable Openat signature stable across the
// platform adapters without changing the flags or descriptor authority.
func openatWithIntFlags(dirfd int, name string, flags int, mode uint32) (int, error) {
	return unix.Openat(dirfd, name, flags, mode)
}
