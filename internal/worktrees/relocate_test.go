package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRelocateMovesManagedSharedWorktreeToLocalAndPreservesClaim(t *testing.T) {
	fixture := newGitFixture(t)
	configHome := t.TempDir()
	shared := filepath.Join(t.TempDir(), "shared-worktrees")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := os.MkdirAll(filepath.Join(configHome, "wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configHome, "wb", "worktrees.yaml"), []byte("version: 1\nworktrees:\n  root: "+shared+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "relocate-layout", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Create selected the configured root, so move it to local first. This also
	// exercises a worktree whose current placement differs from today's config.
	sharedWorktree := created[0].WorktreeDir
	plan, err := Relocate(context.Background(), RelocateOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocate-layout", To: "local"})
	if err != nil || len(plan.Results) != 1 || !plan.Results[0].Eligible || plan.Results[0].Applied {
		t.Fatalf("local relocation plan = %#v, err=%v", plan, err)
	}
	if _, err := os.Stat(sharedWorktree); err != nil {
		t.Fatalf("dry run changed shared worktree: %v", err)
	}
	applied, err := Relocate(context.Background(), RelocateOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocate-layout", To: "local", Apply: true})
	if err != nil || len(applied.Results) != 1 || !applied.Results[0].Applied || applied.Results[0].ClaimID == "" || applied.Results[0].ReceiptPath == "" {
		t.Fatalf("local relocation apply = %#v, err=%v", applied, err)
	}
	localWorktree := filepath.Join(fixture.canonical, ".worktrees", "relocate-layout")
	if _, err := os.Stat(sharedWorktree); !os.IsNotExist(err) {
		t.Fatalf("shared source remains after relocation: %v", err)
	}
	if _, err := Guard(context.Background(), localWorktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil {
		t.Fatalf("guard after relocation: %v", err)
	}
	listed, err := List(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocate-layout"})
	if err != nil || len(listed) != 1 || listed[0].WorktreeDir != localWorktree {
		t.Fatalf("list after relocation = %#v, err=%v", listed, err)
	}
	back, err := Relocate(context.Background(), RelocateOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocate-layout", To: "shared", Apply: true})
	if err != nil || len(back.Results) != 1 || !back.Results[0].Applied || back.Results[0].ReceiptPath == applied.Results[0].ReceiptPath {
		t.Fatalf("shared relocation apply = %#v, err=%v", back, err)
	}
	if _, err := Guard(context.Background(), sharedWorktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil {
		t.Fatalf("guard after shared relocation: %v", err)
	}
	again, err := Relocate(context.Background(), RelocateOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocate-layout", To: "local", Apply: true})
	if err != nil || len(again.Results) != 1 || !again.Results[0].Applied {
		t.Fatalf("second local relocation apply = %#v, err=%v", again, err)
	}
	retry, err := Relocate(context.Background(), RelocateOptions{ProjectsRoot: fixture.projectsRoot, Task: "relocate-layout", To: "local", Apply: true})
	if err != nil || len(retry.Results) != 1 || !retry.Results[0].AlreadyThere || retry.Results[0].Applied || retry.Results[0].ReceiptPath != again.Results[0].ReceiptPath {
		t.Fatalf("relocation retry = %#v, err=%v", retry, err)
	}
}

func TestRelocateRefusesExternalAndDirtyWorktrees(t *testing.T) {
	if eligible, reason := relocationEligibility(ListResult{External: true, Clean: true}); eligible || reason == "" {
		t.Fatalf("external relocation eligibility = %t, %q", eligible, reason)
	}
	if eligible, reason := relocationEligibility(ListResult{Clean: false}); eligible || reason == "" {
		t.Fatalf("dirty relocation eligibility = %t, %q", eligible, reason)
	}
}
