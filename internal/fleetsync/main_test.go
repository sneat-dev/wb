package fleetsync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// TestMain pins WB_PROJECTS_ROOT for the whole package.
//
// Several tests call Sync with an empty projects root, which resolves WB home
// to the developer's real projects root. That was harmless while this package
// only read state; now that --prune-archived writes a deletion receipt before
// removing a clone, an unpinned test would deposit receipts in a real home.
// Pinning here rather than per-test means a future test cannot forget.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "fleetsync-wbroot-")
	if err != nil {
		panic(err)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if err := os.Setenv("WB_PROJECTS_ROOT", root); err != nil {
		panic(err)
	}
	// Disable git's detached gc/maintenance for every git this binary
	// starts, including Sync's own pulls and clones against t.TempDir()
	// fixtures, so no background writer can race TempDir cleanup (task-21).
	testenv.GitAutoMaintenanceOffProcess()
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
