package fleet

import (
	"runtime"
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
