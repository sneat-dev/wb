package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// pinDaemonHome keeps a test's daemon state inside its own fixture.
//
// The daemon derives its runtime directory — socket, lifecycle record, log,
// file-bridge keys, operation store — from WB's one home resolver. That
// resolver's default is the developer's real WB home, so a daemon test that
// does not pin WB_HOME creates sockets and lifecycle records in ~/.wb and can
// then observe, or collide with, a daemon that is genuinely running there.
//
// Pinning WB_HOME inside the fixture root restores what these tests assumed
// before the daemon consulted the resolver: the runtime directory is a child
// of the temporary projects root that the test owns and removes.
func pinDaemonHome(t *testing.T, root string) {
	t.Helper()
	t.Setenv(wbhome.EnvOverride, filepath.Join(root, ".wb"))
	t.Setenv(wbhome.EnvMigrationCompat, "")
}

// daemonTestRoot returns a fresh temporary projects root whose daemon runtime
// state stays inside it. See pinDaemonHome.
func daemonTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	pinDaemonHome(t, root)
	return root
}

// daemonTestService builds the daemon queue the way `wb daemon serve` does:
// the store location is resolved from the home this test pinned rather than by
// the constructor, which no longer reads the environment at all.
func daemonTestService(t *testing.T, root, build, generation string, authorizeRaw func() error) (*daemon.Service, error) {
	t.Helper()
	operationsDirectory, err := daemon.OperationsDir(root)
	if err != nil {
		return nil, err
	}
	return daemon.NewService(root, operationsDirectory, build, generation, authorizeRaw)
}

// mustDaemonPath resolves a daemon runtime path in tests, where an unexpected
// resolver failure is a test failure rather than a condition to report.
func mustDaemonPath(t *testing.T, resolve func(string) (string, error), root string) string {
	t.Helper()
	path, err := resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// daemonTestState builds a lifecycle record that identifies the fixture home,
// the way the daemon itself now does: a record without home identity cannot be
// shown to belong to this home, and status reports that as unverified rather
// than as ready.
func daemonTestState(t *testing.T, root, listen string, provenance daemon.Provenance, token string, now time.Time) daemon.State {
	t.Helper()
	return daemon.NewStartingAt(nil, listen, provenance, token,
		mustDaemonPath(t, func(string) (string, error) { return wbhome.Root(root) }, root),
		mustDaemonPath(t, daemonStatePath, root), now)
}
