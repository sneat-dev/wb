package orchestrate

import (
	"context"
	"strings"
	"testing"
)

//nolint:paralleltest // newCreateFixture calls t.Setenv for its projects root
func TestResolvePullRequestCreateWorktreeFindsAnExactPathOrTask(t *testing.T) {
	fixture := newCreateFixture(t)
	const task = "resolve-pr-task"
	worktree := fixture.createWorktree(t, task, "feature/resolve-pr", "main", "change.txt")
	for _, argument := range []string{worktree, task} {
		resolved, err := ResolvePullRequestCreateWorktree(context.Background(), fixture.projects, argument)
		if err != nil || resolved != worktree {
			t.Fatalf("argument=%q resolved=%q error=%v, want %q", argument, resolved, err, worktree)
		}
	}
	missing := "missing-task"
	if _, err := ResolvePullRequestCreateWorktree(context.Background(), fixture.projects, missing); err == nil || !strings.Contains(err.Error(), "no worktree or task") {
		t.Fatalf("missing path error=%v", err)
	}
}
