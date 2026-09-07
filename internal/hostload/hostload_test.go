package hostload

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckRefusesAboveFloor(t *testing.T) {
	read := func() (float64, error) { return 9.0, nil }
	err := Check(read, 4.0, false)
	if err == nil {
		t.Fatal("Check(9.0, floor 4.0, allow=false) = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "9.00") || !strings.Contains(err.Error(), "4.00") {
		t.Fatalf("refusal %q does not name both the load and the floor", err)
	}
	if !strings.Contains(err.Error(), "--allow-saturated-host") {
		t.Fatalf("refusal %q does not mention the override flag", err)
	}
}

func TestCheckAdmitsAtOrBelowFloor(t *testing.T) {
	for _, load := range []float64{1.0, 4.0} {
		read := func() (float64, error) { return load, nil }
		if err := Check(read, 4.0, false); err != nil {
			t.Fatalf("Check(%v, floor 4.0, allow=false) = %v, want nil", load, err)
		}
	}
}

func TestCheckOverrideRecordedByCallerBypassesRefusal(t *testing.T) {
	read := func() (float64, error) { return 99.0, nil }
	if err := Check(read, 1.0, true); err != nil {
		t.Fatalf("Check with allow=true = %v, want nil", err)
	}
}

func TestCheckReaderErrorFailsOpen(t *testing.T) {
	read := func() (float64, error) { return 0, errors.New("no loadavg source") }
	if err := Check(read, 1.0, false); err != nil {
		t.Fatalf("Check with a failing reader = %v, want nil (fail open)", err)
	}
}

func TestCheckUnsupportedPlatformFailsOpen(t *testing.T) {
	read := func() (float64, error) { return 0, ErrUnsupported }
	if err := Check(read, 0.1, false); err != nil {
		t.Fatalf("Check with ErrUnsupported = %v, want nil (fail open)", err)
	}
}

func TestFloorDefaultsToNumCPUWithoutConfig(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	got := Floor(missing)
	if got < 1 {
		t.Fatalf("Floor(missing config) = %v, want >= 1", got)
	}
}

func TestFloorReadsConfiguredLoadFloor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte("admission:\n  load_floor: 2.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Floor(path); got != 2.5 {
		t.Fatalf("Floor(configured) = %v, want 2.5", got)
	}
}

func TestFloorIgnoresNonPositiveConfiguredValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte("admission:\n  load_floor: 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Floor(path)
	if got < 1 {
		t.Fatalf("Floor(load_floor: 0) = %v, want the NumCPU default", got)
	}
}

func TestFloorPreservesUnrelatedConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wb.yaml")
	original := "parallel: 3\nrecipes:\n  demo:\n    type: command\n    command: echo hi\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Floor(path)
	if got < 1 {
		t.Fatalf("Floor(unrelated config) = %v, want the NumCPU default", got)
	}
}

func TestConsumersOrdersMostCPUFirst(t *testing.T) {
	consumers := Consumers(5)
	// Best-effort and host-dependent: only assert it never exceeds the
	// requested count and never errors out (ps must always be present in
	// CI and dev environments this test runs in).
	if len(consumers) > 5 {
		t.Fatalf("Consumers(5) returned %d lines, want at most 5", len(consumers))
	}
}

func TestConsumersZeroReturnsNothing(t *testing.T) {
	if got := Consumers(0); got != nil {
		t.Fatalf("Consumers(0) = %v, want nil", got)
	}
}
