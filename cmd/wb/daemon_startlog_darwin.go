//go:build darwin

package main

import (
	"os"
	"path/filepath"
)

// daemonStartLogPath is where the supervisor's copy of the daemon's output
// goes.
//
// On darwin that is a launchd job whose stdout and stderr are absolute paths in
// a unit file WB writes once. It deliberately does not point into WB's runtime
// directory: a unit that pinned a home-derived log path kept a daemon writing
// into the directory a later WB_HOME move had abandoned — a 2.5 MB log in a
// home nothing else used. The user's own log directory does not move with
// WB_HOME.
func daemonStartLogPath(string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Logs", "wb", "daemon.log"), nil
}

// daemonStartLogIsResolvedRuntimePath reports whether the supervisor's log
// location is derived from the home, and therefore whether a unit that records
// it would pin a path a WB_HOME move can abandon.
func daemonStartLogIsResolvedRuntimePath() bool { return false }
