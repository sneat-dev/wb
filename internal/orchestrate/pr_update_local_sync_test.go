package orchestrate

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestPullRequestUpdateSyncsOnlyAnActiveClaimedWorktreeAtTheExactHead(t *testing.T) {
	fixture := newEngineFixture(t)
	options := PullRequestUpdateOptions{ProjectsRoot: fixture.githubDir, Repository: fixture.repository.Slug, PullRequest: "7"}
	if note := syncOwnedPullRequestUpdateWorktree(context.Background(), options, "feature/missing", "head"); !strings.Contains(note, "no linked worktree") {
		t.Fatalf("unowned branch sync note=%q", note)
	}
	const branch = "feature/update-sync"
	source := createMergeSource(t, fixture, "update-sync-task", branch, "change.txt", "change\n")
	head := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	runEngineGit(t, source.WorktreeDir, "push", "origin", "HEAD:refs/heads/"+branch)
	if note := syncOwnedPullRequestUpdateWorktree(context.Background(), options, branch, head); !strings.Contains(note, "fast-forwarded worktree") {
		t.Fatalf("exact claimed worktree sync note=%q", note)
	}
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "unfinished.txt"), "unfinished\n")
	if note := syncOwnedPullRequestUpdateWorktree(context.Background(), options, branch, head); !strings.Contains(note, "uncommitted changes") {
		t.Fatalf("dirty claimed worktree sync note=%q", note)
	}
}
