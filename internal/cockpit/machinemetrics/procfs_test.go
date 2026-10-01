package machinemetrics

import (
	"errors"
	"testing"
)

const (
	statFirst  = "cpu  100 0 100 700 100 0 0 0 5 0\ncpu0 1 2 3 4 5 6 7 8 9 10\n"
	statSecond = "cpu  150 0 150 800 100 0 0 0 5 0\n"
	loadText   = "0.52 0.40 0.30 1/200 1234\n"
	memText    = "MemTotal:       16000000 kB\nMemFree:  100 kB\nMemAvailable:    4000000 kB\nBuffers: 1 kB\nnocolon\nEmpty:\n"
)

func fakeProc(files map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		text, ok := files[path]
		if !ok {
			return nil, errors.New("no such file")
		}
		return []byte(text), nil
	}
}

func okDisk() (uint64, uint64, error) { return 40, 100, nil }

func TestProcSourceReadsAllFiveMetrics(t *testing.T) {
	t.Parallel()
	files := map[string]string{"/proc/stat": statFirst, "/proc/loadavg": loadText, "/proc/meminfo": memText}
	source := &procSource{readFile: fakeProc(files), disk: okDisk}
	first, err := source.Read()
	if err != nil {
		t.Fatal(err)
	}
	if first.CPUPercent != nil {
		t.Error("the first reading has a CPU percent, which needs two readings")
	}
	if first.Load1 != 0.52 || first.MemoryTotalBytes != 16000000*1024 || first.MemoryUsedBytes != 12000000*1024 || first.DiskFreeBytes != 40 || first.DiskTotalBytes != 100 {
		t.Errorf("first = %+v", first)
	}
	files["/proc/stat"] = statSecond
	second, err := source.Read()
	if err != nil || second.CPUPercent == nil || *second.CPUPercent != 50 {
		t.Fatalf("second = %+v, %v; want 50 percent (100 of 200 jiffies)", second, err)
	}
	// Counters that did not advance (or went backwards) give no percent.
	if third, _ := source.Read(); third.CPUPercent != nil {
		t.Errorf("a standstill gave %v percent", *third.CPUPercent)
	}
}

func TestProcSourceFailsOnAnyUnreadableOrMalformedPart(t *testing.T) {
	t.Parallel()
	good := map[string]string{"/proc/stat": statFirst, "/proc/loadavg": loadText, "/proc/meminfo": memText}
	for name, change := range map[string]func(map[string]string){
		"no stat":     func(f map[string]string) { delete(f, "/proc/stat") },
		"bad stat":    func(f map[string]string) { f["/proc/stat"] = "nothing" },
		"no loadavg":  func(f map[string]string) { delete(f, "/proc/loadavg") },
		"bad loadavg": func(f map[string]string) { f["/proc/loadavg"] = "" },
		"no meminfo":  func(f map[string]string) { delete(f, "/proc/meminfo") },
		"bad meminfo": func(f map[string]string) { f["/proc/meminfo"] = "MemTotal: 1 kB\n" },
	} {
		files := map[string]string{}
		for key, value := range good {
			files[key] = value
		}
		change(files)
		if _, err := (&procSource{readFile: fakeProc(files), disk: okDisk}).Read(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	failing := &procSource{readFile: fakeProc(good), disk: func() (uint64, uint64, error) { return 0, 0, errors.New("statfs") }}
	if _, err := failing.Read(); err == nil {
		t.Error("a failed statfs gave no error")
	}
}

func TestParsers(t *testing.T) {
	t.Parallel()
	if busy, all, err := parseCPUTimes("cpu 1 2 3 4 5\n"); err != nil || busy != 1+2+3 || all != 15 {
		t.Errorf("short cpu line = %d %d %v", busy, all, err)
	}
	for _, bad := range []string{"", "cpu 1 2\n", "cpu 1 x 3 4 5\n", "intr 1 2 3 4 5\n"} {
		if _, _, err := parseCPUTimes(bad); err == nil {
			t.Errorf("parseCPUTimes(%q) has no error", bad)
		}
	}
	for _, bad := range []string{"", "soon", "-1 0 0"} {
		if _, err := parseLoad1(bad); err == nil {
			t.Errorf("parseLoad1(%q) has no error", bad)
		}
	}
	for name, bad := range map[string]string{
		"junk number": "MemTotal: x kB\nMemAvailable: 1 kB\n",
		"no total":    "MemAvailable: 1 kB\n",
		"no avail":    "MemTotal: 1 kB\n",
		"avail>total": "MemTotal: 1 kB\nMemAvailable: 2 kB\n",
	} {
		if _, _, err := parseMemInfo(bad); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
