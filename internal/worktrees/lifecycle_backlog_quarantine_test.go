package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A durable cleanup record belonging to somebody else must never be able to
// refuse this task's cleanup.
//
// This is the record that actually did it: written by `wb worktree gc --apply`
// for a detached review checkout, already complete and owing nothing, and
// invalid to any WB whose validator predates the detached field. One file in a
// shared directory of 292 made `wb worktree cleanup --apply` refuse every
// worktree on the machine, for every lane.
func TestCleanupSurvivesAnInvalidBacklogRecordBelongingToAnotherTask(t *testing.T) {
	fixture, result, head, mergedAt := prepareMergedTask(t, "cleanup-past-invalid-backlog")
	installMergedPullRequestFixture(t, head, mergedAt)

	backlogDirectory := filepath.Join(fixture.home, "reports", "worktree-cleanup", "backlog")
	if err := os.MkdirAll(backlogDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "bad237b69e075233c84c19476a7e31cdd834b42ab843c909f3d665d6c9af9853"
	record := map[string]any{
		"version":        1,
		"id":             id,
		"task":           "wb-pr332-review",
		"repository":     "sneat-dev/wb",
		"projects_root":  fixture.projectsRoot,
		"canonical_dir":  filepath.Join(fixture.projectsRoot, "sneat-dev", "wb"),
		"worktrees_root": filepath.Join(fixture.home, "worktrees"),
		"worktree_dir":   filepath.Join(fixture.home, "worktrees", "wb-pr332-review", "sneat-dev", "wb"),
		"branch":         "",
		"base":           "main",
		"head_sha":       "1572bceaa36da9053f7d4d742d86d0093880aa01",
		"disposition":    "removed",
		"stage":          "complete",
		"created_at":     time.Now().UTC().Format(time.RFC3339Nano),
		"updated_at":     time.Now().UTC().Format(time.RFC3339Nano),
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backlogDirectory, id+".json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-past-invalid-backlog",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatalf("one invalid record for another task must not refuse this task's cleanup: %v", err)
	}
	if len(outcome.Results) != 1 || !outcome.Results[0].Applied {
		t.Fatalf("cleanup = %#v", outcome.Results)
	}
	if _, statErr := os.Stat(result.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("worktree survived: %v", statErr)
	}
	// It belongs to another task and is already complete, so this run has no
	// business with it and says nothing about it — and it is still there,
	// untouched, for whoever does.
	if _, statErr := os.Stat(filepath.Join(backlogDirectory, id+".json")); statErr != nil {
		t.Fatalf("the record was removed rather than left alone: %v", statErr)
	}
}

// A record this run IS responsible for, and cannot validate, is named rather
// than silently skipped — and still does not abort the run.
func TestCleanupQuarantinesAnInvalidRecordItIsResponsibleFor(t *testing.T) {
	fixture, result, head, mergedAt := prepareMergedTask(t, "cleanup-own-invalid-backlog")
	installMergedPullRequestFixture(t, head, mergedAt)

	backlogDirectory := filepath.Join(fixture.home, "reports", "worktree-cleanup", "backlog")
	if err := os.MkdirAll(backlogDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "aa11bb22cc33dd44ee55ff6677889900aa11bb22cc33dd44ee55ff6677889900"
	record := map[string]any{
		"version":        1,
		"id":             id,
		"task":           "cleanup-own-invalid-backlog",
		"repository":     "acme/app",
		"projects_root":  fixture.projectsRoot,
		"canonical_dir":  filepath.Join(fixture.projectsRoot, "acme", "app"),
		"worktrees_root": filepath.Join(fixture.home, "worktrees"),
		"worktree_dir":   filepath.Join(fixture.home, "worktrees", "cleanup-own-invalid-backlog", "acme", "app"),
		"branch":         "",
		"base":           "main",
		"head_sha":       "1572bceaa36da9053f7d4d742d86d0093880aa01",
		"disposition":    "removed",
		"stage":          "sealed",
		"created_at":     time.Now().UTC().Format(time.RFC3339Nano),
		"updated_at":     time.Now().UTC().Format(time.RFC3339Nano),
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backlogDirectory, id+".json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-own-invalid-backlog",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatalf("an unvalidatable record must not abort the run: %v", err)
	}
	if len(outcome.Quarantined) != 1 || outcome.Quarantined[0].Task != "cleanup-own-invalid-backlog" {
		t.Fatalf("quarantine = %#v, want the record named", outcome.Quarantined)
	}
	if outcome.Quarantined[0].Reason == "" {
		t.Fatal("a quarantined record must say why it could not be acted on")
	}
	_ = result
}
