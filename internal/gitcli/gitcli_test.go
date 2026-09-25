package gitcli

import (
	"context"
	"errors"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/testsweep"
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

// TestClientIsAncestorErrorReturnSweptByTestsweep is task-8's "prove it on
// one real consumer" step: internal/testsweep.Sweep drives IsAncestor's
// happy path (one runner call) and reruns it with that one call failing,
// in place of TestClientIsAncestorReturnsAGitErrorOnAnAmbiguousExitStatus
// above hand-writing the same case. IsAncestor's only error return is
// reached this way: an injected failure carries no exit code, so it always
// lands in the "neither yes nor no" ambiguous branch, never the "exit
// status 1 means false, nil error" branch -- that branch returns no error
// at all, so it is out of Sweep's scope by construction. internal/gitcli
// was already at 100% statement coverage before this test (PR-1's
// hand-written cases covered every branch), so this adds no newly covered
// statement; it proves the generic sweep reaches the same production error
// path a real consumer of runnertest.Fake would.
func TestClientIsAncestorErrorReturnSweptByTestsweep(t *testing.T) {
	t.Parallel()
	failErr := errors.New("boom")

	body := func(fake *runnertest.Fake) error {
		fake.ExpectArgv([]string{"git", "merge-base", "--is-ancestor", "base", "head"}, runner.Result{}, nil)
		_, err := New(fake).IsAncestor(context.Background(), "/repo", "base", "head")
		return err
	}

	testsweep.Sweep(t, func() *runnertest.Fake { return runnertest.New(t) }, failErr, body,
		func(t testing.TB, callNum, total int, err error) {
			if total != 1 {
				t.Fatalf("total = %d, want 1: IsAncestor makes exactly one runner call", total)
			}
			var gitErr *GitError
			if !errors.As(err, &gitErr) {
				t.Fatalf("call %d err = %v, want a *GitError", callNum, err)
			}
			if !errors.Is(err, failErr) {
				t.Fatalf("call %d err = %v, want it to wrap the injected failure", callNum, err)
			}
			if !errors.Is(err, errIsAncestorAmbiguous) {
				t.Fatalf("call %d err = %v, want it to wrap errIsAncestorAmbiguous", callNum, err)
			}
		})
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

// The tests below cover the methods gitcli.go adds for
// spec/plans/coverage-to-100 task-17 (internal/orchestrate's Git port). Each
// runVoid-backed method gets one success case and one failure case proving
// it surfaces a *GitError; c.run's own format tests above already prove the
// error text itself is byte-identical to what a direct exec.CommandContext
// call produced.

func TestClientWorktreeRemoveForce(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "worktree", "remove", "--force", "/scratch"}, runner.Result{}, nil)
	if err := New(fake).WorktreeRemoveForce(context.Background(), "/repo", "/scratch"); err != nil {
		t.Fatalf("WorktreeRemoveForce: %v", err)
	}
}

func TestClientWorktreeAddDetached(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "worktree", "add", "--detach", "/scratch", "abc123"}, runner.Result{}, nil)
	if err := New(fake).WorktreeAddDetached(context.Background(), "/repo", "/scratch", "abc123"); err != nil {
		t.Fatalf("WorktreeAddDetached: %v", err)
	}

	fake.ExpectArgv([]string{"git", "worktree", "add", "--detach", "/scratch", "abc123"}, runner.Result{Stderr: "fatal: already exists"}, errors.New("exit status 128"))
	err := New(fake).WorktreeAddDetached(context.Background(), "/repo", "/scratch", "abc123")
	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("err = %v, want a *GitError", err)
	}
}

func TestClientCherryPickNoCommit(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "cherry-pick", "--no-commit", "sha1", "sha2"}, runner.Result{}, nil)
	if err := New(fake).CherryPickNoCommit(context.Background(), "/repo", "sha1", "sha2"); err != nil {
		t.Fatalf("CherryPickNoCommit: %v", err)
	}
}

func TestClientCommitNoVerify(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "commit", "--no-verify", "-m", "aggregate"}, runner.Result{}, nil)
	if err := New(fake).CommitNoVerify(context.Background(), "/repo", "aggregate"); err != nil {
		t.Fatalf("CommitNoVerify: %v", err)
	}
}

func TestClientCherryPick(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "cherry-pick", "sha1"}, runner.Result{}, nil)
	if err := New(fake).CherryPick(context.Background(), "/repo", "sha1"); err != nil {
		t.Fatalf("CherryPick: %v", err)
	}

	fake.ExpectArgv([]string{"git", "cherry-pick", "sha1"}, runner.Result{Stderr: "conflict"}, errors.New("exit status 1"))
	err := New(fake).CherryPick(context.Background(), "/repo", "sha1")
	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("err = %v, want a *GitError", err)
	}
}

