package machinemetrics

import "testing"

// The real macOS reader: read-only sysctls and a statfs of a temporary directory.
func TestRealMacSourceReadsThisMachine(t *testing.T) {
	t.Parallel()
	sample, err := NewSource(t.TempDir()).Read()
	if err != nil {
		t.Fatal(err)
	}
	if sample.CPUPercent != nil || sample.MemoryTotalBytes == 0 || sample.MemoryUsedBytes > sample.MemoryTotalBytes || sample.DiskTotalBytes == 0 || sample.Load1 < 0 {
		t.Errorf("sample = %+v", sample)
	}
}
