package fleet

import "testing"

func TestBootTimeIsReadFromTheKernel(t *testing.T) {
	t.Parallel()
	if bootTime().IsZero() {
		t.Error("kern.boottime is not readable on macOS")
	}
}