func TestClientPushForceWithLeaseHead(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "push", "--force-with-lease=refs/heads/feature:oldsha", "origin", "HEAD:refs/heads/feature"}, runner.Result{}, nil)
	if err := New(fake).PushForceWithLeaseHead(context.Background(), "/repo", "feature", "oldsha"); err != nil {
		t.Fatalf("PushForceWithLeaseHead: %v", err)
	}

	fake.ExpectArgv([]string{"git", "push", "--force-with-lease=refs/heads/feature:oldsha", "origin", "HEAD:refs/heads/feature"}, runner.Result{Stderr: "stale info"}, errors.New("exit status 1"))
	err := New(fake).PushForceWithLeaseHead(context.Background(), "/repo", "feature", "oldsha")
	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("err = %v, want a *GitError", err)
	}
}

func TestClientFetchRefs(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "fetch", "origin", "main", "feature"}, runner.Result{}, nil)
	if err := New(fake).FetchRefs(context.Background(), "/repo", "origin", "main", "feature"); err != nil {
		t.Fatalf("FetchRefs: %v", err)
	}
}

func TestClientRevListReverseRange(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "rev-list", "--reverse", "base..head"}, runner.Result{Stdout: "sha1\nsha2\n"}, nil)
	out, err := New(fake).RevListReverseRange(context.Background(), "/repo", "base", "head")
	if err != nil {
		t.Fatalf("RevListReverseRange: %v", err)
	}
	if out != "sha1\nsha2" {
		t.Fatalf("out = %q", out)
	}

	fake.ExpectArgv([]string{"git", "rev-list", "--reverse", "base..head"}, runner.Result{}, errors.New("exit status 128"))
	if _, err := New(fake).RevListReverseRange(context.Background(), "/repo", "base", "head"); err == nil {
		t.Fatal("want an error")
	}
}

func TestClientRemotePushURL(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "remote", "get-url", "--push", "origin"}, runner.Result{Stdout: "git@github.com:o/r.git\n"}, nil)
	url, err := New(fake).RemotePushURL(context.Background(), "/repo", "origin")
	if err != nil {
		t.Fatalf("RemotePushURL: %v", err)
	}
	if url != "git@github.com:o/r.git" {
		t.Fatalf("url = %q", url)
	}
}

func TestClientStatusPorcelain(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "status", "--porcelain"}, runner.Result{Stdout: " M file.go\n"}, nil)
	status, err := New(fake).StatusPorcelain(context.Background(), "/repo")
	if err != nil {
		t.Fatalf("StatusPorcelain: %v", err)
	}
	if status != "M file.go" {
		t.Fatalf("status = %q", status)
	}
}

func TestClientBranchShowCurrent(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "branch", "--show-current"}, runner.Result{Stdout: "feature\n"}, nil)
	branch, err := New(fake).BranchShowCurrent(context.Background(), "/repo")
	if err != nil {
		t.Fatalf("BranchShowCurrent: %v", err)
	}
	if branch != "feature" {
		t.Fatalf("branch = %q", branch)
	}
}

// TestClientMergeBaseIsAncestorStrictErrorsOnExitStatus1 is the behaviour
// that makes this method deliberately different from IsAncestor: exit
// status 1 ("not an ancestor") is still an error here, because the caller
// this replaced (internal/orchestrate's pr_land_local_sync.go) never
// inspected a bool -- it only ever asked "did the call fail".
func TestClientMergeBaseIsAncestorStrictErrorsOnExitStatus1(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "merge-base", "--is-ancestor", "HEAD", "origin/feature"}, runner.Result{ExitCode: 1}, errors.New("exit status 1"))
	if err := New(fake).MergeBaseIsAncestorStrict(context.Background(), "/repo", "HEAD", "origin/feature"); err == nil {
		t.Fatal("want an error on exit status 1")
	}
}

func TestClientMergeBaseIsAncestorStrictSucceeds(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "merge-base", "--is-ancestor", "HEAD", "origin/feature"}, runner.Result{}, nil)
	if err := New(fake).MergeBaseIsAncestorStrict(context.Background(), "/repo", "HEAD", "origin/feature"); err != nil {
		t.Fatalf("MergeBaseIsAncestorStrict: %v", err)
	}
}

func TestClientBranchSetUpstreamTo(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "branch", "--set-upstream-to=origin/feature", "feature"}, runner.Result{}, nil)
	if err := New(fake).BranchSetUpstreamTo(context.Background(), "/repo", "origin/feature", "feature"); err != nil {
		t.Fatalf("BranchSetUpstreamTo: %v", err)
	}
}

