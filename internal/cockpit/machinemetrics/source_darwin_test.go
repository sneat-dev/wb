package machinemetrics

import (
	"testing"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

// TestNewSourceIsWiredToTheMacReaders: the source reads the load average
// through the kernel's sysctl, CPU and memory through gopsutil and the disk of
// the directory it was given. Each reader is called once; what the machine
// answers over time (a CPU percent needs two readings a tick apart) is the e2e
// tier's (source_e2e_darwin_test.go).
func TestNewSourceIsWiredToTheMacReaders(t *testing.T) {
	t.Parallel()
	source, ok := NewSource(t.TempDir()).(*sysctlSource)
	if !ok {
		t.Fatal("the macOS source is not the sysctl source")
	}
	if raw, err := source.raw("vm.loadavg"); err != nil || len(raw) == 0 {
		t.Errorf("vm.loadavg = %d bytes, %v", len(raw), err)
	}
	if busy, all, err := source.cpu(); err != nil || all <= 0 || busy < 0 || busy > all {
		t.Errorf("cpu times = %v of %v, %v", busy, all, err)
	}
	if used, total, err := source.memory(); err != nil || total == 0 || used > total {
		t.Errorf("memory = %d of %d, %v", used, total, err)
	}
	if free, total, err := source.disk(); err != nil || total == 0 || free > total {
		t.Errorf("disk = %d free of %d, %v", free, total, err)
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
