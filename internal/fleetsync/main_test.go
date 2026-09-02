package fleetsync

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain pins WB_HOME for the whole package.
//
// Several tests call Sync with an empty projects root, which resolves WB home
// to the developer's real ~/.wb. That was harmless while this package only
// read state; now that --prune-archived writes a deletion receipt before
// removing a clone, an unpinned test would deposit receipts in a real home.
// Pinning here rather than per-test means a future test cannot forget.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "fleetsync-wbhome-")
	if err != nil {
		panic(err)
	}
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	if err := os.Setenv("WB_HOME", home); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
