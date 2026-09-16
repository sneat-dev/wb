//go:build linux

package wbhome

// resolveAbs only fails on filepath.Abs, and filepath.Abs only fails when
// os.Getwd fails. Linux is the platform on which removing the process's own
// working directory reliably makes getcwd(2) return ENOENT; darwin keeps the
// unlinked directory's vnode reachable, so the branch cannot be reached there.
// The file is Linux-only rather than a test that skips.

import (
	"os"
	"testing"
)

// TestTailCovResolveAbsRefusesARelativePathWithoutAWorkingDirectory pins that
// resolveAbs never silently falls back to an unresolved relative path.
func TestTailCovResolveAbsRefusesARelativePathWithoutAWorkingDirectory(t *testing.T) {
	working := t.TempDir()
	t.Chdir(working)
	if err := os.RemoveAll(working); err != nil {
		t.Fatalf("remove the working directory: %v", err)
	}

	resolved, err := resolveAbs("relative-home")
	if err == nil {
		t.Fatalf("resolveAbs = %q, want an error with no readable working directory", resolved)
	}
}
