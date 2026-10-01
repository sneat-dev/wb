package fleet

import "testing"

func TestBootTimeIsReadFromProcStat(t *testing.T) {
	t.Parallel()
	if bootTime().IsZero() {
		t.Error("/proc/stat has no btime on Linux")
	}
}
