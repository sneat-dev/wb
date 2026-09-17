//go:build linux

package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// ProcessStartTime observes when pid started, so a recorded PID can be checked
// against the process now holding it. It reports false when the platform cannot
// answer, which callers must surface as unknown rather than as a match.
func ProcessStartTime(pid int) (time.Time, bool) {
	if pid <= 0 {
		return time.Time{}, false
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return time.Time{}, false
	}
	ticks, ok := ParseProcStatStartTicks(string(stat))
	if !ok {
		return time.Time{}, false
	}
	system, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	bootTime, ok := ParseProcStatBootTime(string(system))
	if !ok {
		return time.Time{}, false
	}
	return ProcessStartFromProcStat(ticks, bootTime), true
}
