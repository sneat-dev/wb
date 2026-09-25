package worktrees

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The old claim and the listed checkout may name different repositories only
// when their paths describe the same deterministic repository transfer.
func TestLegacyRepositoryRelocationPathsAcceptsOnlyDeterministicTransfers(t *testing.T) {
	t.Parallel()
	// A host-qualified canonical path gives placement its host directly, so
	// this path validation test needs no Git checkout or subprocess.
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	canonical := filepath.Join(projectsRoot, "github.com", "acme", "renamed")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(t.TempDir(), "worktrees")
	task := "moved-task"
	old := filepath.Join(store, task, "github.com", "acme", "app")
	newPath := filepath.Join(store, task, "github.com", "acme", "renamed")
	base := ListResult{Repository: "acme/renamed", CanonicalDir: canonical,
		WorktreesRoot: store, WorktreeDir: newPath, Task: task}
	claim := workLogClaim{Repository: "github.com/acme/app", Worktree: old}
	outsideCanonical := t.TempDir()

	cases := []struct {
		name    string
		edit    func(*ListResult, *workLogClaim)
		wantErr bool
		want    string
	}{
		{name: "central transfer"},
		{name: "foreign checkout", edit: func(entry *ListResult, _ *workLogClaim) { entry.External = true }, wantErr: true, want: "supported managed-worktree layout"},
		{name: "relative store", edit: func(entry *ListResult, _ *workLogClaim) { entry.WorktreesRoot = "relative" }, wantErr: true, want: "supported managed-worktree layout"},
		{name: "invalid task", edit: func(entry *ListResult, _ *workLogClaim) { entry.Task = "../escape" }, wantErr: true, want: "supported managed-worktree layout"},
		{name: "wrong destination", edit: func(entry *ListResult, _ *workLogClaim) {
			entry.WorktreeDir = filepath.Join(store, task, "github.com", "acme", "other")
		}, wantErr: true, want: "not a deterministic repository-transfer placement"},
		{name: "wrong source", edit: func(_ *ListResult, claim *workLogClaim) {
			claim.Worktree = filepath.Join(store, task, "github.com", "acme", "other")
		}, wantErr: true, want: "not a deterministic repository-transfer placement"},
		{name: "malformed claimed repository", edit: func(_ *ListResult, claim *workLogClaim) { claim.Repository = "invalid" }, wantErr: true, want: "must be owner/name"},
		{name: "malformed listed repository", edit: func(entry *ListResult, _ *workLogClaim) { entry.Repository = "invalid" }, wantErr: true, want: "must be owner/name"},
		{name: "canonical outside projects root", edit: func(entry *ListResult, _ *workLogClaim) {
			entry.CanonicalDir = outsideCanonical
		}, wantErr: true, want: "canonical clone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entry, recorded := base, claim
			if tc.edit != nil {
				tc.edit(&entry, &recorded)
			}
			err := legacyRepositoryRelocationPaths(projectsRoot, entry, recorded)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("valid transfer refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("invalid transfer error = %v, want %q", err, tc.want)
			}
		})
	}

	localCanonical := canonical
	local := ListResult{Repository: "acme/renamed", CanonicalDir: localCanonical, Local: true,
		WorktreesRoot: filepath.Join(localCanonical, ".worktrees"), Task: task}
	local.WorktreeDir = filepath.Join(local.WorktreesRoot, task)
	localClaim := workLogClaim{Repository: "github.com/acme/app", Worktree: filepath.Join(projectsRoot, "github.com", "acme", "app", ".worktrees", task)}
	if err := legacyRepositoryRelocationPaths(projectsRoot, local, localClaim); err != nil {
		t.Fatalf("valid repository-local transfer refused: %v", err)
	}
	localClaim.Worktree = filepath.Join(projectsRoot, "github.com", "acme", "app", ".worktrees", "different-task")
	if err := legacyRepositoryRelocationPaths(projectsRoot, local, localClaim); err == nil || !strings.Contains(err.Error(), "not a deterministic repository-transfer placement") {
		t.Fatalf("mismatched repository-local source error = %v", err)
	}
}
