// Package buildinfo resolves the running WB binary's version so that any
// package can record it, not just the command layer.
//
// The version lives in three places depending on how the binary was produced:
// a release build stamps it via -ldflags, a `go install` of the module leaves
// it in the embedded build info, and an ad-hoc build has neither. Resolving
// that order in one place keeps `wb version` and the provenance WB writes into
// worktrees from ever disagreeing about which binary is running.
package buildinfo

import (
	"runtime/debug"
	"sync"
)

// Unknown is reported when no version can be resolved at all.
const Unknown = "unknown"

var (
	mu      sync.RWMutex
	stamped string
)

// Set records the link-time version. The command layer calls it during
// start-up so the release stamp takes precedence over embedded build info.
func Set(v string) {
	mu.Lock()
	defer mu.Unlock()
	stamped = v
}

// Version returns the link-time stamp, else the module version the Go
// toolchain embedded, else Unknown. It never returns an empty string, so
// callers can record it unconditionally.
func Version() string {
	mu.RLock()
	v := stamped
	mu.RUnlock()
	if v != "" {
		return v
	}
	if build, ok := debug.ReadBuildInfo(); ok && build.Main.Version != "" {
		return build.Main.Version
	}
	return Unknown
}
