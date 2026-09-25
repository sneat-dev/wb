package orchestrate

import (
	"context"
	"errors"
	"strings"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

// ghObserver is a package-private test seam over the githubobserver.Observer
// this package reads/writes gh through (directly here, and via the
// githubobserver.GetPages/Read package-level calls scattered across this
// package's other files, which all default to the same
// githubobserver.Default() this var starts from). Once those started
// routing gh through the task-24 guarded runner seam, this package's tests
// could no longer answer them with a fake `gh` on PATH (that real
// subprocess start is refused under go test). Tests reassign it to one
// built with githubobserver.NewTestObserver and restore it with
// t.Cleanup; production callers never touch it.
var ghObserver = githubobserver.Default()

// SetGitHubObserverForTest overrides this package's githubobserver.Observer
// for the duration of a test, and returns a restore func the caller must
// invoke (typically via t.Cleanup) to put it back. It exists for OTHER
// packages' tests that exercise code built on this package (internal/deps'
// bump campaigns, ...): ghObserver above is unexported, so they cannot reach
// it directly, and a real gh subprocess is refused by the task-24 guarded
// runner seam under go test. Production code never calls this. The caller
// is responsible for not running two tests that use it in parallel with
// each other.
func SetGitHubObserverForTest(o *githubobserver.Observer) (restore func()) {
	original := ghObserver
	ghObserver = o
	return func() { ghObserver = original }
}

func githubRead(ctx context.Context, worktree string, args ...string) (string, error) {
	output, err := ghObserver.Read(ctx, worktree, args...)
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func githubGet(ctx context.Context, worktree, repository, target, head, endpoint string) ([]byte, error) {
	response, err := ghObserver.Get(ctx, githubobserver.GetRequest{
		Dir:         worktree,
		Repository:  strings.TrimSpace(repository),
		Target:      strings.TrimSpace(target),
		Head:        strings.TrimSpace(head),
		Endpoint:    endpoint,
		FreshWindow: 0,
	})
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

func githubExecute(ctx context.Context, worktree string, args ...string) githubobserver.CommandResponse {
	return ghObserver.Execute(ctx, worktree, args...)
}

// isTransientReadReason reports whether a "reason" string produced by one of
// the CI-wait read helpers (which flatten a githubGet/githubRead error into a
// plain string) came from exhausting every in-process retry for a transient
// GitHub read failure — a signal-killed or timed-out `gh` attempt, an
// HTTP 502/503/504, a secondary rate limit, or a transient network error —
// rather than an authoritative one (404, 401, an ordinary 403, or exact-head
// drift). Callers treat it like a slice deadline: resumable, not terminal.
func isTransientReadReason(reason string) bool {
	return strings.Contains(reason, githubobserver.ErrTransientRetriesExhausted.Error())
}

// IsTransientReadFailure is the exported form of isTransientReadReason for
// callers outside this package that hold an error rather than a flattened
// reason string. A verb that reports rather than merges uses it to keep a
// target pending across a provider blip instead of ending a wait with a
// verdict WB never observed.
func IsTransientReadFailure(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, githubobserver.ErrTransientRetriesExhausted) || isTransientReadReason(err.Error())
}

// IsTransientGitHubFailure includes both exhausted read retries and a write
// whose provider/transport response was lost. Both are resumable; the latter
// is deliberately not retried until authoritative state has been re-read.
func IsTransientGitHubFailure(err error) bool {
	return IsTransientReadFailure(err) || errors.Is(err, githubobserver.ErrTransientMutationOutcomeUnknown)
}
