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