func TestClientMergeTreeWriteTree(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "merge-tree", "--write-tree", "candidate", "target"}, runner.Result{Stdout: "treeid\n"}, nil)
	tree, err := New(fake).MergeTreeWriteTree(context.Background(), "/repo", "candidate", "target")
	if err != nil {
		t.Fatalf("MergeTreeWriteTree: %v", err)
	}
	if tree != "treeid" {
		t.Fatalf("tree = %q", tree)
	}
}

func TestClientShowTreeFormat(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "show", "-s", "--format=%T", "head1"}, runner.Result{Stdout: "treeid\n"}, nil)
	tree, err := New(fake).ShowTreeFormat(context.Background(), "/repo", "head1")
	if err != nil {
		t.Fatalf("ShowTreeFormat: %v", err)
	}
	if tree != "treeid" {
		t.Fatalf("tree = %q", tree)
	}
}

func TestClientCommitObjectExists(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "cat-file", "-e", "sha1^{commit}"}, runner.Result{}, nil)
	exists, err := New(fake).CommitObjectExists(context.Background(), "/repo", "sha1")
	if err != nil || !exists {
		t.Fatalf("CommitObjectExists() = (%v, %v), want (true, nil)", exists, err)
	}

	// Exit status 1 (object exists but isn't a commit) and exit status 128
	// ("fatal: Not a valid object name", the shape a sha that is not any
	// object at all actually produces) are both ordinary, error-free
	// negative answers -- neither is something the caller should ever see
	// as err.
	fake.ExpectArgv([]string{"git", "cat-file", "-e", "sha1^{commit}"}, runner.Result{ExitCode: 1}, errors.New("exit status 1"))
	exists, err = New(fake).CommitObjectExists(context.Background(), "/repo", "sha1")
	if err != nil || exists {
		t.Fatalf("CommitObjectExists() = (%v, %v), want (false, nil) for exit status 1", exists, err)
	}
	fake.ExpectArgv([]string{"git", "cat-file", "-e", "sha1^{commit}"}, runner.Result{ExitCode: 128, Stderr: "fatal: Not a valid object name sha1^{commit}\n"}, errors.New("exit status 128"))
	exists, err = New(fake).CommitObjectExists(context.Background(), "/repo", "sha1")
	if err != nil || exists {
		t.Fatalf("CommitObjectExists() = (%v, %v), want (false, nil) for exit status 128 (not a valid object name)", exists, err)
	}

	// The one failure that must surface as an error rather than be read as
	// "the commit doesn't exist" (B5): task-24's runtime guard refusing to
	// start the process at all, once this method is reached through a
	// guarded runner.
	fake.ExpectArgv([]string{"git", "cat-file", "-e", "sha1^{commit}"}, runner.Result{}, runner.ErrRealProcessBlocked)
	exists, err = New(fake).CommitObjectExists(context.Background(), "/repo", "sha1")
	if !errors.Is(err, runner.ErrRealProcessBlocked) || exists {
		t.Fatalf("CommitObjectExists() = (%v, %v), want (false, ErrRealProcessBlocked)", exists, err)
	}
}

func TestClientConfigRegexpMatchesTrueOnAMatch(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "config", "--get-regexp", "^remote\\.x\\."}, runner.Result{Stdout: "remote.x.url foo\n"}, nil)
	matched, err := New(fake).ConfigRegexpMatches(context.Background(), "/repo", "^remote\\.x\\.")
	if err != nil {
		t.Fatalf("ConfigRegexpMatches: %v", err)
	}
	if !matched {
		t.Fatal("want true")
	}
}

func TestClientConfigRegexpMatchesFalseOnNoMatch(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "config", "--get-regexp", "^remote\\.x\\."}, runner.Result{ExitCode: 1}, errors.New("exit status 1"))
	matched, err := New(fake).ConfigRegexpMatches(context.Background(), "/repo", "^remote\\.x\\.")
	if err != nil {
		t.Fatalf("ConfigRegexpMatches: %v", err)
	}
	if matched {
		t.Fatal("want false")
	}
}

func TestClientConfigRegexpMatchesErrorsOnAnUnexpectedExitStatus(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "config", "--get-regexp", "^remote\\.x\\."}, runner.Result{ExitCode: 129, Stderr: "fatal: bad config"}, errors.New("exit status 129"))
	_, err := New(fake).ConfigRegexpMatches(context.Background(), "/repo", "^remote\\.x\\.")
	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("err = %v, want a *GitError", err)
	}
}
