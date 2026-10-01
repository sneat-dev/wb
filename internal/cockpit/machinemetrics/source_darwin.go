package machinemetrics

import (
	"errors"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"golang.org/x/sys/unix"
)

// gopsutil's calls, replaced by a test.
var (
	readCPUTimes      = cpu.Times
	readVirtualMemory = mem.VirtualMemory
)

var errNoCPUTimes = errors.New("no aggregate CPU times")

// NewSource reads the load average from the kernel's sysctl, CPU times and
// memory through gopsutil (which calls libSystem through purego, so WB still
// builds with CGO_ENABLED=0), and the disk of root.
func NewSource(root string) Source {
	return &sysctlSource{
		raw:    func(name string) ([]byte, error) { return unix.SysctlRaw(name) },
		cpu:    cpuTimes,
		memory: memoryUsage,
		disk:   func() (uint64, uint64, error) { return diskUsage(root) },
	}
}

// cpuTimes is the aggregate CPU time, in seconds, that was busy and in all.
func cpuTimes() (busy, all float64, err error) {
	times, err := readCPUTimes(false)
	if err != nil {
		return 0, 0, err
	}
	if len(times) == 0 {
		return 0, 0, errNoCPUTimes
	}
	total := times[0].User + times[0].System + times[0].Idle + times[0].Nice
	return total - times[0].Idle, total, nil
}

// memoryUsage is the used and total bytes of memory as gopsutil reports them:
// used is the total less what is available without swapping, which on macOS is
// the free and the inactive pages (the same rule as Linux's MemAvailable).
func memoryUsage() (used, total uint64, err error) {
	memory, err := readVirtualMemory()
	if err != nil {
		return 0, 0, err
	}
	return memory.Used, memory.Total, nil
}
