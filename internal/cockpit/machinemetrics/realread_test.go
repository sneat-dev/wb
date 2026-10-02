//go:build linux || (darwin && e2e)

package machinemetrics

import (
	"testing"
	"time"
)

// readUntilCPU reads source until a reading has a CPU percent: the kernel's
// counters move in steps, so a second reading soon after the first may see none move.
func readUntilCPU(t *testing.T, source Source) Sample {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		sample, err := source.Read()
		if err != nil {
			t.Fatal(err)
		}
		if sample.CPUPercent != nil || time.Now().After(deadline) {
			return sample
		}
		time.Sleep(250 * time.Millisecond)
	}
}
