package githubobserver

import (
	"context"
	"errors"
	"testing"
)

// TestIsTransientCommandFailure_NoError drives the fast-path branch: a
// response with no error is never transient.
func TestIsTransientCommandFailure_NoError(t *testing.T) {
	t.Parallel()

	if IsTransientCommandFailure(context.Background(), CommandResponse{}) {
		t.Error("IsTransientCommandFailure() = true for a nil-error response, want false")
	}
}

// TestIsTransientCommandFailure_CallerContextAlreadyDone drives the branch
// that treats an already-cancelled caller context as authoritative: the
// mutation may have landed before the client noticed cancellation, so it is
// reported transient rather than silently retried.
func TestIsTransientCommandFailure_CallerContextAlreadyDone(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	response := CommandResponse{Err: errors.New("boom")}
	if !IsTransientCommandFailure(ctx, response) {
		t.Error("IsTransientCommandFailure() = false for a cancelled caller context, want true")
	}
}

// TestIsTransientCommandFailure_HTTP500Marker drives the branch that
// recognizes an "http 500"/"status code 500" marker in the failure message
// without deferring to retryableReadFailure's narrower marker set.
func TestIsTransientCommandFailure_HTTP500Marker(t *testing.T) {
	t.Parallel()

	response := CommandResponse{
		Err:    errors.New("gh: request failed"),
		Stderr: []byte("gh: HTTP 500: Internal Server Error"),
	}
	if !IsTransientCommandFailure(context.Background(), response) {
		t.Error("IsTransientCommandFailure() = false for an HTTP 500 message, want true")
	}
}

// TestIsTransientCommandFailure_DelegatesToRetryableReadFailure drives the
// final fallback: a failure that names neither a 500 nor a cancelled
// context is classified by the shared retryableReadFailure logic, and an
// ordinary non-matching error is not transient.
func TestIsTransientCommandFailure_DelegatesToRetryableReadFailure(t *testing.T) {
	t.Parallel()

	response := CommandResponse{Err: errors.New("not found")}
	if IsTransientCommandFailure(context.Background(), response) {
		t.Error("IsTransientCommandFailure() = true for a plain not-found error, want false")
	}
}
