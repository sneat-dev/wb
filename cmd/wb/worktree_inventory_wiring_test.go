package main

import (
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeInventoryRootBindingUsesActualNativeListService(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	inv := &invocation{projectsRoot: filepath.Join(blocker, "projects")}
	list, summary := newWorktreeListCmd(inv), newWorktreeSummaryCmd(inv)
	inv.projectsRoot = projects
	inv.filterFlag = "private-filter"
	if out, err := executeCollaborationCommand(t, list); err != nil || out != "no WB worktrees\n" {
		t.Fatalf("lazy native list=%q err=%v", out, err)
	}
	if out, err := executeCollaborationCommand(t, summary, "private-task"); err != nil || !strings.Contains(out, "no live worktrees for this task") {
		t.Fatalf("lazy native summary=%q err=%v", out, err)
	}
	for _, args := range [][]string{{"list", "--format", "json"}, {"summary", "private-task", "--format", "json"}} {
		out, err := executeCollaborationCommand(t, newWorktreeCmd(inv), args...)
		if err != nil {
			t.Fatal(err)
		}
		var outcome worktrees.ListOutcome
		if err := json.Unmarshal([]byte(out), &outcome); err != nil || outcome.SchemaVersion != 1 || len(outcome.Results) != 0 {
			t.Fatalf("actual registry %v outcome=%+v err=%v", args, outcome, err)
		}
	}
	_, err := executeCollaborationCommand(t, newWorktreeCmd(inv), "list", "--finalized", "--not-finalized")
	var coded *exitError
	if !errors.As(err, &coded) || coded.code != exitUsage || coded.message != "--finalized and --not-finalized cannot be combined" {
		t.Fatalf("actual root usage identity=%v", err)
	}
}
