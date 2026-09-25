package locallink

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

// TestResolveRunnerDefaultsToTheRealRunner covers resolveRunner's own
// branches directly: a nil field (production's zero value) resolves to the
// real runner, and an injected fake passes through unchanged.
func TestResolveRunnerDefaultsToTheRealRunner(t *testing.T) {
	t.Parallel()
	if _, ok := resolveRunner(nil).(runner.Real); !ok {
		t.Fatalf("resolveRunner(nil) = %T, want runner.Real", resolveRunner(nil))
	}
	fake := runnertest.New(t)
	if resolveRunner(fake) != runner.Runner(fake) {
		t.Fatal("resolveRunner did not pass an injected runner through unchanged")
	}
}

// TestExecGitTrackedChangesUsesTheConfiguredRunner proves ExecGit.Runner
// reaches git.run/runBounded: the fake, not a real git process, answers
// the call.
func TestExecGitTrackedChangesUsesTheConfiguredRunner(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "status", "--porcelain", "--untracked-files=no"},
		runner.Result{Stdout: " M tracked.txt\n"}, nil)

	git := ExecGit{Runner: fake}
	changes, err := git.TrackedChanges(context.Background(), "/repo")
	if err != nil {
		t.Fatalf("TrackedChanges: %v", err)
	}
	if len(changes) != 1 || changes[0] != "tracked.txt" {
		t.Fatalf("changes = %v, want [tracked.txt]", changes)
	}
}

// TestExecGitRunReportsAFailure covers runBounded's error branch through
// ExecGit.run, without a real process.
func TestExecGitRunReportsAFailure(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	wantErr := errors.New("exit status 128")
	fake.Expect(func(c runnertest.Call) bool { return c.Name == "git" }, runner.Result{ExitCode: 128}, wantErr)

	git := ExecGit{Runner: fake}
	if _, err := git.TrackedChanges(context.Background(), "/repo"); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want it to wrap %v", err, wantErr)
	}
}

// TestExecNodeFrozenInstallUsesTheConfiguredRunner proves ExecNode.Runner
// reaches the package-manager invocation through runBounded.
func TestExecNodeFrozenInstallUsesTheConfiguredRunner(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fake := runnertest.New(t)
	fake.Expect(func(c runnertest.Call) bool { return c.Name == "pnpm" }, runner.Result{}, nil)

	node := ExecNode{Runner: fake}
	if err := node.FrozenInstall(context.Background(), dir); err != nil {
		t.Fatalf("FrozenInstall: %v", err)
	}
}

// TestExecNodeVerifyRuntimeGraphSendsTheProbeScriptOnStdin proves
// verifyRuntimeGraph reaches node through RunOpts with the probe script as
// stdin and WB_LINKED_PACKAGES in the environment.
func TestExecNodeVerifyRuntimeGraphSendsTheProbeScriptOnStdin(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	var seenStdin string
	var seenEnv []string
	fake.Expect(func(c runnertest.Call) bool {
		seenStdin = string(c.Opts.Stdin)
		seenEnv = c.Opts.Env
		return c.Name == "node"
	}, runner.Result{Stdout: `{"visited":1,"mismatches":[],"error":""}`}, nil)

	node := ExecNode{Runner: fake}
	if err := node.verifyRuntimeGraph(context.Background(), t.TempDir(), []string{"@acme/core"}); err != nil {
		t.Fatalf("verifyRuntimeGraph: %v", err)
	}
	if seenStdin != nodeRuntimeGraphProbeScript {
		t.Fatal("verifyRuntimeGraph did not pipe the probe script on stdin")
	}
	found := false
	for _, entry := range seenEnv {
		if entry == `WB_LINKED_PACKAGES=["@acme/core"]` {
			found = true
		}
	}
	if !found {
		t.Fatalf("env = %v, want WB_LINKED_PACKAGES set", seenEnv)
	}
}
