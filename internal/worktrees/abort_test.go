package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAbortDiscardedResumesExactBranchAfterWorktreeRemoval(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "discard-resume-after-remove",
	})
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected abort crash after worktree removal")
	first, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "discard-resume-after-remove",
		Disposition: AbortDiscarded, DeleteRemote: true, Apply: true,
		afterAbortWorktreeRemoval: func(string) error { return injected },
	})
	if !errors.Is(err, injected) {
		t.Fatalf("abort interruption = %v, want %v", err, injected)
	}
	if len(first) != 1 || !first[0].WorktreeGone || first[0].BranchDeleted || first[0].BacklogID == "" {
		t.Fatalf("interrupted abort = %#v", first)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created[0].Branch); branchErr != nil || !exists {
		t.Fatalf("interrupted abort branch exists=%t err=%v", exists, branchErr)
	}

	resumed, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "discard-resume-after-remove",
		Disposition: AbortDiscarded, DeleteRemote: true, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 1 || !resumed[0].Applied || !resumed[0].WorktreeGone || !resumed[0].BranchDeleted || resumed[0].BacklogID == "" {
		t.Fatalf("resumed abort = %#v", resumed)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created[0].Branch); branchErr != nil || exists {
		t.Fatalf("resumed abort branch exists=%t err=%v", exists, branchErr)
	}
}

// TestAbortDiscardedUnusedWorktreesIsAudited covers the common storage-agent
// failure shape: two untouched worktrees were claimed but never started, so
// they cannot have merged PR evidence and must not become abandoned branches.
func TestAbortDiscardedUnusedWorktreesIsAudited(t *testing.T) {
	fixture := newGitFixture(t)
	otherCanonical := filepath.Join(fixture.projectsRoot, "acme", "storage")
	gitTest(t, fixture.projectsRoot, "clone", fixture.remote, otherCanonical)
	created, err := Create(context.Background(), []string{"acme/app", "acme/storage"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "unused-storage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 {
		t.Fatalf("created = %#v", created)
	}
	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "unused-storage", Disposition: AbortDiscarded,
		DeleteRemote: true, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("abort = %#v", results)
	}
	for _, result := range results {
		if !result.Applied || !result.WorktreeGone || !result.BranchDeleted {
			t.Fatalf("abort result = %#v", result)
		}
	}
	for _, create := range created {
		if _, err := os.Stat(create.WorktreeDir); !os.IsNotExist(err) {
			t.Fatalf("discarded worktree remains: %v", err)
		}
		canonical := fixture.canonical
		if create.Repository == "acme/storage" {
			canonical = otherCanonical
		}
		if exists, err := localBranchExists(context.Background(), canonical, create.Branch); err != nil || exists {
			t.Fatalf("discarded branch exists=%t err=%v", exists, err)
		}
	}
	terminal := filepath.Join(fixture.home, "worklogs", "unused-storage", "runs")
	entries, err := os.ReadDir(terminal)
	if err != nil || len(entries) != 1 {
		t.Fatalf("terminal archive directory = %#v err=%v", entries, err)
	}
	terminals, err := os.ReadDir(filepath.Join(terminal, entries[0].Name(), "terminals"))
	if err != nil || len(terminals) != 2 {
		t.Fatalf("sealed terminal cardinality = %d err=%v, want 2", len(terminals), err)
	}
	for _, terminal := range terminals {
		if !validClaimID(strings.TrimSuffix(terminal.Name(), ".json")) {
			t.Fatalf("invalid terminal claim name: %s", terminal.Name())
		}
	}
}

