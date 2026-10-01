package machinemetrics

import (
	"testing"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

// The real macOS reader: read-only sysctls and a statfs of a temporary directory.
func TestRealMacSourceReadsThisMachine(t *testing.T) {
	t.Parallel()
	source := NewSource(t.TempDir())
	sample := readUntilCPU(t, source)
	if sample.CPUPercent == nil || *sample.CPUPercent < 0 || *sample.CPUPercent > 100 {
		t.Errorf("cpu_percent = %v after two readings", sample.CPUPercent)
	}
	if sample.MemoryTotalBytes == nil || *sample.MemoryTotalBytes == 0 || *sample.MemoryUsedBytes > *sample.MemoryTotalBytes || sample.DiskTotalBytes == nil || *sample.DiskTotalBytes == 0 || sample.Load1 == nil || *sample.Load1 < 0 {
		t.Errorf("sample = %+v", sample)
	}
}

func TestGopsutilRefusalsAndEmptyAnswersAreErrors(t *testing.T) { //nolint:paralleltest // it replaces package variables
	times, memory := readCPUTimes, readVirtualMemory
	t.Cleanup(func() { readCPUTimes, readVirtualMemory = times, memory })
	readCPUTimes = func(bool) ([]cpu.TimesStat, error) { return nil, errRefused }
	if _, _, err := cpuTimes(); err == nil {
		t.Error("refused CPU times gave no error")
	}
	readCPUTimes = func(bool) ([]cpu.TimesStat, error) { return nil, nil }
	if _, _, err := cpuTimes(); err == nil {
		t.Error("no CPU times gave no error")
	}
	readCPUTimes = func(bool) ([]cpu.TimesStat, error) { return []cpu.TimesStat{{User: 3, System: 1, Idle: 6}}, nil }
	if busy, all, err := cpuTimes(); err != nil || busy != 4 || all != 10 {
		t.Errorf("times = %v %v %v", busy, all, err)
	}
	readVirtualMemory = func() (*mem.VirtualMemoryStat, error) { return nil, errRefused }
	if _, _, err := memoryUsage(); err == nil {
		t.Error("refused memory gave no error")
	}
	readVirtualMemory = func() (*mem.VirtualMemoryStat, error) { return &mem.VirtualMemoryStat{Used: 4, Total: 9}, nil }
	if used, total, err := memoryUsage(); err != nil || used != 4 || total != 9 {
		t.Errorf("memory = %v %v %v", used, total, err)
	}
}
