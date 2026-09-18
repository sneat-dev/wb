//go:build linux

package daemon

import (
	"os"
	"testing"
)

// The test binary's own process is not a systemd unit, so this exercises the
// real /proc read path end-to-end and asserts the one fact that always holds.
func TestObservedCgroupSupervisorReadsRealProc(t *testing.T) {
	kind, known := ObservedCgroupSupervisor(os.Getpid(), testWBUnit)
	if !known {
		t.Fatal("observing this test process's own cgroup must succeed on Linux")
	}
	if kind == SupervisorSystemd {
		t.Fatal("the go test binary does not run inside a systemd service unit")
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
