package worktrees

import (
	"path/filepath"
	"testing"
)

// AbsoluteProjectsRoot resolves symlinks and refuses an empty root.
func TestAbsoluteProjectsRootResolvesAndRequiresARoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := AbsoluteProjectsRoot(dir); err != nil || got != want {
		t.Fatalf("AbsoluteProjectsRoot = (%q, %v), want %q", got, err, want)
	}
	if _, err := AbsoluteProjectsRoot(" "); err == nil {
		t.Fatal("AbsoluteProjectsRoot accepted an empty root")
	}
}
