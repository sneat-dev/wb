//go:build !linux && !darwin && !windows

package fleet

import "testing"

func TestBootTimeIsNotReadOnThisSystem(t *testing.T) {
	t.Parallel()
	if !bootTime().IsZero() {
		t.Error("boot time is read where no reader exists")
	}
}
