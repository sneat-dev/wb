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

var errRefused = errors.New("refused")

func goodMac() *sysctlSource {
	return &sysctlSource{
		raw:    func(string) ([]byte, error) { return loadavgBytes(2048*3, 2048), nil },
		cpu:    func() (float64, float64, error) { return 10, 100, nil },
		memory: func() (uint64, uint64, error) { return 60, 100, nil },
		disk:   okDisk,
	}
}

func TestSysctlSourceReportsEverythingAndDerivesCPUFromTwoReadings(t *testing.T) {
	t.Parallel()
	source := goodMac()
	first, err := source.Read()
	if err != nil {
		t.Fatal(err)
	}
	if first.CPUPercent != nil || *first.Load1 != 3 || *first.MemoryUsedBytes != 60 || *first.MemoryTotalBytes != 100 || *first.DiskFreeBytes != 40 {
		t.Errorf("first = %+v", first)
	}
	source.cpu = func() (float64, float64, error) { return 30, 200, nil }
	second, err := source.Read()
	if err != nil || second.CPUPercent == nil || *second.CPUPercent != 20 {
		t.Errorf("second = %+v, %v; want 20 percent", second, err)
	}
}

func TestSysctlSourceKeepsWhatItCouldReadWhenAPartFails(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		change func(*sysctlSource)
		absent func(Sample) bool
	}{
		"loadavg refused": {func(s *sysctlSource) { s.raw = func(string) ([]byte, error) { return nil, errRefused } }, func(s Sample) bool { return s.Load1 == nil && s.DiskTotalBytes != nil }},
		"loadavg short":   {func(s *sysctlSource) { s.raw = func(string) ([]byte, error) { return []byte{1}, nil } }, func(s Sample) bool { return s.Load1 == nil }},
		"cpu":             {func(s *sysctlSource) { s.cpu = func() (float64, float64, error) { return 0, 0, errRefused } }, func(s Sample) bool { return s.CPUPercent == nil && s.Load1 != nil }},
		"memory":          {func(s *sysctlSource) { s.memory = func() (uint64, uint64, error) { return 0, 0, errRefused } }, func(s Sample) bool { return s.MemoryTotalBytes == nil && s.Load1 != nil }},
		"disk":            {func(s *sysctlSource) { s.disk = func() (uint64, uint64, error) { return 0, 0, errRefused } }, func(s Sample) bool { return s.DiskFreeBytes == nil && s.Load1 != nil }},
	} {
		source := goodMac()
		test.change(source)
		sample, err := source.Read()
		if err == nil || !sample.HasData() || !test.absent(sample) {
			t.Errorf("%s: err %v, sample %+v", name, err, sample)
		}
	}
	if _, err := parseLoadavg(loadavgBytes(1, 0)); err == nil {
		t.Error("a zero scale gave no error")
	}
}
