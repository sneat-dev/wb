//go:build !linux && !darwin

package session

// parentPID has no portable answer here, so a session simply goes unresolved,
// which degrades to the same behaviour as never having registered.
func parentPID(int) (int, bool) { return 0, false }
