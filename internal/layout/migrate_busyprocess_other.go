//go:build !linux

package layout

// busyProcessCheckSupported is false on every OS other than Linux: there is
// no equivalent of /proc/<pid>/cwd this package relies on elsewhere, so the
// check is skipped rather than silently reporting nothing found as if it had
// run. The migrate report notes this once per run instead.
const busyProcessCheckSupported = false

func busyProcessReason(paths []string) string { return "" }
