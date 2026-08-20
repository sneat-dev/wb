package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// installUnremovableIgnoredTree reproduces the founder's 2026-08-20 incident on
// sneat-co/communitycentrum: a worktree that is clean by Git's own definition
// but carries a large ignored build directory Git cannot finish deleting. Git
// removes a worktree's working tree first and its registration second, and it
// removes the registration even when the tree delete failed partway, so the
// checkout survives as residue nothing is registered to own any more.
//
// The ignore rule goes into the canonical repository's info/exclude, which
// lives in the shared common directory every linked worktree reads, so the
// worktree stays clean without a tracked .gitignore the fixture would have to
// merge.
func installUnremovableIgnoredTree(t *testing.T, canonical, worktree string) string {
	t.Helper()
	exclude := filepath.Join(canonical, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		t.Fatal(err)
	}
	// Append: WB keeps its own private projection excluded through this same
	// file, and replacing it would make the fixture dirty for the wrong reason.
	existing, err := os.ReadFile(exclude)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(exclude, append(existing, []byte("\nnode_modules/\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	sealed := filepath.Join(worktree, "node_modules", "sealed")
	if err := os.MkdirAll(sealed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sealed, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Read and search, but no write: the directory can be listed and entered,
	// and the file inside it cannot be unlinked. This is what makes Git's
	// recursive delete fail while its registration delete still succeeds.
	if err := os.Chmod(sealed, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o700) })
	// WB's own private projection (.wb, .wb-worklog) is untracked by design and
	// is not what this fixture is asserting. The residue must be invisible to
	// Git, so that cleanup reaches the removal step at all.
	if status := gitTestOutput(t, worktree, "status", "--porcelain"); strings.Contains(status, "node_modules") {
		t.Fatalf("fixture residue is not ignored by Git: %q", status)
	}
	return sealed
}

func requireUnprivilegedResidueTest(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permissions this test depends on")
	}
}

// TestCleanupRemovesResidueLeftByFailedGitWorktreeRemoval is the regression for
// the incident itself. Cleanup must finish the task rather than abort on Git's
// non-zero exit: the tree WB created is gone, the branch is retired, and the
// report says WB removed the residue.
func TestCleanupRemovesResidueLeftByFailedGitWorktreeRemoval(t *testing.T) {
	requireUnprivilegedResidueTest(t)
	fixture, created, head, mergedAt := prepareMergedTask(t, "cleanup-residue-inline")
	installMergedPullRequestFixture(t, head, mergedAt)
	installUnremovableIgnoredTree(t, fixture.canonical, created.WorktreeDir)

	cleaned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-residue-inline",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned.Results) != 1 {
		t.Fatalf("cleanup results = %#v", cleaned.Results)
	}
	result := cleaned.Results[0]
	if !result.Applied || !result.WorktreeGone || !result.BranchDeleted {
		t.Fatalf("cleanup did not finish the task: %#v", result)
	}
	if !result.WorktreeResidueRemoved {
		t.Fatalf("cleanup report does not credit WB with removing the residue: %#v", result)
	}
	if _, statErr := os.Stat(created.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("residual worktree remains: %v", statErr)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created.Branch); branchErr != nil || exists {
		t.Fatalf("local branch exists=%t err=%v, want it retired", exists, branchErr)
	}
	if cleaned.ReportPath == "" {
		t.Fatal("cleanup wrote no report")
	}
	reportContent, err := os.ReadFile(cleaned.ReportPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(reportContent), `"worktree_residue_removed": true`) {
		t.Fatalf("cleanup.json does not record the residue removal:\n%s", reportContent)
	}
}

