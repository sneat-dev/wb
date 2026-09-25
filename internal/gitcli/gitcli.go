// Package gitcli is task-8's real git-operation adapter: it builds argv,
// runs it through internal/runner, and parses the output, so a consumer
// package's own narrow ports.go (as internal/streams/ports.go,
// internal/streamsync/ports.go and internal/locallink/ports.go already
// declare) can be backed by real git without depending on os/exec at all.
//
// wb runs the `git` executable and imports no Go git library (the root
// README's "Why WB runs the `git` CLI" section); this package is where that
// choice lives.
//
// This is the plan's PR-1 adapter skeleton: a handful of read-only
// operations, chosen because they are easy to contract-test against both
// real git and a fake (internal/gitcli/gitclitest), proving the pattern
// before any consumer package migrates onto it. It is not meant to be
// imported by a consumer directly; a consumer's own ports.go declares only
// the operations it needs, and its adapter can be built the same way this
// one is, or can embed/delegate to a Client where the operations overlap.
package gitcli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/internal/runner"
)

// Git is the narrow git-operation port this package's Client implements.
type Git interface {
	// CurrentBranch reports the checked-out branch of a worktree.
	CurrentBranch(ctx context.Context, dir string) (string, error)
	// RevParse resolves rev to a commit SHA.
	RevParse(ctx context.Context, dir, rev string) (string, error)
	// IsAncestor reports whether ancestor is reachable from descendant.
	IsAncestor(ctx context.Context, dir, ancestor, descendant string) (bool, error)
	// Fetch refreshes remote in dir.
	Fetch(ctx context.Context, dir, remote string) error
}

// Client implements Git over a runner.Runner. Its unit tests run against
// runnertest.Fake; its contract tests (internal/gitcli/contract_test.go,
// //go:build e2e) run the same cases against real git.
type Client struct {
	Runner runner.Runner
}

var _ Git = Client{}

// New returns a Client backed by r.
func New(r runner.Runner) Client {
	return Client{Runner: r}
}

// GitError wraps a failed git invocation with the argv that failed and the
// stderr git printed, so a caller's error message names the actual command
// rather than a generic "exit status 1".
type GitError struct {
	Argv   []string
	Stderr string
	Err    error
}

func (e *GitError) Error() string {
	stderr := strings.TrimSpace(e.Stderr)
	if stderr == "" {
		return fmt.Sprintf("git %s: %v", strings.Join(e.Argv, " "), e.Err)
	}
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.Argv, " "), e.Err, stderr)
}

func (e *GitError) Unwrap() error { return e.Err }

// run executes git with args in dir and returns trimmed stdout, wrapping any
// failure in a *GitError that names the argv and carries git's stderr.
func (c Client) run(ctx context.Context, dir string, args ...string) (string, error) {
	result, err := c.Runner.Run(ctx, dir, "git", args...)
	if err != nil {
		return "", &GitError{Argv: args, Stderr: result.Stderr, Err: err}
	}
	return strings.TrimSpace(result.Stdout), nil
}

