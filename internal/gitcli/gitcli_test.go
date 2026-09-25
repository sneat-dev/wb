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

func TestClientRunTrimsAndReturnsStdout(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "status", "--porcelain"}, runner.Result{Stdout: "M file.go\n"}, nil)

	out, err := New(fake).Run(context.Background(), "/repo", "status", "--porcelain")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "M file.go" {
		t.Fatalf("out = %q", out)
	}
}

func TestClientRunWrapsAFailureAsGitError(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "status"}, runner.Result{Stderr: "not a git repository"}, errors.New("exit status 128"))

	_, err := New(fake).Run(context.Background(), "/repo", "status")
	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("err = %v, want a *GitError", err)
	}
}

func TestClientAtomicRenameRefsSucceeds(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "checkout", "--detach", "sha"}, runner.Result{}, nil)
	fake.Expect(func(c runnertest.Call) bool {
		return c.Op == "RunStdin" && c.Name == "git" && c.Args[0] == "update-ref" && c.Args[1] == "--stdin" &&
			c.Stdin == "start\ncreate refs/heads/main sha\ndelete refs/heads/master sha\nprepare\ncommit\n"
	}, runner.Result{}, nil)

	if err := New(fake).AtomicRenameRefs(context.Background(), "/repo", "master", "main", "sha"); err != nil {
		t.Fatalf("AtomicRenameRefs: %v", err)
	}
}

func TestClientAtomicRenameRefsReportsADetachFailureNamingSource(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "checkout", "--detach", "sha"}, runner.Result{Stderr: "unknown revision"}, errors.New("exit status 128"))

	err := New(fake).AtomicRenameRefs(context.Background(), "/repo", "master", "main", "sha")
	if err == nil {
		t.Fatal("want an error for a failed detach")
	}
	if got, want := err.Error(), "detach HEAD at verified master: exit status 128: unknown revision"; got != want {
		t.Fatalf("err = %q, want %q", got, want)
	}
}

func TestClientAtomicRenameRefsReportsATransactionFailureNamingBothBranches(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "checkout", "--detach", "sha"}, runner.Result{}, nil)
	fake.Expect(func(c runnertest.Call) bool { return c.Op == "RunStdin" }, runner.Result{Stderr: "stale ref"}, errors.New("exit status 1"))

	err := New(fake).AtomicRenameRefs(context.Background(), "/repo", "master", "main", "sha")
	if err == nil {
		t.Fatal("want an error for a failed transaction")
	}
	if got, want := err.Error(), "atomically rename local master to main at sha: exit status 1: stale ref"; got != want {
		t.Fatalf("err = %q, want %q", got, want)
	}
}

func TestClientAttachHeadSucceeds(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "symbolic-ref", "HEAD", "refs/heads/main"}, runner.Result{}, nil)

	if err := New(fake).AttachHead(context.Background(), "/repo", "main"); err != nil {
		t.Fatalf("AttachHead: %v", err)
	}
}

func TestClientAttachHeadReportsAFailureNamingDestination(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "symbolic-ref", "HEAD", "refs/heads/main"}, runner.Result{Stderr: "cannot lock ref"}, errors.New("exit status 1"))

	err := New(fake).AttachHead(context.Background(), "/repo", "main")
	if err == nil {
		t.Fatal("want an error for a failed attach")
	}
	if got, want := err.Error(), "attach HEAD to renamed local main: exit status 1: cannot lock ref"; got != want {
		t.Fatalf("err = %q, want %q", got, want)
	}
}

func TestClientRefExistsReturnsTrueOnSuccess(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "rev-parse", "--verify", "--quiet", "refs/heads/main"}, runner.Result{}, nil)

	ok, err := New(fake).RefExists(context.Background(), "/repo", "refs/heads/main")
	if err != nil {
		t.Fatalf("RefExists: %v", err)
	}
	if !ok {
		t.Fatal("want true")
	}
}

func TestClientRefExistsReturnsFalseOnExitStatus1(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "rev-parse", "--verify", "--quiet", "refs/heads/main"}, runner.Result{ExitCode: 1}, errors.New("exit status 1"))

	ok, err := New(fake).RefExists(context.Background(), "/repo", "refs/heads/main")
	if err != nil {
		t.Fatalf("RefExists: %v", err)
	}
	if ok {
		t.Fatal("want false")
	}
}

func TestClientRefExistsReportsAnAmbiguousFailureOmittingQuietFromTheMessage(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "rev-parse", "--verify", "--quiet", "refs/heads/main"}, runner.Result{ExitCode: 128, Stderr: "not a git repository"}, errors.New("exit status 128"))

	ok, err := New(fake).RefExists(context.Background(), "/repo", "refs/heads/main")
	if ok {
		t.Fatal("want false alongside the error")
	}
	if got, want := err.Error(), "git rev-parse --verify refs/heads/main: exit status 128: not a git repository"; got != want {
		t.Fatalf("err = %q, want %q", got, want)
	}
}
