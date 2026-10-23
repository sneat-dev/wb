//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

//nolint:paralleltest // Reuses the package's existing HOME/WB projects-root fixture for native resolution.
func TestE2EShellResidueNextResolvedSweepDeduplicatesActualLayouts(t *testing.T) {
	projects, root := setUpShellRetirementFixture(t)
	task := filepath.Join(root, "terminal-shell")
	if err := os.Mkdir(task, 0700); err != nil {
		t.Fatal(err)
	}
	resolution, err := wbhome.Resolve(projects)
	if err != nil {
		t.Fatal(err)
	}
	// The resolved-layout boundary remains defensive when a discovered layout
	// repeats an existing real root; no metadata or directory entry is fabricated.
	resolution.Read = append(resolution.Read, resolution.Read...)
	outcome, err := retireResolvedTaskShells(context.Background(), RetireShellsOptions{ProjectsRoot: projects}, resolution)
	if err != nil || len(outcome.Results) != 1 || outcome.Results[0].Task != "terminal-shell" || !outcome.Results[0].Eligible || outcome.Results[0].Applied {
		t.Fatalf("duplicate sweep=%+v %v", outcome, err)
	}
	if _, err := os.Stat(task); err != nil {
		t.Fatalf("dry sweep removed task: %v", err)
	}
}
