//go:build linux

package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessStartTimeFromProcRootRequiresBothRecords(t *testing.T) {
	t.Parallel()
	const validProcess = "1234 (my (odd) name) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 250\n"
	const validSystem = "cpu 1 2 3\nbtime 1700000000\nprocesses 7\n"
	cases := []struct {
		name, process, system string
		pid                   int
		processMissing        bool
		systemMissing         bool
		processDirectory      bool
		systemDirectory       bool
		known                 bool
	}{
		{name: "zero pid", pid: 0, process: validProcess, system: validSystem},
		{name: "negative pid", pid: -1, process: validProcess, system: validSystem},
		{name: "missing process", pid: 1234, processMissing: true, system: validSystem},
		{name: "unreadable process", pid: 1234, processDirectory: true, system: validSystem},
		{name: "malformed process", pid: 1234, process: "1234 (wb) S 1 2", system: validSystem},
		{name: "invalid ticks", pid: 1234, process: "1234 (wb) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 invalid", system: validSystem},
		{name: "missing system", pid: 1234, process: validProcess, systemMissing: true},
		{name: "unreadable system", pid: 1234, process: validProcess, systemDirectory: true},
		{name: "missing boot time", pid: 1234, process: validProcess, system: "cpu 1 2 3\n"},
		{name: "invalid boot time", pid: 1234, process: validProcess, system: "btime invalid\n"},
		{name: "valid records", pid: 1234, process: validProcess, system: validSystem, known: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			processPath := filepath.Join(root, "1234", "stat")
			if err := os.MkdirAll(filepath.Dir(processPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.processDirectory {
				if err := os.Mkdir(processPath, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if !tc.processMissing {
				if err := os.WriteFile(processPath, []byte(tc.process), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			systemPath := filepath.Join(root, "stat")
			if tc.systemDirectory {
				if err := os.Mkdir(systemPath, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if !tc.systemMissing {
				if err := os.WriteFile(systemPath, []byte(tc.system), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			started, known := processStartTimeFromProcRoot(root, tc.pid)
			want := time.Time{}
			if tc.known {
				want = time.Unix(1700000002, 500000000).UTC()
			}
			if known != tc.known || !started.Equal(want) {
				t.Fatalf("process start = (%s, %t), want (%s, %t)", started, known, want, tc.known)
			}
		})
	}
}

func TestProcessStartTimeReadsCurrentProcess(t *testing.T) {
	t.Parallel()
	before := time.Now()
	started, known := ProcessStartTime(os.Getpid())
	if !known || started.IsZero() || started.After(before) {
		t.Fatalf("current process start = (%s, %t), want a known time before %s", started, known, before)
	}
}
