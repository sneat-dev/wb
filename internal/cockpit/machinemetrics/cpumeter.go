package machinemetrics

// cpuMeter derives a CPU percent from two cumulative readings of busy and total
// time (jiffies or seconds, whatever the platform counts in). The first reading,
// and any reading whose counters did not advance, gives none.
type cpuMeter struct {
	have      bool
	busy, all float64
}

// percent records the reading and returns the percent of time busy since the
// last one, or nil.
func (m *cpuMeter) percent(busy, all float64) *float64 {
	previousBusy, previousAll, had := m.busy, m.all, m.have
	m.have, m.busy, m.all = true, busy, all
	if !had || all <= previousAll || busy < previousBusy {
		return nil
	}
	percent := min(100, 100*(busy-previousBusy)/(all-previousAll))
	return &percent
}
