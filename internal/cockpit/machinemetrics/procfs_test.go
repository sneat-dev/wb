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
	if *first.Load1 != 0.52 || *first.MemoryTotalBytes != 16000000*1024 || *first.MemoryUsedBytes != 12000000*1024 || *first.DiskFreeBytes != 40 || *first.DiskTotalBytes != 100 {
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

// TestProcSourceKeepsWhatItCouldReadWhenAPartFails proves each part is read on its
// own: a failing part is absent and its error returned, and the rest stays.
func TestProcSourceKeepsWhatItCouldReadWhenAPartFails(t *testing.T) {
	t.Parallel()
	good := map[string]string{"/proc/stat": statFirst, "/proc/loadavg": loadText, "/proc/meminfo": memText}
	for name, test := range map[string]struct {
		file   string
		bad    string // "" deletes the file
		absent func(Sample) bool
	}{
		"no stat":     {"/proc/stat", "", func(s Sample) bool { return s.CPUPercent == nil && s.Load1 != nil }},
		"bad stat":    {"/proc/stat", "nothing", func(s Sample) bool { return s.Load1 != nil }},
		"no loadavg":  {"/proc/loadavg", "", func(s Sample) bool { return s.Load1 == nil && s.MemoryTotalBytes != nil }},
		"bad loadavg": {"/proc/loadavg", "nan", func(s Sample) bool { return s.Load1 == nil }},
		"no meminfo":  {"/proc/meminfo", "", func(s Sample) bool { return s.MemoryUsedBytes == nil && s.Load1 != nil }},
		"bad meminfo": {"/proc/meminfo", "MemTotal: 1 kB\n", func(s Sample) bool { return s.MemoryTotalBytes == nil }},
	} {
		files := map[string]string{}
		for key, value := range good {
			files[key] = value
		}
		if test.bad == "" {
			delete(files, test.file)
		} else {
			files[test.file] = test.bad
		}
		sample, err := (&procSource{readFile: fakeProc(files), disk: okDisk}).Read()
		if err == nil || !sample.HasData() || !test.absent(sample) {
			t.Errorf("%s: err %v, sample %+v", name, err, sample)
		}
	}
	failing := &procSource{readFile: fakeProc(good), disk: func() (uint64, uint64, error) { return 0, 0, errors.New("statfs") }}
	if sample, err := failing.Read(); err == nil || sample.DiskTotalBytes != nil || sample.Load1 == nil {
		t.Errorf("a failed statfs: %+v, %v", sample, err)
	}
	if sample, err := (&procSource{readFile: fakeProc(nil), disk: func() (uint64, uint64, error) { return 0, 0, errors.New("x") }}).Read(); err == nil || sample.HasData() {
		t.Errorf("nothing readable gave %+v, %v", sample, err)
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
	for _, bad := range []string{"", "soon", "-1 0 0", "NaN 0 0", "+Inf 0 0"} {
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
