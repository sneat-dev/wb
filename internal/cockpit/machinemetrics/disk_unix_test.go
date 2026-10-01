//go:build linux || darwin

package machinemetrics

import "testing"

func TestDiskUsageOfADirectory(t *testing.T) {
	t.Parallel()
	free, total, err := diskUsage(t.TempDir())
	if err != nil || total == 0 || free > total {
		t.Errorf("free %d, total %d, %v", free, total, err)
	}
	if _, _, err := diskUsage(t.TempDir() + "/missing"); err == nil {
		t.Error("a missing directory gave no error")
	}
}
