package hostload

// Helpers kept for tests only: no production caller remains.

// Floor resolves the load-average ceiling above which new CPU-heavy work is
// refused, discarding the disablement reason. Use Resolve when the reason
// needs to be recorded (e.g. on a receipt or runlog event).
func Floor(configPath string) float64 {
	floor, _ := Resolve(configPath)
	return floor
}
