//go:build !linux && !darwin

package session

// parentPID has no portable answer here, so a session simply goes unresolved,
// which degrades to the same behaviour as never having registered.
func parentPID(int) (int, bool) { return 0, false }

// processName has no portable answer either, so a park-time registration falls
// back to an Unknown runtime rather than to a guess.
func processName(int) string { return "" }
