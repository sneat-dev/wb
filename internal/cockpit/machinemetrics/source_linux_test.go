package machinemetrics

import "testing"

// The real Linux reader: /proc and a statfs of a temporary directory.
func TestRealLinuxSourceReadsThisMachine(t *testing.T) {
	t.Parallel()
	source := NewSource(t.TempDir())
	if _, err := source.Read(); err != nil {
		t.Fatal(err)
	}
	sample, err := source.Read()
	if err != nil {
		t.Fatal(err)
	}
	if sample.MemoryTotalBytes == 0 || sample.MemoryUsedBytes > sample.MemoryTotalBytes || sample.DiskTotalBytes == 0 {
		t.Errorf("sample = %+v", sample)
	}
}
