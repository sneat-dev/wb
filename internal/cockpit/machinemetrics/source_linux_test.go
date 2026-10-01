package machinemetrics

import "testing"

// The real Linux reader: /proc and a statfs of a temporary directory.
func TestRealLinuxSourceReadsThisMachine(t *testing.T) {
	t.Parallel()
	source := NewSource(t.TempDir())
	sample := readUntilCPU(t, source)
	if sample.MemoryTotalBytes == nil || *sample.MemoryTotalBytes == 0 || *sample.MemoryUsedBytes > *sample.MemoryTotalBytes || sample.DiskTotalBytes == nil || *sample.DiskTotalBytes == 0 || sample.CPUPercent == nil {
		t.Errorf("sample = %+v", sample)
	}
}
