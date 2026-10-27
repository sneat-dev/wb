//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

//nolint:paralleltest // newGitFixture configures process-wide isolated Git state.
func TestE2ETaskScopedRegistryAndMissingPathReadersKeepHelperTaskIdentity(t *testing.T) {
	f := newGitFixture(t)
	configureFixtureSharedWorktrees(t, f)
	var created []CreateResult
	for _, task := range []string{"helper-a", "helper-b"} {
		result, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: f.projectsRoot, Operation: task, WorkLog: WorkLogOptions{EffortID: "shared-parent", Model: "unknown"}})
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, result[0])
	}
	selected := map[string]bool{"helper-b": true}
	results, diagnostics := listTaskScopedClaimedRegistryWorktrees(t.Context(), f.projectsRoot, f.home, map[string]bool{}, selected, "main", "acme/app", "", false, 1, nil, inspectPolicy{})
	if len(diagnostics) != 0 || len(results) != 1 || results[0].Task != "helper-b" || results[0].WorktreeDir != created[1].WorktreeDir {
		t.Fatalf("scoped shared effort: %+v %+v", results, diagnostics)
	}
	claim, err := activeWorkLogClaimAtPath(f.home, created[1].WorktreeDir, selected)
	if err != nil || claim == nil || claim.Task != "helper-b" || claim.EffortID != "shared-parent" {
		t.Fatalf("exact alias claim: %+v %v", claim, err)
	}
	if other, err := activeWorkLogClaimAtPath(f.home, created[0].WorktreeDir, selected); err != nil || other != nil {
		t.Fatalf("sibling admitted under selected task: %+v %v", other, err)
	}
}

func TestTaskScopedRegistryFailsClosedOnUnreadableEffortInventory(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "worklogs"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	results, diagnostics := listTaskScopedClaimedRegistryWorktrees(t.Context(), t.TempDir(), home, nil, map[string]bool{"task": true}, "main", "", "", false, 1, nil, inspectPolicy{})
	if len(results) != 0 || len(diagnostics) != 1 {
		t.Fatalf("unreadable effort inventory: %+v %+v", results, diagnostics)
	}
}
