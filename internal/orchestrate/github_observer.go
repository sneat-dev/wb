package orchestrate

import (
	"context"
	"errors"
	"strings"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

func githubRead(ctx context.Context, worktree string, args ...string) (string, error) {
	output, err := githubobserver.Read(ctx, worktree, args...)
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func githubGet(ctx context.Context, worktree, repository, target, head, endpoint string) ([]byte, error) {
	response, err := githubobserver.Get(ctx, githubobserver.GetRequest{
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
	return githubobserver.Execute(ctx, worktree, args...)
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
