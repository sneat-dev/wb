//go:build e2e

package quality

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestE2EComputeBaselineRejectsUnreadableSelectedPackage(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", "package app\n")
	base := repo.commitAll("base")
	// A real checked-out file produces a directory-read failure independent
	// of permissions or user privileges. It must never become an empty,
	// successful baseline for the selected scope.
	baseline, err := ComputeBaselineAtRef(t.Context(), repo.dir, base, time.Minute,
		RunOptions{GoTestPackages: []string{"./go.mod"}})
	var pathError *os.PathError
	if !errors.As(err, &pathError) || !strings.Contains(err.Error(), "resolve coverage package ./go.mod") {
		t.Fatalf("error=%v, want the baseline package directory read failure", err)
	}
	if baseline.SHA != "" || baseline.Packages != nil {
		t.Fatalf("unreadable selected package produced a baseline: %+v", baseline)
	}
	if worktrees := repo.runGit("worktree", "list", "--porcelain"); strings.Count(worktrees, "worktree ") != 1 {
		t.Fatalf("failed baseline retained its temporary worktree: %s", worktrees)
	}
}
