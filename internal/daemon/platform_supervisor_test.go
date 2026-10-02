//go:build !linux

package daemon

import "testing"

func TestPlatformWithoutCgroupsReportsUnknownSupervisor(t *testing.T) {
	t.Parallel()
	if supervisor, known := ObservedCgroupSupervisor(1, "claimed-service"); supervisor != "" || known {
		t.Fatalf("unsupported observation=%q,%t", supervisor, known)
	}
	if unit, known := ObservedCgroupUnit(1); unit != "" || known {
		t.Fatalf("unsupported unit=%q,%t", unit, known)
	}
	if started, known := ProcessStartTime(1); !started.IsZero() || known {
		t.Fatalf("unsupported process start=%v,%t", started, known)
	}
}
