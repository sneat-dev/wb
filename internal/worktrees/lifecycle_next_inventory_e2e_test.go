//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

type lifecycleNextNativeGitObservation struct {
	runner.Runner
	after func(string, []string, runner.Result, error)
}

func (observation *lifecycleNextNativeGitObservation) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	result, err := observation.Runner.RunOpts(ctx, dir, opts, name, args...)
	if name == "git" && observation.after != nil {
		observation.after(dir, args, result, err)
	}
	return result, err
}

func TestE2ELifecycleNextCommonDirectoryPreservesFallbackContract(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "missing")
	if got := gitCommonDir(context.Background(), missing); got != missing {
		t.Fatalf("native failed query fallback=%q", got)
	}
	// A configured Runner is permitted to return successful empty stdout.
	// This is the established collaborator contract, not a claim about stock Git.
	fake := runnertest.New(t)
	fake.Expect(func(call runnertest.Call) bool {
		return call.Name == "git" && reflect.DeepEqual(call.Args, []string{"-C", missing, "rev-parse", "--path-format=absolute", "--git-common-dir"})
	}, runner.Result{CombinedOutput: " \n"}, nil)
	if got := gitCommonDir(withGitRunner(context.Background(), fake), missing); got != missing {
		t.Fatalf("empty configured observation fallback=%q", got)
	}
}

//nolint:paralleltest // HOME and XDG_CONFIG_HOME configure the native public inventory admission.
func TestE2ELifecycleNextInventoryRetainsRootChangedAfterDiscovery(t *testing.T) {
	projects := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	canonical := newLegacyClone(t, projects, "acme", "app")
	root := filepath.Join(canonical, ".worktrees")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	observed := false
	observer := &lifecycleNextNativeGitObservation{Runner: runner.New(), after: func(_ string, args []string, _ runner.Result, err error) {
		if !observed && err == nil && reflect.DeepEqual(args, []string{"-C", canonical, "rev-parse", "--path-format=absolute", "--git-common-dir"}) {
			observed = true
			if err := os.Remove(root); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(root, []byte("retained successor root"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}}
	outcome, err := ListWithDiagnostics(withGitRunner(context.Background(), observer), ListOptions{ProjectsRoot: projects, Workers: 1})
	if !observed || err == nil || !strings.Contains(err.Error(), "read canonical local worktrees under "+root) || len(outcome.Results) != 0 {
		t.Fatalf("changed inventory=%+v %v observed=%v", outcome, err, observed)
	}
	if data, err := os.ReadFile(root); err != nil || string(data) != "retained successor root" {
		t.Fatalf("inventory mutated successor: %q %v", data, err)
	}
}
