//go:build !windows

package runqueue

import "github.com/sneat-dev/wb/internal/procalive"

// processAlive reports whether pid names a live process.
func processAlive(pid int) bool {
	return procalive.Alive(pid)
}
