//go:build linux

package checkoutmarker

// This file covers the filepath.Abs failure branch in Describe. filepath.Abs
// fails only when os.Getwd fails, and Linux is the platform on which removing
// the process's own working directory reliably makes getcwd(2) return ENOENT.
// On darwin the kernel keeps the unlinked directory's vnode reachable, so the
// same fixture resolves successfully and the branch is unreachable there; the
// file is therefore Linux-only rather than a test that skips.

import (
	"os"
	"strings"
	"testing"
)

// TestTailCovDescribeReportsAPathItCannotResolve pins that Describe reports a
// path it cannot make absolute instead of proceeding with a relative one.
func TestTailCovDescribeReportsAPathItCannotResolve(t *testing.T) {
	working := t.TempDir()
	t.Chdir(working)
	if err := os.RemoveAll(working); err != nil {
		t.Fatalf("remove the working directory: %v", err)
	}

	_, err := Describe("relative-checkout", DescribeOptions{ProjectsRoot: working})
	if err == nil {
		t.Fatal("Describe resolved a relative path with no readable working directory")
	}
	if !strings.Contains(err.Error(), "resolve ") {
		t.Fatalf("error %q does not name the failed path resolution", err)
	}
}
