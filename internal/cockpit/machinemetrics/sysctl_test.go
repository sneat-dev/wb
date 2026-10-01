//go:build darwin

package machinemetrics

import (
	"encoding/binary"
	"errors"
	"testing"
)

func loadavgBytes(first uint32, scale uint64) []byte {
	raw := make([]byte, 24)
	binary.LittleEndian.PutUint32(raw[0:], first)
	binary.LittleEndian.PutUint64(raw[16:], scale)
	return raw
}

func fakeSysctls(values map[string]uint64) func(string) (uint64, error) {
	return func(name string) (uint64, error) {
		value, ok := values[name]
		if !ok {
			return 0, errors.New("unknown oid")
		}
		return value, nil
	}
}

func goodSysctls() map[string]uint64 {
	return map[string]uint64{
		"hw.memsize": 1000 * 4096, "vm.pagesize": 4096,
		"vm.page_free_count": 100, "vm.page_speculative_count": 50, "vm.page_pageable_external_count": 250,
	}
}

func TestSysctlSourceReportsEverythingButCPU(t *testing.T) {
	t.Parallel()
	source := &sysctlSource{
		uint64Of: fakeSysctls(goodSysctls()),
		raw:      func(string) ([]byte, error) { return loadavgBytes(2048*3, 2048), nil },
		disk:     okDisk,
	}
	sample, err := source.Read()
	if err != nil {
		t.Fatal(err)
	}
	if sample.CPUPercent != nil {
		t.Error("macOS reports a CPU percent without cgo")
	}
	if sample.Load1 != 3 || sample.MemoryTotalBytes != 1000*4096 || sample.MemoryUsedBytes != 600*4096 || sample.DiskFreeBytes != 40 {
		t.Errorf("sample = %+v", sample)
	}
}

func TestSysctlSourceFailsOnAnyRefusal(t *testing.T) {
	t.Parallel()
	good := func(string) ([]byte, error) { return loadavgBytes(1, 1), nil }
	failing := func(skip string) func(string) (uint64, error) {
		values := goodSysctls()
		delete(values, skip)
		return fakeSysctls(values)
	}
	for _, name := range []string{"hw.memsize", "vm.pagesize", "vm.page_free_count", "vm.page_speculative_count", "vm.page_pageable_external_count"} {
		if _, err := (&sysctlSource{uint64Of: failing(name), raw: good, disk: okDisk}).Read(); err == nil {
			t.Errorf("%s refused: no error", name)
		}
	}
	if _, err := (&sysctlSource{uint64Of: fakeSysctls(goodSysctls()), raw: func(string) ([]byte, error) { return nil, errors.New("refused") }, disk: okDisk}).Read(); err == nil {
		t.Error("a refused vm.loadavg gave no error")
	}
	if _, err := (&sysctlSource{uint64Of: fakeSysctls(goodSysctls()), raw: func(string) ([]byte, error) { return []byte{1}, nil }, disk: okDisk}).Read(); err == nil {
		t.Error("a short vm.loadavg gave no error")
	}
	if _, err := (&sysctlSource{uint64Of: fakeSysctls(goodSysctls()), raw: good, disk: func() (uint64, uint64, error) { return 0, 0, errors.New("statfs") }}).Read(); err == nil {
		t.Error("a failed statfs gave no error")
	}
	if _, err := parseLoadavg(loadavgBytes(1, 0)); err == nil {
		t.Error("a zero scale gave no error")
	}
}

func TestMoreReclaimableThanTotalIsClampedToZeroUsed(t *testing.T) {
	t.Parallel()
	values := goodSysctls()
	values["vm.page_free_count"] = 5000
	sample, err := (&sysctlSource{uint64Of: fakeSysctls(values), raw: func(string) ([]byte, error) { return loadavgBytes(1, 1), nil }, disk: okDisk}).Read()
	if err != nil || sample.MemoryUsedBytes != 0 {
		t.Errorf("used = %d, %v", sample.MemoryUsedBytes, err)
	}
}
