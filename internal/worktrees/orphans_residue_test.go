package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unregisterWorktreeLeavingItOnDisk reproduces what a failed `git worktree
// remove` leaves behind: Git deletes the registration even when it cannot
// finish deleting the tree, so the checkout survives with nothing listing it.
func unregisterWorktreeLeavingItOnDisk(t *testing.T, worktree string) {
	t.Helper()
	marker, err := os.ReadFile(filepath.Join(worktree, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	administrative, found := strings.CutPrefix(strings.TrimSpace(string(marker)), "gitdir: ")
	if !found {
		t.Fatalf("fixture worktree has no gitdir marker: %q", marker)
	}
	if err := os.RemoveAll(administrative); err != nil {
		t.Fatal(err)
	}
}

func TestOrphansReportsUnregisteredResidue(t *testing.T) {
	fixture, created, _, _ := prepareMergedTask(t, "orphan-residue")
	unregisterWorktreeLeavingItOnDisk(t, created.WorktreeDir)

	report, err := Orphans(context.Background(), OrphanOptions{ProjectsRoot: fixture.projectsRoot})
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.Residue != 1 || len(report.Residue) != 1 {
		t.Fatalf("residue = %#v, totals=%d", report.Residue, report.Totals.Residue)
	}
	residue := report.Residue[0]
	if residue.Path != created.WorktreeDir || residue.Task != "orphan-residue" || residue.Repository != "acme/app" {
		t.Fatalf("residue = %#v", residue)
	}
	if residue.CanonicalDir == "" || residue.Remedy == "" || len(residue.Evidence) == 0 {
		t.Fatalf("residue carries no actionable explanation: %#v", residue)
	}
	if !strings.Contains(residue.Remedy, "wb worktree cleanup orphan-residue") {
		t.Fatalf("remedy does not name the command that finishes it: %q", residue.Remedy)
	}
	// Read-only: the sweep explains, it never removes.
	if _, statErr := os.Stat(created.WorktreeDir); statErr != nil {
		t.Fatalf("orphan sweep mutated the residue: %v", statErr)
	}
}

func TestOrphansLeavesRegisteredWorktreesOutOfResidue(t *testing.T) {
	fixture, _, _, _ := prepareMergedTask(t, "orphan-registered")

	report, err := Orphans(context.Background(), OrphanOptions{ProjectsRoot: fixture.projectsRoot})
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.Residue != 0 {
		t.Fatalf("a registered worktree was reported as residue: %#v", report.Residue)
	}
}

// TestOrphansIgnoresTaskWorkingDirectories keeps the sweep from calling a
// task's own notes a lost checkout. WB's worktrees root holds more than
// repositories — an effort's evidence and scripts live there too.
func TestOrphansIgnoresTaskWorkingDirectories(t *testing.T) {
	fixture := newGitFixture(t)
	scratch := filepath.Join(fixture.home, "worktrees", "orphan-scratch", "local", "evidence")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "notes.md"), []byte("# notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Orphans(context.Background(), OrphanOptions{ProjectsRoot: fixture.projectsRoot})
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.Residue != 0 {
		t.Fatalf("a task working directory was reported as residue: %#v", report.Residue)
	}
}
