package fleet

import (
	"errors"
	"testing"
	"time"
)

func TestBootTimeIsReadFromProcStat(t *testing.T) {
	t.Parallel()
	if bootTime().IsZero() {
		t.Error("/proc/stat has no btime on Linux")
	}
}

func TestBootTimeIsZeroWhenProcStatCannotBeRead(t *testing.T) { //nolint:paralleltest // it replaces a package variable
	read := readStat
	t.Cleanup(func() { readStat = read })
	readStat = func() ([]byte, error) { return nil, errors.New("no /proc") }
	if !bootTime().IsZero() {
		t.Error("an unreadable /proc/stat gave a boot time")
	}
}

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
