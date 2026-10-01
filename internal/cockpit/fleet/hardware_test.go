package fleet

import (
	"runtime"
	"testing"
	"time"
)

func TestLocalHardwareNamesThisMachine(t *testing.T) {
	t.Parallel()
	hardware := LocalHardware()
	if hardware.OS != runtime.GOOS || hardware.Arch != runtime.GOARCH || hardware.CPUCount != runtime.NumCPU() {
		t.Errorf("hardware = %+v", hardware)
	}
	if booted := bootTime(); !booted.IsZero() && (booted.After(time.Now()) || booted.Before(time.Now().AddDate(-5, 0, 0))) {
		t.Errorf("boot time = %v, want a time in the past five years or none", booted)
	}
}
