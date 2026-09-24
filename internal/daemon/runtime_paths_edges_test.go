package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRuntimePathHelpersReportResolutionFailureWhenCurrentDirectoryIsGone
// drives StatePath's, OperationsDir's and SocketPath's shared
// "if err != nil { return "", err }" branch after RuntimeDir (runtime.go):
// RuntimeDir's own error return comes from wbhome.Root -> Resolve ->
// projectsRootAbs -> filepath.Abs, which can only fail (for a relative
// projectsRoot) when os.Getwd itself fails -- reproduced here, without any
// new production seam, by chdir-ing into a scratch directory this test then
// removes out from under the process before calling each helper with a
// relative projectsRoot.
//
// Not run in parallel: t.Chdir is process-wide (and panics if combined with
// t.Parallel).
func TestRuntimePathHelpersReportResolutionFailureWhenCurrentDirectoryIsGone(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	const relativeRoot = "relative/projects/root"
	const wantSubstring = "resolve WB home for daemon runtime"

	if _, err := StatePath(relativeRoot); err == nil || !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("StatePath(relative root, no cwd) = %v, want a %q error", err, wantSubstring)
	}
	if _, err := OperationsDir(relativeRoot); err == nil || !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("OperationsDir(relative root, no cwd) = %v, want a %q error", err, wantSubstring)
	}
	if _, err := SocketPath(relativeRoot); err == nil || !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("SocketPath(relative root, no cwd) = %v, want a %q error", err, wantSubstring)
	}
}
