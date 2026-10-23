package hostload

// Helpers kept for tests only: no production caller remains.

// Floor resolves the load-average ceiling above which new CPU-heavy work is
// refused, discarding the disablement reason. Use Resolve when the reason
// needs to be recorded (e.g. on a receipt or runlog event).
func Floor(configPath string) float64 {
	floor, _ := Resolve(configPath)
	return floor
}

// Disabled reports whether host-load admission is turned off entirely, and
// why. See Resolve for the reason values.
func Disabled(configPath string) (bool, string) {
	floor, reason := Resolve(configPath)
	return floor <= 0, reason
}
