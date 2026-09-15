//go:build !darwin

package main

import (
	"path/filepath"

	"github.com/sneat-dev/wb/internal/daemon"
)

// daemonStartLogPath is the daemon's own log file. Everywhere but darwin the
// launcher opens it and redirects the child's output into it, so no supervisor
// unit records the path at all.
func daemonStartLogPath(root string) (string, error) {
	return daemonLogPath(root)
}

// daemonLogPath is the daemon log inside the runtime directory this invocation
// resolves. It lives here rather than in the shared file because darwin does
// not use it: a unit file would pin that path across a WB_HOME move.
func daemonLogPath(root string) (string, error) {
	dir, err := daemon.RuntimeDir(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.log"), nil
}

// daemonStartLogIsResolvedRuntimePath reports whether the supervisor's log
// location is derived from the home. Everywhere but darwin there is no unit
// file to pin it in: the launcher opens the file itself.
func daemonStartLogIsResolvedRuntimePath() bool { return true }
