package fleet

import (
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Hardware is what the machine entry of this machine reports of itself
// (cockpit-views#req:machine-fields): the operating system and architecture
// names, the CPU count and the boot time. Zero fields are omitted from the
// document. Nothing here is a process list, a path or an environment value.
type Hardware struct {
	OS       string
	Arch     string
	CPUCount int
	BootTime time.Time
}

// LocalHardware reads this machine's hardware facts. The boot time is the
// operating system's own and is zero where it cannot be read.
func LocalHardware() Hardware {
	return Hardware{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUCount: runtime.NumCPU(), BootTime: bootTime()}
}

// parseBootTime reads the `btime` line (seconds since the epoch) of the text of
// /proc/stat; it returns the zero time when there is none.
func parseBootTime(stat string) time.Time {
	for _, line := range strings.Split(stat, "\n") {
		if value, found := strings.CutPrefix(line, "btime "); found {
			if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && seconds > 0 {
				return time.Unix(seconds, 0).UTC()
			}
		}
	}
	return time.Time{}
}
