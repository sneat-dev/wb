package remoterun

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

// A store that is not syncing must be reported as a WARNING naming the local
// copy, distinct from an ordinary one-off skipped claim (sneat-dev/wb#899),
// on claim and on release; an ordinary provider failure gets no warning.
func TestAutoClaimAndReleaseWarnWhenRemoteStoreIsNotSyncing(t *testing.T) {
	t.Parallel()
	syncErr := &remotestate.SyncError{Store: "/projects/team/state", Remedy: "fix it", Err: errObservedProvider}
	p := &observedProvider{
		claim: func(context.Context, remotestate.Claim, remotestate.ClaimMode, string) (remotestate.ClaimOutcome, error) {
			return remotestate.ClaimOutcome{}, syncErr
		},
		release: func(context.Context, string, string, string, bool) (remotestate.ReleaseOutcome, error) {
			return remotestate.ReleaseOutcome{}, syncErr
		},
	}
	s := providerService(t, p)
	const warning = "WARNING: remote store sync failing for /projects/team/state"

	var claimOut bytes.Buffer
	if got := s.AutoClaim("explicit", "task", time.Hour, &claimOut); got.Outcome != "skipped" || !strings.Contains(got.Detail, "fix it") {
		t.Fatalf("claim outcome = %+v", got)
	}
	if !strings.Contains(claimOut.String(), warning) {
		t.Fatalf("claim output = %q, want the sync warning", claimOut.String())
	}

	var releaseOut bytes.Buffer
	if got := s.AutoRelease("explicit", "task", &releaseOut); !got.Leaked() {
		t.Fatalf("release outcome = %+v, want a leaked claim", got)
	}
	if !strings.Contains(releaseOut.String(), warning) {
		t.Fatalf("release output = %q, want the sync warning", releaseOut.String())
	}

	p.claim = func(context.Context, remotestate.Claim, remotestate.ClaimMode, string) (remotestate.ClaimOutcome, error) {
		return remotestate.ClaimOutcome{}, errObservedProvider
	}
	var plainOut bytes.Buffer
	s.AutoClaim("explicit", "task", time.Hour, &plainOut)
	if strings.Contains(plainOut.String(), "WARNING") {
		t.Fatalf("plain provider failure output = %q, want no sync warning", plainOut.String())
	}
}

// The stale-takeover path warns too: both its store read and its takeover
// claim can hit a store that is not syncing.
func TestAutoTakeOverWarnsWhenRemoteStoreIsNotSyncing(t *testing.T) {
	t.Parallel()
	syncErr := &remotestate.SyncError{Store: "/projects/team/state", Remedy: "fix it", Err: errObservedProvider}
	held := remotestate.Claim{Task: "task", Login: "other", Machine: "box"}
	calls := 0
	p := &observedProvider{
		claim: func(context.Context, remotestate.Claim, remotestate.ClaimMode, string) (remotestate.ClaimOutcome, error) {
			calls++
			if calls%2 == 1 {
				return remotestate.ClaimOutcome{Kind: remotestate.ClaimHeld, Current: held}, nil
			}
			return remotestate.ClaimOutcome{}, syncErr
		},
		list: func(context.Context) ([]remotestate.Entry, error) { return nil, syncErr },
	}
	s := providerService(t, p)

	var listOut bytes.Buffer
	if got := s.AutoClaim("explicit", "task", time.Hour, &listOut); got.Outcome != "skipped" {
		t.Fatalf("outcome = %+v", got)
	}
	if !strings.Contains(listOut.String(), "WARNING: remote store sync failing") {
		t.Fatalf("store-read output = %q, want the sync warning", listOut.String())
	}

	// No snapshot for the holder reads as stale, so the takeover claim runs.
	p.list = func(context.Context) ([]remotestate.Entry, error) { return nil, nil }
	var takeOut bytes.Buffer
	if got := s.AutoClaim("explicit", "task", time.Hour, &takeOut); got.Outcome != "skipped" {
		t.Fatalf("outcome = %+v", got)
	}
	if !strings.Contains(takeOut.String(), "WARNING: remote store sync failing") {
		t.Fatalf("takeover output = %q, want the sync warning", takeOut.String())
	}
}
