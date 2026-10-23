//go:build linux

package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestObservedCgroupSupervisorReadsRealProc(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(os.Getpid()), "cgroup"))
	var wantKind Supervisor
	var wantKnown bool
	if err == nil {
		wantKind, wantKnown = ParseCgroupSupervisor(string(data), testWBUnit)
	}
	kind, known := ObservedCgroupSupervisor(os.Getpid(), testWBUnit)
	if kind != wantKind || known != wantKnown {
		t.Fatalf("current process supervisor = (%q, %t), want (%q, %t); independent read error: %v", kind, known, wantKind, wantKnown, err)
	}
}

func TestObservedCgroupSupervisorRejectsInvalidPID(t *testing.T) {
	if _, known := ObservedCgroupSupervisor(0, testWBUnit); known {
		t.Fatal("pid 0 must not be observable")
	}
	if _, known := ObservedCgroupSupervisor(-1, testWBUnit); known {
		t.Fatal("a negative pid must not be observable")
	}
}

func TestObservedCgroupSupervisorUnknownForMissingPID(t *testing.T) {
	if _, known := ObservedCgroupSupervisor(1<<30, testWBUnit); known {
		t.Fatal("a nonexistent pid must report unknown, not a supervisor kind")
	}
}

func TestReadCgroupFromProcRootPreservesEvidence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, contents, unit string
		pid                  int
		missing, directory   bool
		readable, known      bool
		supervisor           Supervisor
	}{
		{name: "zero pid", pid: 0, contents: "0::/system.slice/wb-daemon.service\n"},
		{name: "negative pid", pid: -1, contents: "0::/system.slice/wb-daemon.service\n"},
		{name: "missing file", pid: 1234, missing: true},
		{name: "unreadable file", pid: 1234, directory: true},
		{name: "empty file", pid: 1234, readable: true},
		{name: "malformed file", pid: 1234, contents: "invalid\n", readable: true},
		{name: "legacy hierarchy", pid: 1234, contents: "1:name=systemd:/system.slice/wb-daemon.service\n", readable: true},
		{name: "own unit", pid: 1234, contents: "0::/system.slice/wb-daemon.service\n", readable: true, known: true, unit: testWBUnit, supervisor: SupervisorSystemd},
		{name: "foreign unit", pid: 1234, contents: "0::/system.slice/foreign.service\n", readable: true, known: true, unit: "foreign.service", supervisor: SupervisorNone},
		{name: "session scope", pid: 1234, contents: "0::/user.slice/session-3.scope\n", readable: true, known: true, supervisor: SupervisorNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "1234", "cgroup")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.directory {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if !tc.missing {
				if err := os.WriteFile(path, []byte(tc.contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			contents, readable := readCgroupFromProcRoot(root, tc.pid)
			wantContents := ""
			if tc.readable {
				wantContents = tc.contents
			}
			if contents != wantContents || readable != tc.readable {
				t.Fatalf("cgroup evidence = (%q, %t), want (%q, %t)", contents, readable, wantContents, tc.readable)
			}
			unit, known := ParseCgroupUnit(contents)
			if unit != tc.unit || known != tc.known {
				t.Fatalf("observed unit = (%q, %t), want (%q, %t)", unit, known, tc.unit, tc.known)
			}
			kind, known := ParseCgroupSupervisor(contents, testWBUnit)
			if kind != tc.supervisor || known != tc.known {
				t.Fatalf("observed supervisor = (%q, %t), want (%q, %t)", kind, known, tc.supervisor, tc.known)
			}
		})
	}
}

func TestObservedCgroupUnitRequiresObservablePID(t *testing.T) {
	t.Parallel()
	for _, pid := range []int{0, -1, 1 << 30} {
		if unit, known := ObservedCgroupUnit(pid); unit != "" || known {
			t.Fatalf("pid %d unit = (%q, %t), want unknown", pid, unit, known)
		}
	}
}

func TestObservedCgroupUnitReadsCurrentProcess(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(os.Getpid()), "cgroup"))
	if err != nil {
		t.Fatal(err)
	}
	wantUnit, wantKnown := ParseCgroupUnit(string(data))
	unit, known := ObservedCgroupUnit(os.Getpid())
	if unit != wantUnit || known != wantKnown {
		t.Fatalf("current process unit = (%q, %t), want (%q, %t)", unit, known, wantUnit, wantKnown)
	}
}
