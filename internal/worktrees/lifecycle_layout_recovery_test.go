package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListRetainsClaimedSharedWorktreeAfterUserRootChanges(t *testing.T) {
	fixture := newGitFixture(t)
	configHome := t.TempDir()
	oldRoot := filepath.Join(t.TempDir(), "old-shared-worktrees")
	newRoot := filepath.Join(t.TempDir(), "new-shared-worktrees")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  root: "+oldRoot+"\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "root-drift", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create shared worktree = %#v, err=%v", created, err)
	}
	unrelated, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "unrelated-root-drift", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(unrelated) != 1 {
		t.Fatalf("create unrelated shared worktree = %#v, err=%v", unrelated, err)
	}
	// The next create would use newRoot, but this live member remains owned by
	// its exact active Work Log claim and Git registry rather than a stale
	// user preference.
	mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  root: "+newRoot+"\n")
	listed, err := ListWithDiagnostics(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "root-drift", Workers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Results) != 1 {
		t.Fatalf("list after shared-root drift = %#v diagnostics=%#v", listed.Results, listed.Diagnostics)
	}
	result := listed.Results[0]
	if result.WorktreeDir != created[0].WorktreeDir || result.External || result.Local {
		t.Fatalf("claimed shared result = %#v", result)
	}
	resolvedOldRoot, resolveErr := resolveSharedWorktreesRoot(oldRoot)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if got, want := result.WorktreesRoot, resolvedOldRoot; got != want {
		t.Fatalf("physical shared root = %q, want %q", got, want)
	}
	for _, diagnostic := range listed.Diagnostics {
		if diagnostic.Task == "unrelated-root-drift" {
			t.Fatalf("named list leaked unrelated shared task diagnostic: %#v", diagnostic)
		}
	}
}

func TestLocalStageIsReportedAndBlocksItsPhysicalTask(t *testing.T) {
	fixture := newGitFixture(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "local-stage", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create local worktree = %#v, err=%v", created, err)
	}
	stage := filepath.Join(fixture.canonical, ".worktrees", ".wb-retired-stage-0123456789abcdef0123456789abcdef")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "interrupted.txt"), []byte("preserve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	listed, err := ListWithDiagnostics(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "local-stage", Workers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Results) != 1 || !listed.Results[0].Local {
		t.Fatalf("local inventory = %#v", listed.Results)
	}
	foundArtifact, foundBlockingDiagnostic := false, false
	for _, artifact := range listed.Artifacts {
		if artifact.Path == stage && artifact.Kind == "secure_worktree_stage" {
			foundArtifact = true
		}
	}
	for _, diagnostic := range listed.Diagnostics {
		if diagnostic.Task == "local-stage" && diagnostic.Path == stage && strings.Contains(diagnostic.Message, "blocks cleanup") {
			foundBlockingDiagnostic = true
		}
	}
	if !foundArtifact || !foundBlockingDiagnostic {
		t.Fatalf("local stage not fully reported: artifacts=%#v diagnostics=%#v", listed.Artifacts, listed.Diagnostics)
	}
}

func TestListReportsMissingRegisteredClaimedSharedWorktree(t *testing.T) {
	fixture := newGitFixture(t)
	configHome := t.TempDir()
	sharedRoot := filepath.Join(t.TempDir(), "shared-worktrees")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  root: "+sharedRoot+"\n")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "missing-claimed", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create claimed shared worktree = %#v, err=%v", created, err)
	}
	// Leave Git's registry intact while modelling an interrupted external
	// deletion of the working tree.
	if err := os.RemoveAll(created[0].WorktreeDir); err != nil {
		t.Fatal(err)
	}
	listed, err := ListWithDiagnostics(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "missing-claimed", Workers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range listed.Diagnostics {
		if diagnostic.Task == "missing-claimed" && diagnostic.Path == created[0].WorktreeDir && strings.Contains(diagnostic.Message, "still registers") {
			return
		}
	}
	t.Fatalf("missing claimed worktree was silently dropped: %#v", listed.Diagnostics)
}
