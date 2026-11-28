//go:build !linux && !windows

package procalive

import "os"

// TODO(sneat-dev/wb#1022): Detect zombie processes on macOS/BSD without
// spawning a ps subprocess on every liveness probe. Until then, fall back to
// the kill(pid, 0) result.
func defaultReadProcStat(int) ([]byte, error) {
	return nil, os.ErrNotExist
}
