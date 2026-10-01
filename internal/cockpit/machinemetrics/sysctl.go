//go:build darwin

package machinemetrics

import (
	"encoding/binary"
	"errors"
)

// sysctlSource reads macOS through injected functions: the kernel's sysctls for
// the load average, the system's own CPU times and memory figures (gopsutil's
// libSystem calls, made without cgo through purego) and statfs for the disk. CPU
// percent comes from two readings of the aggregate CPU times, so the first sample
// has none.
type sysctlSource struct {
	raw    func(name string) ([]byte, error)
	cpu    func() (busy, all float64, err error)
	memory func() (used, total uint64, err error)
	disk   func() (free, total uint64, err error)

	meter cpuMeter
}

var errBadSysctl = errors.New("unexpected sysctl value")

// Read reads vm.loadavg, the CPU times, the memory figures and the disk.
func (s *sysctlSource) Read() (Sample, error) {
	var sample Sample
	raw, err := s.raw("vm.loadavg")
	if err != nil {
		return sample, err
	}
	if sample.Load1, err = parseLoadavg(raw); err != nil {
		return sample, err
	}
	busy, all, err := s.cpu()
	if err != nil {
		return sample, err
	}
	if sample.MemoryUsedBytes, sample.MemoryTotalBytes, err = s.memory(); err != nil {
		return sample, err
	}
	if sample.DiskFreeBytes, sample.DiskTotalBytes, err = s.disk(); err != nil {
		return sample, err
	}
	sample.CPUPercent = s.meter.percent(busy, all)
	return sample, nil
}

// parseLoadavg reads the one-minute load of the `struct loadavg` that vm.loadavg
// returns: three 32-bit fixed-point values, alignment padding, then the 64-bit
// scale (little-endian on every supported Mac).
func parseLoadavg(raw []byte) (float64, error) {
	if len(raw) < 24 {
		return 0, errBadSysctl
	}
	scale := binary.LittleEndian.Uint64(raw[16:24])
	if scale == 0 {
		return 0, errBadSysctl
	}
	return float64(binary.LittleEndian.Uint32(raw[0:4])) / float64(scale), nil
}
