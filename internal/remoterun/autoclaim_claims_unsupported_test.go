package remoterun

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

type claimsUnsupportedProvider struct{ *observedProvider }

func (*claimsUnsupportedProvider) SupportsClaims() bool { return false }

// A provider that declares no claim store never acquired a claim, so both
// automatic paths must report an informational skip naming the capability gap,
// not the "FAILED ... claim was not released" leak line (sneat-dev/wb#626).
func TestAutoClaimAndReleaseSkipProviderWithoutClaimsSupport(t *testing.T) {
	t.Parallel()
	p := &claimsUnsupportedProvider{observedProvider: &observedProvider{
		claim: func(context.Context, remotestate.Claim, remotestate.ClaimMode, string) (remotestate.ClaimOutcome, error) {
			t.Error("claim must not be attempted without claim support")
			return remotestate.ClaimOutcome{}, nil
		},
		release: func(context.Context, string, string, string, bool) (remotestate.ReleaseOutcome, error) {
			t.Error("release must not be attempted without claim support")
			return remotestate.ReleaseOutcome{}, nil
		},
	}}
	s := providerService(t, p)

	var claimOut bytes.Buffer
	claim := s.AutoClaim("explicit", "task", time.Hour, &claimOut)
	if claim.Outcome != "skipped" || !strings.Contains(claim.Detail, "sneat-dev/wb#626") {
		t.Fatalf("claim outcome = %+v", claim)
	}
	if !strings.Contains(claimOut.String(), "remote claim skipped:") || strings.Contains(claimOut.String(), "FAILED") || strings.Contains(claimOut.String(), "WARNING") {
		t.Fatalf("claim output = %q", claimOut.String())
	}

	var releaseOut bytes.Buffer
	release := s.AutoRelease("explicit", "task", &releaseOut)
	if release.Outcome != "skipped" || !release.Unsupported || release.Leaked() || !strings.Contains(release.Detail, "sneat-dev/wb#626") {
		t.Fatalf("release outcome = %+v", release)
	}
	if !strings.Contains(releaseOut.String(), "remote claim release skipped:") || strings.Contains(releaseOut.String(), "FAILED") || strings.Contains(releaseOut.String(), "WARNING") {
		t.Fatalf("release output = %q", releaseOut.String())
	}
}

// The same classification applies to a claim attempt that reports the shared
// capability error without declaring the capability: it is a skip, not a
// store failure, and it must not print a sync warning.
func TestAutoClaimClassifiesUnsupportedClaimsAsSkip(t *testing.T) {
	t.Parallel()
	p := &observedProvider{
		claim: func(context.Context, remotestate.Claim, remotestate.ClaimMode, string) (remotestate.ClaimOutcome, error) {
			return remotestate.ClaimOutcome{}, remotestate.ErrClaimsUnsupported
		},
	}
	s := providerService(t, p)

	var out bytes.Buffer
	got := s.AutoClaim("explicit", "task", time.Hour, &out)
	if got.Outcome != "skipped" || !strings.Contains(got.Detail, "sneat-dev/wb#626") {
		t.Fatalf("claim outcome = %+v", got)
	}
	if strings.Contains(out.String(), "WARNING") || strings.Contains(out.String(), "FAILED") {
		t.Fatalf("claim output = %q", out.String())
	}
}

// A provider that claims support but still refuses Release with the shared
// capability error is classified the same way: no claim exists, so nothing can
// leak. Any other release error keeps the wb#321 leak classification.
func TestAutoReleaseClassifiesUnsupportedClaimsSeparatelyFromStoreFailure(t *testing.T) {
	t.Parallel()
	p := &observedProvider{
		release: func(context.Context, string, string, string, bool) (remotestate.ReleaseOutcome, error) {
			return remotestate.ReleaseOutcome{}, remotestate.ErrClaimsUnsupported
		},
	}
	s := providerService(t, p)

	var out bytes.Buffer
	if got := s.AutoRelease("explicit", "task", &out); got.Outcome != "skipped" || !got.Unsupported || got.Leaked() || !strings.Contains(got.Detail, "sneat-dev/wb#626") {
		t.Fatalf("unsupported outcome = %+v", got)
	}
	if strings.Contains(out.String(), "FAILED") || strings.Contains(out.String(), "WARNING") {
		t.Fatalf("unsupported output = %q", out.String())
	}

	p.release = func(context.Context, string, string, string, bool) (remotestate.ReleaseOutcome, error) {
		return remotestate.ReleaseOutcome{}, errObservedProvider
	}
	out.Reset()
	if got := s.AutoRelease("explicit", "task", &out); !got.Leaked() || got.Outcome != "failed" {
		t.Fatalf("store failure outcome = %+v, want a leaked claim", got)
	}
	if !strings.Contains(out.String(), "remote claim release FAILED:") {
		t.Fatalf("store failure output = %q, want the leak line", out.String())
	}
}
