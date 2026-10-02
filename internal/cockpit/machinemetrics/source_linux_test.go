package machinemetrics

import "testing"

// TestLinuxSourceReadsProcAndTheDiskOfItsRoot checks the wiring NewSource does,
// with no read of the machine's counters (those wait on the kernel's clock and
// are read in TestE2ERealLinuxSourceReadsThisMachine): the source is a /proc
// reader whose disk is the one under the root it was given.
func TestLinuxSourceReadsProcAndTheDiskOfItsRoot(t *testing.T) {
	t.Parallel()
	source, ok := NewSource(t.TempDir()).(*procSource)
	if !ok || source.readFile == nil || source.disk == nil {
		t.Fatalf("NewSource = %#v, want a /proc source with a file reader and a disk reader", source)
	}
	free, total, err := source.disk()
	if err != nil || total == 0 || free > total {
		t.Errorf("disk of the root = %d free of %d (%v)", free, total, err)
	}
}
