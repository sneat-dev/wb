//go:build e2e

package machinemetrics

import "testing"

// TestE2ERealLinuxSourceReadsThisMachine reads this machine through the real
// Linux reader (/proc and a statfs of a temporary directory) until a reading
// carries a CPU percent, which takes two readings a kernel tick apart. It waits
// on the machine's own clock, so it is not in the unit tier.
func TestE2ERealLinuxSourceReadsThisMachine(t *testing.T) {
	t.Parallel()
	sample := readUntilCPU(t, NewSource(t.TempDir()))
	if sample.CPUPercent == nil || *sample.CPUPercent < 0 || *sample.CPUPercent > 100 {
		t.Errorf("cpu_percent = %v after the readings", sample.CPUPercent)
	}
	if sample.MemoryTotalBytes == nil || sample.MemoryUsedBytes == nil || *sample.MemoryTotalBytes == 0 || *sample.MemoryUsedBytes > *sample.MemoryTotalBytes {
		t.Errorf("memory = %v of %v", sample.MemoryUsedBytes, sample.MemoryTotalBytes)
	}
	if sample.DiskTotalBytes == nil || sample.DiskFreeBytes == nil || *sample.DiskTotalBytes == 0 || *sample.DiskFreeBytes > *sample.DiskTotalBytes {
		t.Errorf("disk = %v free of %v", sample.DiskFreeBytes, sample.DiskTotalBytes)
	}
}
