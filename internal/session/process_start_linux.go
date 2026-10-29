//go:build linux

package session

import (
	"github.com/sneat-dev/wb/internal/daemon"
	"time"
)

// Use WB's native reader, which requires both process and boot-time evidence
// from the same absolute /proc root. Environment overrides cannot redirect it.
func processStartTime(pid int) (time.Time, bool) {
	return daemon.ProcessStartTime(pid)
}