// CurrentBranch reports the checked-out branch of dir.
func (c Client) CurrentBranch(ctx context.Context, dir string) (string, error) {
	return c.run(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// RevParse resolves rev to a commit SHA in dir.
func (c Client) RevParse(ctx context.Context, dir, rev string) (string, error) {
	return c.run(ctx, dir, "rev-parse", rev)
}

// errIsAncestorAmbiguous is returned when `git merge-base --is-ancestor`
// exits neither 0 (is an ancestor) nor 1 (is not), which means it could not
// answer the question at all -- an unknown revision, for example -- rather
// than answering "no".
var errIsAncestorAmbiguous = errors.New("gitcli: git merge-base --is-ancestor gave neither a yes nor a no answer")

// IsAncestor reports whether ancestor is reachable from descendant in dir.
func (c Client) IsAncestor(ctx context.Context, dir, ancestor, descendant string) (bool, error) {
	result, err := c.Runner.Run(ctx, dir, "git", "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	if result.ExitCode == 1 {
		return false, nil
	}
	argv := []string{"merge-base", "--is-ancestor", ancestor, descendant}
	return false, &GitError{Argv: argv, Stderr: result.Stderr, Err: errors.Join(errIsAncestorAmbiguous, err)}
}

// Fetch refreshes remote in dir.
func (c Client) Fetch(ctx context.Context, dir, remote string) error {
	_, err := c.run(ctx, dir, "fetch", "--quiet", remote)
	return err
}

// runVoid is c.run for a caller that only cares whether the call failed.
func (c Client) runVoid(ctx context.Context, dir string, args ...string) error {
	_, err := c.run(ctx, dir, args...)
	return err
}

// The methods below back internal/orchestrate's Git port
// (spec/plans/coverage-to-100 task-17): each one's argv reproduces, byte for
// byte, the exec.CommandContext call it replaces, so migrating a call site
// onto it changes nothing observable. Every one of orchestrate's git values
// that a caller actually reads (a SHA, a branch name, a tree id) comes from a
// git subcommand that only ever writes that value to stdout on success, so
// c.run's stdout-only success return already matches what the former
// CombinedOutput()-based call returned; only the failure path's formatting
// needed the byte-identical proof (gitcli_test.go).

// WorktreeRemoveForce removes worktree from dir's repository, discarding any
// modification it holds.
func (c Client) WorktreeRemoveForce(ctx context.Context, dir, worktree string) error {
	return c.runVoid(ctx, dir, "worktree", "remove", "--force", worktree)
}

// WorktreeAddDetached adds a new worktree at path, checked out detached at
// revision.
func (c Client) WorktreeAddDetached(ctx context.Context, dir, path, revision string) error {
	return c.runVoid(ctx, dir, "worktree", "add", "--detach", path, revision)
}

// CherryPickNoCommit replays shas onto the current commit without writing a
// commit, leaving the result staged.
func (c Client) CherryPickNoCommit(ctx context.Context, dir string, shas ...string) error {
	args := append([]string{"cherry-pick", "--no-commit"}, shas...)
	return c.runVoid(ctx, dir, args...)
}

// CommitNoVerify writes a commit from the current staged state without
// running hooks.
func (c Client) CommitNoVerify(ctx context.Context, dir, message string) error {
	return c.runVoid(ctx, dir, "commit", "--no-verify", "-m", message)
}

// CherryPick replays sha onto the current commit and commits it.
func (c Client) CherryPick(ctx context.Context, dir, sha string) error {
	return c.runVoid(ctx, dir, "cherry-pick", sha)
}

// PushForceWithLeaseHead publishes the current commit (HEAD) to ref under a
// lease on leaseSHA -- the exact push a landing's branch rewrite uses to
// replace ref only if it still matches what was read before the rewrite
// started.
func (c Client) PushForceWithLeaseHead(ctx context.Context, dir, ref, leaseSHA string) error {
	return c.runVoid(ctx, dir, "push",
		"--force-with-lease=refs/heads/"+ref+":"+leaseSHA, "origin", "HEAD:refs/heads/"+ref)
}

// FetchRefs refreshes remote in dir for exactly refs, without --quiet.
func (c Client) FetchRefs(ctx context.Context, dir, remote string, refs ...string) error {
	args := append([]string{"fetch", remote}, refs...)
	return c.runVoid(ctx, dir, args...)
}

// RevListReverseRange lists the commits in the exclusive range from..to,
// oldest first.
func (c Client) RevListReverseRange(ctx context.Context, dir, from, to string) (string, error) {
	return c.run(ctx, dir, "rev-list", "--reverse", from+".."+to)
}

// RemotePushURL resolves the push URL of remote in dir.
func (c Client) RemotePushURL(ctx context.Context, dir, remote string) (string, error) {
	return c.run(ctx, dir, "remote", "get-url", "--push", remote)
}

// StatusPorcelain reports dir's porcelain-v1 status.
func (c Client) StatusPorcelain(ctx context.Context, dir string) (string, error) {
	return c.run(ctx, dir, "status", "--porcelain")
}

// BranchShowCurrent reports dir's checked-out branch, empty when HEAD is
// detached -- unlike CurrentBranch, which resolves detached HEAD to "HEAD"
// itself; the two are deliberately different git invocations for two
// different questions, so this is its own method rather than a reuse of
// CurrentBranch's argv.
func (c Client) BranchShowCurrent(ctx context.Context, dir string) (string, error) {
	return c.run(ctx, dir, "branch", "--show-current")
}

// MergeBaseIsAncestorStrict reports an error whenever `git merge-base
// --is-ancestor ancestor descendant` exits non-zero, including the ordinary
// "not an ancestor" exit status 1 -- unlike IsAncestor, which turns that
// specific exit status into (false, nil). A caller that only wants "did this
// fail" (never inspecting a bool result) needs that distinction preserved,
// so this is a second, deliberately stricter method rather than a reuse of
// IsAncestor.
func (c Client) MergeBaseIsAncestorStrict(ctx context.Context, dir, ancestor, descendant string) error {
	return c.runVoid(ctx, dir, "merge-base", "--is-ancestor", ancestor, descendant)
}

// BranchSetUpstreamTo points branch's upstream at upstream.
func (c Client) BranchSetUpstreamTo(ctx context.Context, dir, upstream, branch string) error {
	return c.runVoid(ctx, dir, "branch", "--set-upstream-to="+upstream, branch)
}

// MergeTreeWriteTree performs a real-merge write-tree of a and b without
// touching the working tree or any ref, and returns the resulting tree id.
func (c Client) MergeTreeWriteTree(ctx context.Context, dir, a, b string) (string, error) {
	return c.run(ctx, dir, "merge-tree", "--write-tree", a, b)
}

// ShowTreeFormat resolves commit's tree id.
func (c Client) ShowTreeFormat(ctx context.Context, dir, commit string) (string, error) {
	return c.run(ctx, dir, "show", "-s", "--format=%T", commit)
}

// CommitObjectExists reports whether sha's commit object already exists in
// dir's object database. Unlike every other method here, it collapses any
// failure (missing object, an unreadable repository, a cancelled context)
// into false rather than returning an error: the call this replaces
// (internal/orchestrate's commitExistsLocally) only ever used the call's
// success as its answer and had no error return of its own to preserve.
func (c Client) CommitObjectExists(ctx context.Context, dir, sha string) bool {
	_, err := c.run(ctx, dir, "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// ConfigRegexpMatches reports whether `git config --get-regexp pattern` in
// dir finds at least one entry: true with a zero exit status, false with
// exit status 1 and empty output (git's own "no matches" shape), and an
// error for anything else.
func (c Client) ConfigRegexpMatches(ctx context.Context, dir, pattern string) (bool, error) {
	result, err := c.Runner.Run(ctx, dir, "git", "config", "--get-regexp", pattern)
	if err == nil {
		return true, nil
	}
	if result.ExitCode == 1 && strings.TrimSpace(result.Stdout+result.Stderr) == "" {
		return false, nil
	}
	args := []string{"config", "--get-regexp", pattern}
	return false, &GitError{Argv: args, Stderr: result.Stderr, Err: err}
}
