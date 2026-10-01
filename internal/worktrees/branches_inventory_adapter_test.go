package worktrees

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBranchListAdapterRejectsSymlinkLoop(t *testing.T) {
	t.Parallel()
	loop := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if _, err := normalizeBranchListOptions(BranchListOptions{ProjectsRoot: loop}); err == nil {
		t.Fatal("symlink loop accepted")
	}
}
