package machinemetrics

import "testing"

func TestCPUMeterDerivesPercentFromTwoReadings(t *testing.T) {
	t.Parallel()
	var meter cpuMeter
	if meter.percent(10, 100) != nil {
		t.Error("the first reading has a percent")
	}
	if got := meter.percent(30, 200); got == nil || *got != 20 {
		t.Errorf("second = %v, want 20", got)
	}
	if meter.percent(30, 200) != nil {
		t.Error("a standstill gave a percent")
	}
	if meter.percent(10, 300) != nil {
		t.Error("counters going backwards gave a percent")
	}
	if got := meter.percent(500, 400); got == nil || *got != 100 {
		t.Errorf("clamp = %v, want 100", got)
	}
}
