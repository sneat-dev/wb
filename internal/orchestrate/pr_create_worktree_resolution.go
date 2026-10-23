package orchestrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// resolvePullRequestCreateWorktree resolves the CLI's `<worktree|task>`
// argument. An existing directory is used as-is; anything else is looked up
// as a task name against the fleet's worktree inventory. Resolution never
// runs a network fetch: the guard and the base-branch fetch that follow are
// where a caller pays that cost, once, for the worktree it actually meant.
// ResolvePullRequestCreateWorktree exports resolvePullRequestCreateWorktree
// for cmd/wb's best-effort #615 prompt-suggestion lookup, which needs the
// same worktree-path-or-task-name resolution `wb pr create` itself uses but
// runs before CreatePullRequest is called.
func ResolvePullRequestCreateWorktree(ctx context.Context, projectsRoot, argument string) (string, error) {
	return resolvePullRequestCreateWorktree(ctx, projectsRoot, argument)
}

func resolvePullRequestCreateWorktree(ctx context.Context, projectsRoot, argument string) (string, error) {
	return resolvePullRequestCreateWorktreeWithPathResolver(ctx, projectsRoot, argument, filepath.Abs)
}

func resolvePullRequestCreateWorktreeWithPathResolver(ctx context.Context, projectsRoot, argument string, abs func(string) (string, error)) (string, error) {
	if info, statErr := os.Stat(argument); statErr == nil && info.IsDir() {
		// Absolute: ReadManifest (and the secure directory helpers it uses)
		// refuse a relative path outright, and "." is the CLI's own default.
		absolute, absErr := abs(argument)
		if absErr != nil {
			return "", fmt.Errorf("resolve %s to an absolute path: %w", argument, absErr)
		}
		return absolute, nil
	}
	entries, err := worktrees.List(ctx, worktrees.ListOptions{ProjectsRoot: projectsRoot, Task: argument})
	if err != nil {
		return "", fmt.Errorf("resolve task %q: %w", argument, err)
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("no worktree or task %q found", argument)
	}
	if len(entries) > 1 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Repository+" "+entry.WorktreeDir)
		}
		return "", fmt.Errorf("task %q has more than one worktree (%s); pass a path instead", argument, strings.Join(names, "; "))
	}
	return entries[0].WorktreeDir, nil
}
