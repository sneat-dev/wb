//go:build e2e

package orchestrate

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
)

func terminalSyncOwnerCanonicalFixture(t *testing.T) (engineFixture, string, string) {
	t.Helper()
	f := newExplicitRootEngineFixture(t)
	before := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "HEAD"))
	updater := filepath.Join(t.TempDir(), "updater")
	runEngineGit(t, filepath.Dir(updater), "clone", f.repository.CloneURL, updater)
	runEngineGit(t, updater, "config", "user.name", "WB Test")
	runEngineGit(t, updater, "config", "user.email", "wb@example.test")
	writeEngineFile(t, filepath.Join(updater, "remote-advance.txt"), "actual remote advance\n")
	runEngineGit(t, updater, "add", "-A")
	runEngineGit(t, updater, "commit", "-m", "private canonical target advance")
	after := strings.TrimSpace(runEngineGit(t, updater, "rev-parse", "HEAD"))
	runEngineGit(t, updater, "push", "origin", "main")
	if contains, err := isMergeAncestor(t.Context(), updater, before, after); err != nil || !contains || before == after {
		t.Fatalf("native forward graph=%t %v", contains, err)
	}
	return f, before, after
}

type terminalSyncOwnerFaultRunner struct {
	runner.Runner
	path, stage, landing     string
	sentinel                 error
	consumed, merged         bool
	afterHeadFailureAncestry bool
}

func (r *terminalSyncOwnerFaultRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if dir == r.path && name == "git" {
		match := false
		switch r.stage {
		case "branch":
			match = reflect.DeepEqual(args, []string{"branch", "--show-current"})
		case "status":
			match = reflect.DeepEqual(args, []string{"status", "--porcelain=v1"})
		case "before HEAD":
			match = !r.merged && reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"})
		case "fetch":
			match = reflect.DeepEqual(args, []string{"fetch", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"})
		case "merge":
			match = reflect.DeepEqual(args, []string{"merge", "--ff-only", "refs/remotes/origin/main"})
		case "after HEAD":
			match = r.merged && reflect.DeepEqual(args, []string{"rev-parse", "--verify", "HEAD^{commit}"})
		case "ancestry":
			match = r.merged && len(args) == 3 && args[0] == "merge-base" && args[1] == r.landing && args[2] == r.landing
		}
		if match && !r.consumed {
			r.consumed = true
			return runner.Result{}, r.sentinel
		}
		if r.stage == "after HEAD" && r.consumed && reflect.DeepEqual(args, []string{"merge-base", r.landing, ""}) {
			r.afterHeadFailureAncestry = true
		}
	}
	result, err := r.Runner.RunOpts(ctx, dir, opts, name, args...)
	if dir == r.path && name == "git" && reflect.DeepEqual(args, []string{"merge", "--ff-only", "refs/remotes/origin/main"}) && err == nil && result.ExitCode == 0 {
		r.merged = true
	}
	return result, err
}

type terminalSyncOwnerContextKey struct{}
