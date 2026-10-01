package fleet

import (
	"runtime"
	"testing"
	"time"
)

func TestParseBootTimeReadsTheBtimeLine(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		stat string
		want time.Time
	}{
		"a real line":  {"cpu 1 2 3\nbtime 1700000000\nprocesses 5\n", time.Unix(1700000000, 0).UTC()},
		"padded":       {"btime  1700000000 \n", time.Unix(1700000000, 0).UTC()},
		"none":         {"cpu 1 2 3\n", time.Time{}},
		"not a number": {"btime soon\n", time.Time{}},
		"zero":         {"btime 0\n", time.Time{}},
		"empty":        {"", time.Time{}},
	} {
		if got := parseBootTime(test.stat); !got.Equal(test.want) {
			t.Errorf("%s: %v, want %v", name, got, test.want)
		}
	}
}

func TestLocalHardwareNamesThisMachine(t *testing.T) {
	t.Parallel()
	hardware := LocalHardware()
	if hardware.OS != runtime.GOOS || hardware.Arch != runtime.GOARCH || hardware.CPUCount != runtime.NumCPU() {
		t.Errorf("hardware = %+v", hardware)
	}
	if booted := bootTime(); !booted.IsZero() && (booted.After(time.Now()) || booted.Before(time.Now().AddDate(-5, 0, 0))) {
		t.Errorf("boot time = %v, want a time in the past five years or none", booted)
	}
}
