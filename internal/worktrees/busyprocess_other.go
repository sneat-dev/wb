//go:build !linux

package worktrees

// BusyProcessCheckSupported is false on every OS other than Linux: there is
// no equivalent of /proc/<pid>/cwd this package relies on elsewhere, so the
// check is skipped rather than silently reporting nothing found as if it had
// run. Callers note this once per run instead.
const BusyProcessCheckSupported = false

// BusyProcessReason is a no-op where BusyProcessCheckSupported is false.
func BusyProcessReason(paths []string) string { return "" }
