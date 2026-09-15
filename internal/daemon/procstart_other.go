//go:build !linux

package daemon

import "time"

// ProcessStartTime reports that this platform cannot observe a process start
// time through a stable interface WB already depends on. Liveness therefore
// falls back to the PID coordinate alone, and callers report that the process
// generation is unknown rather than claiming a match.
func ProcessStartTime(int) (time.Time, bool) { return time.Time{}, false }
