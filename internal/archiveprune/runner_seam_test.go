package archiveprune

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

// TestRunGitReturnsStdoutOnSuccess and its siblings below cover runGit and
// the ref-listing helpers built on it against runnertest.Fake, independent
// of Evaluate's real-git integration tests: these prove the exact argv and
// error text the runner.Runner seam carries, without needing a real
// repository or remote.

func TestRunGitReturnsStdoutOnSuccess(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "tag"}, runner.Result{Stdout: "v1\nv2\n"}, nil)

	output, err := runGit(context.Background(), fake, "/repo", "tag")
	if err != nil {
		t.Fatalf("runGit: %v", err)
	}
	if output != "v1\nv2\n" {
		t.Fatalf("output = %q", output)
	}
}

func TestRunGitReportsANonZeroExitWithStderr(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	wantErr := errors.New("exit status 128")
	fake.ExpectArgv([]string{"git", "tag"}, runner.Result{ExitCode: 128, Stderr: "fatal: not a git repository"}, wantErr)

	_, err := runGit(context.Background(), fake, "/repo", "tag")
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want it to wrap %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "fatal: not a git repository") {
		t.Fatalf("err = %v, want it to carry git's stderr", err)
	}
}

func TestRunGitReportsALaunchFailureWithoutStderrText(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	wantErr := errors.New("exec: \"git\": executable file not found in $PATH")
	fake.ExpectArgv([]string{"git", "tag"}, runner.Result{}, wantErr)

	_, err := runGit(context.Background(), fake, "/repo", "tag")
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want it to wrap %v", err, wantErr)
	}
	if strings.Contains(err.Error(), ": : ") {
		t.Fatalf("err = %v, want no dangling stderr separator for a launch failure", err)
	}
}

func TestLocalOnlyBranchesReportsBranchesAbsentFromOrigin(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "for-each-ref", "--format=%(refname:short)", "refs/heads/"},
		runner.Result{Stdout: "main\nfeature-a\n"}, nil)
	fake.ExpectArgv([]string{"git", "ls-remote", "--heads", "origin"},
		runner.Result{Stdout: "abc123\trefs/heads/main\n"}, nil)

	missing, err := localOnlyBranches(context.Background(), fake, "/repo")
	if err != nil {
		t.Fatalf("localOnlyBranches: %v", err)
	}
	if len(missing) != 1 || missing[0] != "feature-a" {
		t.Fatalf("missing = %v, want [feature-a]", missing)
	}
}

func TestLocalOnlyBranchesSurfacesARemoteLookupFailure(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "for-each-ref", "--format=%(refname:short)", "refs/heads/"},
		runner.Result{Stdout: "main\n"}, nil)
	wantErr := errors.New("exit status 128")
	fake.ExpectArgv([]string{"git", "ls-remote", "--heads", "origin"},
		runner.Result{ExitCode: 128}, wantErr)

	if _, err := localOnlyBranches(context.Background(), fake, "/repo"); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want it to wrap %v", err, wantErr)
	}
}

func TestUnpushedTagNamesReportsTagsAbsentFromOrigin(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "tag"}, runner.Result{Stdout: "v1\nv2\n"}, nil)
	fake.ExpectArgv([]string{"git", "ls-remote", "--tags", "origin"},
		runner.Result{Stdout: "abc\trefs/tags/v1\ndef\trefs/tags/v2^{}\n"}, nil)

	missing, err := unpushedTagNames(context.Background(), fake, "/repo")
	if err != nil {
		t.Fatalf("unpushedTagNames: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want none: v2 is published as an annotated tag (peeled with ^{})", missing)
	}
}

func TestUnpushedTagNamesReportsATagListFailure(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	wantErr := errors.New("exit status 128")
	fake.ExpectArgv([]string{"git", "tag"}, runner.Result{ExitCode: 128}, wantErr)

	if _, err := unpushedTagNames(context.Background(), fake, "/repo"); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want it to wrap %v", err, wantErr)
	}
}

func TestLinkedWorktreePathsSkipsTheCanonicalCheckout(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	porcelain := "worktree /repo\nHEAD abc\nbranch refs/heads/main\n\n" +
		"worktree /other/linked\nHEAD def\nbranch refs/heads/feature\n\n"
	fake.ExpectArgv([]string{"git", "worktree", "list", "--porcelain"}, runner.Result{Stdout: porcelain}, nil)

	paths, err := linkedWorktreePaths(context.Background(), fake, "/repo")
	if err != nil {
		t.Fatalf("linkedWorktreePaths: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/other/linked" {
		t.Fatalf("paths = %v, want [/other/linked] (the primary checkout excluded)", paths)
	}
}

func TestLinkedWorktreePathsReportsAWorktreeListFailure(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	wantErr := errors.New("exit status 128")
	fake.ExpectArgv([]string{"git", "worktree", "list", "--porcelain"}, runner.Result{ExitCode: 128}, wantErr)

	if _, err := linkedWorktreePaths(context.Background(), fake, "/repo"); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want it to wrap %v", err, wantErr)
	}
}

// TestResolveRunnerDefaultsToTheRealRunner covers Options.Runner's own
// resolve helper directly: a nil field resolves to the real runner, and an
// injected fake passes through unchanged.
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
