package session

import (
	"os"
	"testing"
)

// Register with no Machine set must resolve and fill it from the real
// hostname (os.Hostname always succeeds under the unit tier; no process is
// started).
func TestRWI08RegisterFillsMachineFromHostname(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	record, err := Register(dir, Record{PID: 4242})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	wantHostname, err := os.Hostname()
	if err != nil {
		t.Skipf("os.Hostname unavailable in this environment: %v", err)
	}
	if record.Machine != wantHostname {
		t.Fatalf("record.Machine = %q, want hostname %q", record.Machine, wantHostname)
	}
}
