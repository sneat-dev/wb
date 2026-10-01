package machinemetrics

import "math"

// cpuMeter derives a CPU percent from two cumulative readings of busy and total
// time (jiffies or seconds, whatever the platform counts in). The first reading,
// a reading whose counters did not advance or went backwards, and one that is not
// a finite number, give none.
type cpuMeter struct {
	have      bool
	busy, all float64
}

// percent records the reading and returns the percent of time busy since the
// last one, or nil.
func (m *cpuMeter) percent(busy, all float64) *float64 {
	if !finite(busy) || !finite(all) {
		return nil
	}
	previousBusy, previousAll, had := m.busy, m.all, m.have
	m.have, m.busy, m.all = true, busy, all
	if !had || all <= previousAll || busy < previousBusy {
		return nil
	}
	percent := min(100, 100*(busy-previousBusy)/(all-previousAll))
	if !finite(percent) {
		return nil
	}
	return &percent
}

// finite reports whether value is neither NaN nor an infinity.
func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
