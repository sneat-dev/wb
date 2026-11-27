package disk

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The legacy central layout keeps worktrees under the WB home, so the
// worktrees category's root sits inside wb-state's root. A byte that lives in
// both trees must be attributed to one category, not reported as reclaimable
// by each — on 2026-11-27 that made wb-state look like 8.8 GB of state when
// 3.5 GB of it was worktrees wb clean already owns.
func TestNestedWorktreeRootIsNotAlsoCountedAsWBState(t *testing.T) {
	isolate(t)
	projects := t.TempDir()
	home := t.TempDir()
	worktree := filepath.Join(home, "worktrees", "task")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 1<<20)
	if err := os.WriteFile(filepath.Join(worktree, "big.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := Collect(context.Background(), Options{ProjectsRoot: projects, WBHome: home})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Category{}
	for _, category := range report.Categories {
		byName[category.Name] = category
	}
	if byName["worktrees"].UnsharedBytes < int64(len(payload)) {
		t.Fatalf("worktrees reclaim = %d, want at least %d", byName["worktrees"].UnsharedBytes, len(payload))
	}
	if state := byName["wb-state"]; state.UnsharedBytes >= int64(len(payload)) || state.ApparentBytes >= int64(len(payload)) {
		t.Fatalf("wb-state also counts the worktree: reclaim %d, apparent %d", state.UnsharedBytes, state.ApparentBytes)
	}
}
