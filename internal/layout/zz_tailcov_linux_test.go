//go:build linux

package layout

// This file covers the three filepath.Abs failure branches in absoluteRoot and
// removeContainedPath. filepath.Abs fails only when os.Getwd fails, and Linux
// is the platform on which removing the process's own working directory
// reliably makes getcwd(2) return ENOENT. Darwin keeps the unlinked
// directory's vnode reachable, so the same fixture resolves successfully and
// the branch is unreachable there; the file is therefore Linux-only rather
// than a test that skips.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTailCovAbsolutePathsFailClosedWithoutAWorkingDirectory pins that a
// projects root (or a removal target) that cannot be made absolute is refused
// rather than silently treated as relative to an unknown directory.
func TestTailCovAbsolutePathsFailClosedWithoutAWorkingDirectory(t *testing.T) {
	absoluteTarget := filepath.Join(t.TempDir(), "target")

	working := t.TempDir()
	t.Chdir(working)
	if err := os.RemoveAll(working); err != nil {
		t.Fatalf("remove the working directory: %v", err)
	}

	if _, err := absoluteRoot("relative-root"); err == nil {
		t.Fatal("absoluteRoot resolved a relative root with no readable working directory")
	}
	if err := removeContainedPath("relative-root", absoluteTarget); err == nil {
		t.Fatal("removeContainedPath resolved a relative root with no readable working directory")
	}
	if err := removeContainedPath(absoluteTarget, "relative-target"); err == nil {
		t.Fatal("removeContainedPath resolved a relative target with no readable working directory")
	}
}
