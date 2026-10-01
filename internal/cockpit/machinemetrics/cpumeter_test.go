package machinemetrics

import (
	"math"
	"testing"
)

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
	for name, reading := range map[string][2]float64{"NaN busy": {math.NaN(), 600}, "Inf all": {600, math.Inf(1)}, "NaN all": {1, math.NaN()}} {
		if meter.percent(reading[0], reading[1]) != nil {
			t.Errorf("%s gave a percent", name)
		}
	}
	// A non-finite reading is ignored, so it does not poison the next one.
	if got := meter.percent(600, 800); got == nil || *got != 25 {
		t.Errorf("after bad readings = %v, want 25", got)
	}
	// Finite readings whose difference overflows give none, not NaN.
	var wide cpuMeter
	wide.percent(0, -math.MaxFloat64)
	if wide.percent(math.MaxFloat64, math.MaxFloat64) != nil {
		t.Error("an overflowing reading gave a percent")
	}
	if !finite(1) || finite(math.Inf(-1)) {
		t.Error("finite is wrong")
	}
}

func ptr[T any](value T) *T { return &value }
