package gitcli

import (
	"context"
	"errors"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func TestClientCurrentBranchTrimsAndReturnsStdout(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "rev-parse", "--abbrev-ref", "HEAD"}, runner.Result{Stdout: "main\n"}, nil)

	branch, err := New(fake).CurrentBranch(context.Background(), "/repo")
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if branch != "main" {
		t.Fatalf("branch = %q, want %q", branch, "main")
	}
}

func TestClientCurrentBranchWrapsAFailureAsGitError(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	underlying := errors.New("exit status 128")
	fake.ExpectArgv([]string{"git", "rev-parse", "--abbrev-ref", "HEAD"}, runner.Result{Stderr: "fatal: not a git repository"}, underlying)

	_, err := New(fake).CurrentBranch(context.Background(), "/repo")
	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("err = %v, want a *GitError", err)
	}
	if !errors.Is(err, underlying) {
		t.Fatalf("err does not wrap the underlying error: %v", err)
	}
	if got := gitErr.Error(); got == "" {
		t.Fatal("GitError.Error() returned an empty string")
	}
}

func TestClientCurrentBranchGitErrorMessageOmitsEmptyStderr(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	underlying := errors.New("exit status 1")
	fake.ExpectArgv([]string{"git", "rev-parse", "--abbrev-ref", "HEAD"}, runner.Result{}, underlying)

	_, err := New(fake).CurrentBranch(context.Background(), "/repo")
	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("err = %v, want a *GitError", err)
	}
	if got := gitErr.Error(); got != "git rev-parse --abbrev-ref HEAD: exit status 1" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestClientRevParseTrimsAndReturnsStdout(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "rev-parse", "HEAD"}, runner.Result{Stdout: "abc123\n"}, nil)

	sha, err := New(fake).RevParse(context.Background(), "/repo", "HEAD")
	if err != nil {
		t.Fatalf("RevParse: %v", err)
	}
	if sha != "abc123" {
		t.Fatalf("sha = %q", sha)
	}
}

func TestClientRevParseWrapsAFailureAsGitError(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "rev-parse", "no-such-ref"}, runner.Result{Stderr: "unknown revision"}, errors.New("exit status 128"))

	_, err := New(fake).RevParse(context.Background(), "/repo", "no-such-ref")
	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("err = %v, want a *GitError", err)
	}
}

func TestClientIsAncestorReturnsTrueOnSuccess(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "merge-base", "--is-ancestor", "base", "head"}, runner.Result{}, nil)

	ok, err := New(fake).IsAncestor(context.Background(), "/repo", "base", "head")
	if err != nil {
		t.Fatalf("IsAncestor: %v", err)
	}
	if !ok {
		t.Fatal("want true")
	}
}

func TestClientIsAncestorReturnsFalseOnExitStatus1(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "merge-base", "--is-ancestor", "base", "head"}, runner.Result{ExitCode: 1}, errors.New("exit status 1"))

	ok, err := New(fake).IsAncestor(context.Background(), "/repo", "base", "head")
	if err != nil {
		t.Fatalf("IsAncestor: %v", err)
	}
	if ok {
		t.Fatal("want false")
	}
}

func TestClientIsAncestorReturnsAGitErrorOnAnAmbiguousExitStatus(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "merge-base", "--is-ancestor", "base", "head"}, runner.Result{ExitCode: 129, Stderr: "unknown revision"}, errors.New("exit status 129"))

	ok, err := New(fake).IsAncestor(context.Background(), "/repo", "base", "head")
	if ok {
		t.Fatal("want false alongside the error")
	}
	if !errors.Is(err, errIsAncestorAmbiguous) {
		t.Fatalf("err = %v, want it to wrap errIsAncestorAmbiguous", err)
	}
	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("err = %v, want a *GitError", err)
	}
}

func TestClientFetchSucceeds(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "fetch", "--quiet", "origin"}, runner.Result{}, nil)

	if err := New(fake).Fetch(context.Background(), "/repo", "origin"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
}

func TestClientFetchWrapsAFailureAsGitError(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "fetch", "--quiet", "origin"}, runner.Result{Stderr: "could not resolve host"}, errors.New("exit status 128"))

	err := New(fake).Fetch(context.Background(), "/repo", "origin")
	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("err = %v, want a *GitError", err)
	}
}
