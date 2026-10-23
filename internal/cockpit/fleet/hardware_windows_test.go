package fleet

import "testing"

func TestBootTimeIsReadFromTheTickCount(t *testing.T) {
	t.Parallel()
	if bootTime().IsZero() {
		t.Error("GetTickCount64 gave no boot time on Windows")
	}
}
