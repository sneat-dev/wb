package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// pinDaemonHome keeps a test's daemon state inside its own fixture.
//
// The daemon derives its runtime directory — socket, lifecycle record, log,
// file-bridge keys, operation store — from WB's one home resolver, and that
// home is <root>/.wb for the projects root. Pinning WB_PROJECTS_ROOT to the
// fixture root makes every call that passes no root resolve the same
// directory, so a daemon test never creates sockets or lifecycle records in
// the developer's real home.
func pinDaemonHome(t *testing.T, root string) {
	t.Helper()
	t.Setenv(wbhome.EnvOverride, root)
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

// daemonLegacyFixture builds the retired default state home $HOME/.wb with a
// runtime directory a leftover daemon would still serve, and returns that
// runtime directory. Under the one-root schema the current home is
// <root>/.wb, so $HOME/.wb is the location that can actually differ from it.
//
// The resolver reports $HOME/.wb as a legacy read layout only when it holds a
// worktrees root, so the fixture plants that marker too. HOME sits directly
// under /tmp because the legacy endpoint is a filesystem socket with a
// platform path-length limit.
func daemonLegacyFixture(t *testing.T) string {
	t.Helper()
	legacyParent, err := os.MkdirTemp("/tmp", "wb-legacy-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(legacyParent) })
	if resolved, resolveErr := filepath.EvalSymlinks(legacyParent); resolveErr == nil {
		legacyParent = resolved
	}
	t.Setenv("HOME", legacyParent)
	legacyHome := filepath.Join(legacyParent, ".wb")
	if err := os.MkdirAll(filepath.Join(legacyHome, "worktrees", "legacy-task", "acme", "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	legacyDir := filepath.Join(legacyHome, daemon.RuntimeDirName)
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return legacyDir
}

// daemonLegacyStatePath is the lifecycle record a leftover daemon would have
// written in the legacy runtime directory.
func daemonLegacyStatePath(legacyDir string) string {
	return filepath.Join(legacyDir, daemon.StateFileName)
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
