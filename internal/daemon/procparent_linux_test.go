//go:build linux

package daemon

import (
	"os"
	"testing"
)

// The test binary's own process is not started by systemd (the Go test
// runner execs it directly), so this exercises the real /proc read path
// end-to-end and asserts the one fact that always holds: it is answerable
// (known=true) and it is not systemd.
func TestObservedParentSupervisorReadsRealProc(t *testing.T) {
	kind, known := ObservedParentSupervisor(os.Getpid())
	if !known {
		t.Fatal("observing this test process's own parent must succeed on Linux")
	}
	if kind == SupervisorSystemd {
		t.Fatal("the go test binary is not a systemd unit")
	}
}

func TestObservedParentSupervisorRejectsInvalidPID(t *testing.T) {
	if _, known := ObservedParentSupervisor(0); known {
		t.Fatal("pid 0 must not be observable")
	}
	if _, known := ObservedParentSupervisor(-1); known {
		t.Fatal("a negative pid must not be observable")
	}
}

// A PID astronomically unlikely to exist proves the not-found path returns
// unknown rather than a false SupervisorNone.
func TestObservedParentSupervisorUnknownForMissingPID(t *testing.T) {
	if _, known := ObservedParentSupervisor(1 << 30); known {
		t.Fatal("a nonexistent pid must report unknown, not a supervisor kind")
	}
}
