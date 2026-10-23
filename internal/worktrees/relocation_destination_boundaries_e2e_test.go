//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

//nolint:paralleltest // newGitFixture and configureFixtureSharedWorktrees set process-wide WB and Git environment.
func TestE2EPlanRelocationRefusesDirtyAndOccupiedDestination(t *testing.T) {
	fixture := newGitFixture(t)
	configureFixtureSharedWorktrees(t, fixture)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "relocation-plan-boundary", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := List(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocation-plan-boundary"})
	if err != nil || len(listed) != 1 {
		t.Fatalf("list=%+v, err=%v", listed, err)
	}
	if listed[0].WorktreeDir != created[0].WorktreeDir {
		t.Fatal("fixture listed a different checkout")
	}
	resolution, err := wbhome.Resolve(fixture.projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	options := RelocateOptions{ProjectsRoot: fixture.projectsRoot, To: "local"}
	dirty := listed[0]
	dirty.Clean = false
	result, err := planRelocation(context.Background(), resolution, options, dirty)
	if err != nil || result.Eligible || !strings.Contains(result.Reason, "local changes") {
		t.Fatalf("dirty plan=%+v, err=%v", result, err)
	}
	destination := filepath.Join(fixture.canonical, ".worktrees", "relocation-plan-boundary")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	result, err = planRelocation(context.Background(), resolution, options, listed[0])
	if err != nil || result.Eligible || !strings.Contains(result.Reason, "destination already exists") {
		t.Fatalf("occupied plan=%+v, err=%v", result, err)
	}
}

//nolint:paralleltest // newGitFixture sets process-wide HOME, XDG_CONFIG_HOME, and WB_PROJECTS_ROOT for real Git.
func TestE2EPrepareLocalRelocationDestinationFailures(t *testing.T) {
	fixture := newGitFixture(t)
	entry := ListResult{CanonicalDir: fixture.canonical}
	local := filepath.Join(fixture.canonical, ".worktrees", "test-task")
	if err := prepareRelocationDestination(context.Background(), ListResult{CanonicalDir: filepath.Join(t.TempDir(), "missing")}, "HEAD", local, "", "local"); err == nil {
		t.Fatal("missing canonical clone was accepted")
	}
	if err := prepareRelocationDestination(context.Background(), entry, "not-a-revision", local, "", "local"); err == nil {
		t.Fatal("invalid base revision was accepted")
	}
	if _, _, err := prepareRelocationMove(context.Background(), fixture.projectsRoot, entry, "not-a-revision", local, "local"); err == nil {
		t.Fatal("move preparation accepted an invalid base revision")
	}
	if err := prepareRelocationDestination(context.Background(), entry, "HEAD", filepath.Join(t.TempDir(), "elsewhere"), "", "local"); err == nil || !strings.Contains(err.Error(), "local relocation destination root changed") {
		t.Fatalf("wrong local root error=%v", err)
	}
}
