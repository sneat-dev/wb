//go:build e2e

package locallink

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2ECommittedWorkspaceGuardPreservesPhysicalRootFailure(t *testing.T) {
	t.Parallel()
	root := initRepository(t)
	lgCovWriteFile(t, filepath.Join(root, "backend", "go.mod"), "module github.com/acme/app/backend\n\ngo 1.27\n")
	lgCovWriteFile(t, filepath.Join(root, "go.work"), "go 1.27\n\nuse ./backend\n")
	runGuardTestGit(t, root, "add", "go.work", "backend/go.mod")
	runGuardTestGit(t, root, "-c", "user.name=wb", "-c", "user.email=wb@example.test", "commit", "-m", "record intrinsic workspace")
	retained := root + "-retained"
	t.Cleanup(func() { _ = os.RemoveAll(retained) })
	entries, err := unpublishedGoWorkEntriesWithEval(root, []string{"./backend"}, func(path string) (string, error) {
		if err := os.Rename(root, retained); err != nil {
			t.Fatal(err)
		}
		return filepath.EvalSymlinks(path)
	})
	if entries != nil || !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "resolve worktree") {
		t.Fatalf("guard error=%v %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(retained, "backend", "go.mod")); err != nil {
		t.Fatalf("original workspace lost: %v", err)
	}
}
