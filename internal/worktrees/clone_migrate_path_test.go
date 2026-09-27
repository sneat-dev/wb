package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCloneMovePathAndPointerRefusals(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	legacy := filepath.Join(root, "legacy")
	current := filepath.Join(root, "current")
	if err := os.MkdirAll(filepath.Join(current, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, old, want string
		ok                    bool
	}{
		{"nested moved", filepath.Join(legacy, "nested"), legacy, filepath.Join(current, "nested"), true},
		{"clone itself moved", legacy, legacy, current, true},
		{"empty legacy", filepath.Join(legacy, "nested"), "", "", false},
		{"sibling prefix", legacy + "-other/nested", legacy, "", false},
		{"missing destination", filepath.Join(legacy, "absent"), legacy, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := rebaseUnderNewClone(tc.path, tc.old, current)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("rebaseUnderNewClone = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}

	linked := filepath.Join(root, "linked")
	if err := os.Mkdir(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorktreeCommonDir(linked); !os.IsNotExist(err) {
		t.Fatalf("missing pointer error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir:  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorktreeCommonDir(linked); err == nil || !strings.Contains(err.Error(), "empty .git pointer") {
		t.Fatalf("empty pointer error = %v", err)
	}
	for _, tc := range []struct{ pointer, want string }{
		{"gitdir: ../current/.git/worktrees/nested\n", filepath.Join(current, ".git")},
		{"gitdir: " + filepath.Join(current, ".git", "worktrees", "nested") + "\n", filepath.Join(current, ".git")},
	} {
		if err := os.WriteFile(filepath.Join(linked, ".git"), []byte(tc.pointer), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := readWorktreeCommonDir(linked)
		if err != nil || got != tc.want {
			t.Fatalf("readWorktreeCommonDir(%q) = (%q, %v), want %q", tc.pointer, got, err, tc.want)
		}
	}
}

func TestCloneMoveMissingCloneAndRegistration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := PlanCloneMove(ctx, missing, filepath.Join(t.TempDir(), "new")); err == nil {
		t.Fatal("planning nonexistent clone succeeded")
	}
	if _, err := ApplyCloneMove(ctx, missing, filepath.Join(t.TempDir(), "new")); err == nil {
		t.Fatal("moving nonexistent clone succeeded")
	}
	if _, err := registeredWorktreePaths(ctx, missing); err == nil {
		t.Fatal("listing nonexistent clone succeeded")
	}
	if err := VerifyClonePlacement(ctx, missing, nil); err == nil || !strings.Contains(err.Error(), "list worktree registration") {
		t.Fatalf("verify missing clone error = %v", err)
	}
	if _, _, err := ReconcileClonePlacement(ctx, missing, "", false); err == nil || !strings.Contains(err.Error(), "list worktree registration") {
		t.Fatalf("reconcile missing clone error = %v", err)
	}
	if _, err := GitOperationInProgress(ctx, missing); err == nil || !strings.Contains(err.Error(), "resolve git directory") {
		t.Fatalf("operation check missing clone error = %v", err)
	}
	if err := FinalizeCloneMoveRelocationReceipts(nil, time.Time{}); err != nil {
		t.Fatalf("empty receipt batch = %v", err)
	}
}

//nolint:paralleltest // newGitFixture sets HOME, XDG_CONFIG_HOME, and WB_PROJECTS_ROOT for its isolated Git repository.
func TestGitOperationInProgressFindsEachPrivateMarker(t *testing.T) {
	fixture := newGitFixture(t)
	ctx := context.Background()
	gitDir := gitTestOutput(t, fixture.canonical, "rev-parse", "--absolute-git-dir")
	if got, err := GitOperationInProgress(ctx, fixture.canonical); err != nil || got != "" {
		t.Fatalf("clean repository = (%q, %v)", got, err)
	}
	for _, tc := range []struct{ marker, label string }{
		{"MERGE_HEAD", "a merge is in progress"},
		{"CHERRY_PICK_HEAD", "a cherry-pick is in progress"},
		{"REVERT_HEAD", "a revert is in progress"},
		{"rebase-merge", "a rebase is in progress"},
		{"rebase-apply", "a rebase is in progress"},
		{"index.lock", "an index lock is held"},
	} {
		//nolint:paralleltest // Marker cases mutate the same private Git directory and must not overlap.
		t.Run(tc.marker, func(t *testing.T) {
			path := filepath.Join(gitDir, tc.marker)
			if strings.HasPrefix(tc.marker, "rebase-") {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := GitOperationInProgress(ctx, fixture.canonical)
			if err != nil || !strings.Contains(got, tc.label) {
				t.Fatalf("marker %s = (%q, %v), want %q", tc.marker, got, err, tc.label)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

//nolint:paralleltest // newGitFixture sets HOME, XDG_CONFIG_HOME, and WB_PROJECTS_ROOT for its isolated Git repository.
func TestCloneMoveRegistrationAndIntentSelection(t *testing.T) {
	fixture := newGitFixture(t)
	ctx := context.Background()
	linked := filepath.Join(t.TempDir(), "linked")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "feature/linked", linked)
	paths, err := registeredWorktreePaths(ctx, fixture.canonical)
	if err != nil || len(paths) != 1 || paths[0] != linked {
		t.Fatalf("registered linked checkouts = (%#v, %v)", paths, err)
	}
	if err := VerifyClonePlacement(ctx, fixture.canonical, []string{linked}); err != nil {
		t.Fatalf("linked checkout is valid: %v", err)
	}
	unknown := filepath.Join(t.TempDir(), "not-registered")
	if err := VerifyClonePlacement(ctx, fixture.canonical, []string{unknown}); err == nil || !strings.Contains(err.Error(), "does not list") {
		t.Fatalf("unregistered checkout error = %v", err)
	}
	status, informational, err := ReconcileClonePlacement(ctx, fixture.canonical, "", false)
	if err != nil || status != "verified" || len(informational) != 0 {
		t.Fatalf("healthy reconciliation = (%q, %#v, %v)", status, informational, err)
	}
	entries, err := RecordCloneMoveRelocationIntents(fixture.projectsRoot, []CloneMoveWorktree{
		{Source: linked, Destination: linked},
		{Source: filepath.Join(t.TempDir(), "unclaimed"), Destination: filepath.Join(t.TempDir(), "elsewhere")},
	}, time.Now())
	if err != nil || len(entries) != 0 {
		t.Fatalf("unchanged/unclaimed relocation intents = (%#v, %v)", entries, err)
	}
}

//nolint:paralleltest // newGitFixture sets HOME, XDG_CONFIG_HOME, and WB_PROJECTS_ROOT for its isolated Git repository.
func TestCloneMoveRefusesExistingDestinationWithoutMovingSource(t *testing.T) {
	fixture := newGitFixture(t)
	destination := filepath.Join(t.TempDir(), "destination")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanCloneMove(context.Background(), fixture.canonical, destination)
	if err != nil || plan.Source != fixture.canonical || plan.Destination != destination {
		t.Fatalf("clone move plan = (%#v, %v)", plan, err)
	}
	_, err = ApplyCloneMove(context.Background(), fixture.canonical, destination)
	if err == nil || !strings.Contains(err.Error(), "move canonical clone") {
		t.Fatalf("existing destination error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.canonical, ".git")); err != nil {
		t.Fatalf("source clone was changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".git")); !os.IsNotExist(err) {
		t.Fatalf("existing destination was overwritten: %v", err)
	}
}
