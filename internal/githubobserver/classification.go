package githubobserver

import (
	"errors"
	"strings"
)

// IsTransientReadReason reports whether a "reason" string produced by one of
// the CI-wait read helpers (which flatten a githubGet/githubRead error into a
// plain string) came from exhausting every in-process retry for a transient
// GitHub read failure — a signal-killed or timed-out `gh` attempt, an
// HTTP 502/503/504, a secondary rate limit, or a transient network error —
// rather than an authoritative one (404, 401, an ordinary 403, or exact-head
// drift). Callers treat it like a slice deadline: resumable, not terminal.
func IsTransientReadReason(reason string) bool {
	return strings.Contains(reason, ErrTransientRetriesExhausted.Error())
}

// IsTransientReadFailure is the exported form of IsTransientReadReason for
// callers outside this package that hold an error rather than a flattened
// reason string. A verb that reports rather than merges uses it to keep a
// target pending across a provider blip instead of ending a wait with a
// verdict WB never observed.
func IsTransientReadFailure(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrTransientRetriesExhausted) || IsTransientReadReason(err.Error())
}

// IsTransientGitHubFailure includes both exhausted read retries and a write
// whose provider/transport response was lost. Both are resumable; the latter
// is deliberately not retried until authoritative state has been re-read.
func IsTransientGitHubFailure(err error) bool {
	return IsTransientReadFailure(err) || errors.Is(err, ErrTransientMutationOutcomeUnknown)
}
