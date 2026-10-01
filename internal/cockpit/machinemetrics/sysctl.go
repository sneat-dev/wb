package machinemetrics

import (
	"encoding/binary"
	"errors"
)

// sysctlSource reads macOS through injected sysctl functions, so it is tested on
// every platform. It reports memory, load and disk, and never CPU percent: the
// only macOS sources of CPU ticks are the Mach calls host_statistics and
// host_processor_info, which need cgo (the kernel has no kern.cp_time), and WB
// builds without cgo. An absent cpu_percent is honest; a number derived from the
// load average would be wrong.
//
// Used memory is the total less what the kernel can hand out without paging: free,
// speculative and file-backed (external) pageable pages.
type sysctlSource struct {
	uint64Of func(name string) (uint64, error)
	raw      func(name string) ([]byte, error)
	disk     func() (free, total uint64, err error)
}

var errBadSysctl = errors.New("unexpected sysctl value")

// Read reads hw.memsize, vm.pagesize, the page counts, vm.loadavg and the disk.
func (s *sysctlSource) Read() (Sample, error) {
	var sample Sample
	raw, err := s.raw("vm.loadavg")
	if err != nil {
		return sample, err
	}
	if sample.Load1, err = parseLoadavg(raw); err != nil {
		return sample, err
	}
	total, err := s.uint64Of("hw.memsize")
	if err != nil {
		return sample, err
	}
	pageSize, err := s.uint64Of("vm.pagesize")
	if err != nil {
		return sample, err
	}
	var reclaimable uint64
	for _, name := range []string{"vm.page_free_count", "vm.page_speculative_count", "vm.page_pageable_external_count"} {
		pages, readErr := s.uint64Of(name)
		if readErr != nil {
			return sample, readErr
		}
		reclaimable += pages * pageSize
	}
	sample.MemoryTotalBytes, sample.MemoryUsedBytes = total, total-min(reclaimable, total)
	if sample.DiskFreeBytes, sample.DiskTotalBytes, err = s.disk(); err != nil {
		return sample, err
	}
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
