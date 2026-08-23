package worktrees

import (
	"os"
	"testing"
)

func TestOwnerViewsAppendAndEvaluateLivePID(t *testing.T) {
	worktree := newJournalWorktree(t)
	if _, err := recordOwner(worktree, "effort", "codex", "sol", os.Getpid()); err != nil {
		t.Fatalf("record owner: %v", err)
	}
	if _, err := recordOwner(worktree, "effort", "claude", "haiku", 99999999); err != nil {
		t.Fatalf("record second owner: %v", err)
	}
	owners, err := ownerViews(worktree)
	if err != nil {
		t.Fatalf("owner views: %v", err)
	}
	if len(owners) != 2 || owners[0].Agent != "codex" || owners[0].PIDStatus != "active" || owners[1].Agent != "claude" || owners[1].PIDStatus != "orphaned" {
		t.Fatalf("owners = %#v", owners)
	}
	if got := worktreeOwnerState(owners); got != "active" {
		t.Fatalf("owner state = %q, want active", got)
	}
	if got := worktreeOwnerState(nil); got != "orphaned" {
		t.Fatalf("no-owner state = %q, want orphaned", got)
	}
}
