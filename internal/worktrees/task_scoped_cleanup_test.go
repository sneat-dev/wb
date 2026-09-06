package worktrees

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestTaskScopedLocalLayoutSkipsUnrelatedRepositories(t *testing.T) {
	projects := t.TempDir()
	home := filepath.Join(t.TempDir(), ".wb")
	t.Setenv(wbhome.EnvOverride, home)
	task := "requested-task"
	target := filepath.Join(projects, "acme", "app")
	unrelated := filepath.Join(projects, "other", "repo")
	if err := os.MkdirAll(filepath.Join(target, ".worktrees", task), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(unrelated, ".worktrees", "unrelated-task"), 0o755); err != nil {
		t.Fatal(err)
	}
	claimDir := filepath.Join(home, "worklogs", task, "runs", "run-1", "claims")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	claim := workLogClaim{Version: 2, EffortID: task, Task: task, RunID: "run-1", ClaimID: "claim-1", Repository: "acme/app", Worktree: filepath.Join(target, ".worktrees", task), Lifecycle: "active"}
	raw, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claimDir, "claim-1.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	layouts, diagnostics := discoverTaskScopedLocalWorktreeLayouts(projects, map[string]bool{task: true})
	if len(diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
	}
	if len(layouts) != 1 || filepath.Clean(layouts[0].WorktreesRoot) != filepath.Join(target, ".worktrees") {
		t.Fatalf("task-scoped layouts = %#v, want only %s", layouts, filepath.Join(target, ".worktrees"))
	}
}
