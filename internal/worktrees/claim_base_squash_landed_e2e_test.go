//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// squashLandedBaseFixture reproduces a worktree stacked on a base branch that
// was squash-merged: the claimed base commit leaves HEAD's ancestry, while a
// squash commit with the identical tree takes its place.
func squashLandedBaseFixture(t *testing.T) (*gitFixture, CreateResult, string) {
	t.Helper()
	f := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: f.projectsRoot, Operation: "squash-landed-base", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := created[0]
	base := gitTestOutput(t, c.WorktreeDir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(c.WorktreeDir, "lane.txt"), []byte("stacked work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, c.WorktreeDir, "add", "lane.txt")
	gitTest(t, c.WorktreeDir, "commit", "-m", "work stacked on the base")
	return f, c, base
}

// equalTreeCommit makes a commit outside the base's history whose tree is the
// base's tree, as a squash merge of the base branch would.
func equalTreeCommit(t *testing.T, dir, base string) string {
	t.Helper()
	return gitTestOutput(t, dir, "commit-tree", base+"^{tree}", "-m", "squash-merge the base branch")
}

//nolint:paralleltest // The Git fixture configures process-local identity and paths.
func TestE2EActiveClaimAcceptsBaseSquashLandedUnderTheWork(t *testing.T) {
	f, c, base := squashLandedBaseFixture(t)
	squash := equalTreeCommit(t, c.WorktreeDir, base)
	gitTest(t, c.WorktreeDir, "push", "--force", "origin", squash+":refs/heads/main")
	gitTest(t, c.WorktreeDir, "fetch", "origin", "main")
	gitTest(t, c.WorktreeDir, "rebase", "--onto", squash, base)
	if _, _, _, err := activeWorkLogClaim(f.home, c.WorktreeDir); err != nil {
		t.Fatalf("claim on a squash-landed base was refused: %v", err)
	}
}

//nolint:paralleltest // The Git fixture configures process-local identity and paths.
func TestE2EActiveClaimRefusesEqualTreeBaseThatNeverLanded(t *testing.T) {
	f, c, base := squashLandedBaseFixture(t)
	gitTest(t, c.WorktreeDir, "rebase", "--onto", equalTreeCommit(t, c.WorktreeDir, base), base)
	if _, _, _, err := activeWorkLogClaim(f.home, c.WorktreeDir); err == nil {
		t.Fatal("claim was accepted on an equal-tree base that is not on the default branch")
	}
}

//nolint:paralleltest // The Git fixture configures process-local identity and paths.
func TestE2EActiveClaimRefusesBaseWhoseContentLeftTheHistory(t *testing.T) {
	f, c, base := squashLandedBaseFixture(t)
	if err := os.WriteFile(filepath.Join(c.WorktreeDir, "other.txt"), []byte("different content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, c.WorktreeDir, "add", "other.txt")
	tree := gitTestOutput(t, c.WorktreeDir, "write-tree")
	gitTest(t, c.WorktreeDir, "reset", "-q", "HEAD", "--", "other.txt")
	if err := os.Remove(filepath.Join(c.WorktreeDir, "other.txt")); err != nil {
		t.Fatal(err)
	}
	unrelated := gitTestOutput(t, c.WorktreeDir, "commit-tree", tree, "-m", "a base with different content")
	gitTest(t, c.WorktreeDir, "rebase", "--onto", unrelated, base)
	if _, _, _, err := activeWorkLogClaim(f.home, c.WorktreeDir); err == nil {
		t.Fatal("claim was accepted although the base content is absent from HEAD's history")
	}
}

//nolint:paralleltest // The Git fixture configures process-local identity and paths.
func TestE2ESquashLandedBaseRejectsAnUnknownBaseObject(t *testing.T) {
	_, c, _ := squashLandedBaseFixture(t)
	head := gitTestOutput(t, c.WorktreeDir, "rev-parse", "HEAD")
	if squashLandedClaimBase(c.WorktreeDir, "0123456789abcdef0123456789abcdef01234567", head) {
		t.Fatal("an unknown base object counted as squash-landed")
	}
}
