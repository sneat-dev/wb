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

// Read reads vm.loadavg, the CPU times, the memory figures and the disk, each
// independently: what could be read is returned with the errors of what could not.
func (s *sysctlSource) Read() (Sample, error) {
	var sample Sample
	var errs []error
	if raw, err := s.raw("vm.loadavg"); err != nil {
		errs = append(errs, err)
	} else if load, parseErr := parseLoadavg(raw); parseErr != nil {
		errs = append(errs, parseErr)
	} else {
		sample.Load1 = &load
	}
	if busy, all, err := s.cpu(); err != nil {
		errs = append(errs, err)
	} else {
		sample.CPUPercent = s.meter.percent(busy, all)
	}
	if used, total, err := s.memory(); err != nil {
		errs = append(errs, err)
	} else {
		sample.MemoryUsedBytes, sample.MemoryTotalBytes = &used, &total
	}
	if free, total, err := s.disk(); err != nil {
		errs = append(errs, err)
	} else {
		sample.DiskFreeBytes, sample.DiskTotalBytes = &free, &total
	}
	return sample, errors.Join(errs...)
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