// TestCleanupResumesResidueRemovalInterruptedMidRepair covers the state the old
// code could not leave: the registration is gone, the tree is still on disk,
// and the backlog record is stranded at removing_worktree. A later identical
// cleanup must finish it — the residue directory it re-reads as "not a Git
// worktree root" must not block the very task whose record covers it.
func TestCleanupResumesResidueRemovalInterruptedMidRepair(t *testing.T) {
	requireUnprivilegedResidueTest(t)
	fixture, created, head, mergedAt := prepareMergedTask(t, "cleanup-residue-resume")
	installMergedPullRequestFixture(t, head, mergedAt)
	installUnremovableIgnoredTree(t, fixture.canonical, created.WorktreeDir)
	injected := errors.New("injected crash before residue removal")

	first, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-residue-resume",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now:                         func() time.Time { return mergedAt.Add(time.Hour) },
		beforeCleanupResidueRemoval: func(string) error { return injected },
	})
	if !errors.Is(err, injected) {
		t.Fatalf("cleanup interruption = %v, want %v", err, injected)
	}
	if len(first.Results) != 1 || first.Results[0].BranchDeleted || first.Results[0].BacklogID == "" {
		t.Fatalf("interrupted cleanup result = %#v", first.Results)
	}
	if _, statErr := os.Stat(created.WorktreeDir); statErr != nil {
		t.Fatalf("interrupted cleanup must leave the residue on disk: %v", statErr)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created.Branch); branchErr != nil || !exists {
		t.Fatalf("interrupted cleanup branch exists=%t err=%v", exists, branchErr)
	}

	resumed, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-residue-resume",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(2 * time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.Results) != 1 || !resumed.Results[0].Applied || !resumed.Results[0].BranchDeleted {
		t.Fatalf("resumed cleanup = %#v", resumed.Results)
	}
	if !resumed.Results[0].WorktreeResidueRemoved {
		t.Fatalf("resumed cleanup does not credit the residue removal: %#v", resumed.Results[0])
	}
	if _, statErr := os.Stat(created.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("resumed cleanup left the residue behind: %v", statErr)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created.Branch); branchErr != nil || exists {
		t.Fatalf("resumed cleanup branch exists=%t err=%v, want it retired", exists, branchErr)
	}
	var record lifecycleBacklogRecord
	content, readErr := os.ReadFile(filepath.Join(lifecycleBacklogDirectory(fixture.home), resumed.Results[0].BacklogID+".json"))
	if readErr != nil || json.Unmarshal(content, &record) != nil || record.Stage != lifecycleStageComplete {
		t.Fatalf("resumed cleanup backlog = %#v read=%v", record, readErr)
	}
}

// TestCleanupStillFailsWhenGitRefusedTheRemoval keeps the distinction the fix
// rests on: a refusal leaves the worktree registered, and a registered worktree
// is never WB's to delete behind Git's back.
func TestCleanupStillFailsWhenGitRefusedTheRemoval(t *testing.T) {
	fixture, created, head, mergedAt := prepareMergedTask(t, "cleanup-residue-refusal")
	installMergedPullRequestFixture(t, head, mergedAt)

	_, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "cleanup-residue-refusal",
		Apply: true, DeleteRemote: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
		// A worktree Git still knows about after a failed removal was refused,
		// never left behind. Simulate the refusal without simulating Git.
		beforeCleanupWorktreeRemoval: func(worktree string) {
			if err := os.WriteFile(filepath.Join(worktree, "uncommitted.txt"), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	})
	if err == nil {
		t.Fatal("cleanup accepted a refused removal")
	}
	if _, statErr := os.Stat(created.WorktreeDir); statErr != nil {
		t.Fatalf("refused removal must leave the worktree intact: %v", statErr)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created.Branch); branchErr != nil || !exists {
		t.Fatalf("refused removal branch exists=%t err=%v, want it kept", exists, branchErr)
	}
}

// TestRemoveDirectoryContentsAtClearsNestedReadOnlyResidue exercises the
// removal itself: nesting, a symlink that must be unlinked rather than
// followed, and a directory that denies the unlink until WB grants itself
// write permission on it.
func TestRemoveDirectoryContentsAtClearsNestedReadOnlyResidue(t *testing.T) {
	requireUnprivilegedResidueTest(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	nested := filepath.Join(root, "node_modules", ".pnpm", "pkg")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "index.js"), []byte("//\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "node_modules", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(nested, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(nested, 0o700) })

	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	if err := removeDirectoryContentsAt(directory, root, 0); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("residue remains: %#v", entries)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "keep.txt")); statErr != nil {
		t.Fatalf("removal followed a symlink out of the residue: %v", statErr)
	}
}