func TestAbortDiscardedRetiresExactRemoteBranchOnlyWithExplicitAuthorization(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "pushed-discard",
	})
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, created[0].WorktreeDir, "push", "-u", "origin", created[0].Branch)

	_, err = Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "pushed-discard",
		Disposition:  AbortDiscarded,
		Apply:        true,
	})
	if err == nil || !strings.Contains(err.Error(), "--remote") {
		t.Fatalf("discard without remote authorization error = %v", err)
	}
	if _, statErr := os.Stat(created[0].WorktreeDir); statErr != nil {
		t.Fatalf("refused discard changed worktree: %v", statErr)
	}
	if remoteHead, remoteErr := remoteBranchHead(context.Background(), fixture.canonical, created[0].Branch); remoteErr != nil || remoteHead == "" {
		t.Fatalf("refused discard changed remote branch: head=%q err=%v", remoteHead, remoteErr)
	}

	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "pushed-discard",
		Disposition:  AbortDiscarded,
		DeleteRemote: true,
		Apply:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Applied || !results[0].RemoteDeleted || !results[0].WorktreeGone || !results[0].BranchDeleted {
		t.Fatalf("discard result = %#v", results)
	}
	if remoteHead, remoteErr := remoteBranchHead(context.Background(), fixture.canonical, created[0].Branch); remoteErr != nil || remoteHead != "" {
		t.Fatalf("discard left remote branch at %q: %v", remoteHead, remoteErr)
	}
}

func TestAbortDiscardedRechecksDirtyStateAtRemovalBoundary(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "raced-discard",
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	var writeErr error
	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "raced-discard",
		Disposition:  AbortDiscarded,
		DeleteRemote: true,
		Apply:        true,
		beforeAbortRemoval: func(worktree string) {
			writeErr = os.WriteFile(filepath.Join(worktree, "README.md"), []byte("concurrent writer\n"), 0o644)
		},
	})
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "immediately before removal") {
		t.Fatalf("raced discard error = %v, results=%#v", err, results)
	}
	if _, statErr := os.Stat(created[0].WorktreeDir); statErr != nil {
		t.Fatalf("raced discard removed worktree: %v", statErr)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created[0].Branch); branchErr != nil || !exists {
		t.Fatalf("raced discard removed branch: exists=%t err=%v", exists, branchErr)
	}
	after, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	if after != before || after.Lifecycle != "active" {
		t.Fatalf("raced discard changed active projection: before=%#v after=%#v", before, after)
	}
}

func TestAbortNotLandedSealsButRetainsResumableWorktree(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "resume-storage"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	results, err := Abort(context.Background(), AbortOptions{ProjectsRoot: fixture.projectsRoot, Task: "resume-storage", Disposition: AbortNotLanded, Successor: "codex-resume-2", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Applied || results[0].WorktreeGone || results[0].BranchDeleted {
		t.Fatalf("abort = %#v", results)
	}
	if _, err := os.Stat(created[0].WorktreeDir); err != nil {
		t.Fatalf("resumable worktree missing: %v", err)
	}
	after, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	if after.Lifecycle != "active" || after.ClaimID == before.ClaimID {
		t.Fatalf("successor projection = %#v, prior = %#v", after, before)
	}
	runDir := filepath.Join(fixture.home, "worklogs", after.EffortID, "runs", after.RunID)
	claims, err := os.ReadDir(filepath.Join(runDir, "claims"))
	if err != nil || len(claims) != 2 {
		t.Fatalf("claim transfer cardinality = %d err=%v, want 2", len(claims), err)
	}
	terminals, err := os.ReadDir(filepath.Join(runDir, "terminals"))
	if err != nil || len(terminals) != 1 {
		t.Fatalf("terminal transfer cardinality = %d err=%v, want 1", len(terminals), err)
	}
}

func TestAbortResumableRequiresExactlyOneSuccessor(t *testing.T) {
	fixture := newGitFixture(t)
	if _, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "needs-successor"}); err != nil {
		t.Fatal(err)
	}
	_, err := Abort(context.Background(), AbortOptions{ProjectsRoot: fixture.projectsRoot, Task: "needs-successor", Disposition: AbortHandoff, Apply: true})
	if err == nil || !strings.Contains(err.Error(), "--successor") {
		t.Fatalf("missing successor error = %v", err)
	}
}
