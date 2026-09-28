package orchestrate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func TestResolveAddPathsUsesInjectedRunnerForDeletion(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "status", "--porcelain", "--", "doomed.go"},
		runner.Result{CombinedOutput: " D doomed.go\n"}, nil)
	paths, err := resolveAddPaths(context.Background(), fake, worktree, []string{"doomed.go"})
	if err != nil || len(paths) != 1 || paths[0] != "doomed.go" {
		t.Fatalf("resolved paths=%v err=%v", paths, err)
	}
	if calls := fake.Calls(); len(calls) != 1 || calls[0].Dir != worktree || !calls[0].Opts.CaptureCombined {
		t.Fatalf("runner calls = %+v", calls)
	}
}

func TestResolveAddPathsReportsRunnerFailure(t *testing.T) {
	t.Parallel()
	worktree := t.TempDir()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "status", "--porcelain", "--", "doomed.go"},
		runner.Result{}, errors.New("git unavailable"))
	_, err := resolveAddPaths(context.Background(), fake, worktree, []string{"doomed.go"})
	if err == nil || !strings.Contains(err.Error(), "git unavailable") || strings.Contains(err.Error(), "match nothing") {
		t.Fatalf("resolveAddPaths error = %v", err)
	}
}
