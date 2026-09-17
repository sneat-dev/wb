package remotestate

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// dqCovProvider is a scripted Provider: every method returns what the test
// configured and records how often it was reached, so a test can assert both
// the projection ReadStatus returns and which store reads actually happened.
type dqCovProvider struct {
	machines    []Entry
	machinesErr error
	claims      []ClaimEntry
	claimsErr   error
	listCalls   int
	claimsCalls int
}

func (p *dqCovProvider) Publish(context.Context, Snapshot) (PublishResult, error) {
	return PublishResult{}, errors.New("dqCovProvider.Publish is not scripted")
}

func (p *dqCovProvider) List(context.Context) ([]Entry, error) {
	p.listCalls++
	return p.machines, p.machinesErr
}

func (p *dqCovProvider) Claim(context.Context, Claim, ClaimMode, string) (ClaimOutcome, error) {
	return ClaimOutcome{}, errors.New("dqCovProvider.Claim is not scripted")
}

func (p *dqCovProvider) Release(context.Context, string, string, string, bool) (ReleaseOutcome, error) {
	return ReleaseOutcome{}, errors.New("dqCovProvider.Release is not scripted")
}

func (p *dqCovProvider) Claims(context.Context) ([]ClaimEntry, error) {
	p.claimsCalls++
	return p.claims, p.claimsErr
}

// dqCovStatusProvider adds the optional batched Status capability on top of the
// scripted Provider, so a test can prove ReadStatus takes the single-read path
// and never falls back to List+Claims.
type dqCovStatusProvider struct {
	dqCovProvider
	batched     StatusSnapshot
	batchedErr  error
	statusCalls int
}

func (p *dqCovStatusProvider) Status(context.Context) (StatusSnapshot, error) {
	p.statusCalls++
	return p.batched, p.batchedErr
}

func TestReadStatusUsesOneBatchedStatusReadWhenSupported(t *testing.T) {
	want := StatusSnapshot{
		Machines: []Entry{{Snapshot: Snapshot{Login: "alice", Machine: "laptop"}}},
		Claims:   []ClaimEntry{{Claim: Claim{Task: "t-1", Login: "bob", Machine: "vm"}}},
	}
	provider := &dqCovStatusProvider{batched: want}

	got, err := ReadStatus(context.Background(), provider)
	if err != nil {
		t.Fatalf("ReadStatus = %+v, %v", got, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadStatus = %+v, want %+v", got, want)
	}
	if provider.statusCalls != 1 || provider.listCalls != 0 || provider.claimsCalls != 0 {
		t.Fatalf("calls: status=%d list=%d claims=%d, want 1/0/0",
			provider.statusCalls, provider.listCalls, provider.claimsCalls)
	}
}

func TestReadStatusFallsBackToSeparateListAndClaimsReads(t *testing.T) {
	provider := &dqCovProvider{
		machines: []Entry{{Snapshot: Snapshot{Login: "alice", Machine: "laptop"}}},
		claims:   []ClaimEntry{{Claim: Claim{Task: "t-1", Login: "bob", Machine: "vm"}}},
	}

	got, err := ReadStatus(context.Background(), provider)
	if err != nil {
		t.Fatal(err)
	}
	want := StatusSnapshot{Machines: provider.machines, Claims: provider.claims}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadStatus = %+v, want %+v", got, want)
	}
	if provider.listCalls != 1 || provider.claimsCalls != 1 {
		t.Fatalf("calls: list=%d claims=%d, want one of each", provider.listCalls, provider.claimsCalls)
	}
}

func TestReadStatusStopsBeforeClaimsWhenListFails(t *testing.T) {
	listErr := errors.New("machines store unavailable")
	provider := &dqCovProvider{
		machinesErr: listErr,
		claims:      []ClaimEntry{{Claim: Claim{Task: "t-1"}}},
	}

	got, err := ReadStatus(context.Background(), provider)
	if !errors.Is(err, listErr) {
		t.Fatalf("ReadStatus error = %v, want %v", err, listErr)
	}
	if got.Machines != nil || got.Claims != nil {
		t.Fatalf("ReadStatus returned a partial snapshot with an error: %+v", got)
	}
	if provider.claimsCalls != 0 {
		t.Fatalf("Claims was called %d times after List failed", provider.claimsCalls)
	}
}

func TestReadStatusReportsClaimsFailure(t *testing.T) {
	claimsErr := errors.New("claims store unavailable")
	provider := &dqCovProvider{
		machines:  []Entry{{Snapshot: Snapshot{Login: "alice", Machine: "laptop"}}},
		claimsErr: claimsErr,
	}

	got, err := ReadStatus(context.Background(), provider)
	if !errors.Is(err, claimsErr) {
		t.Fatalf("ReadStatus error = %v, want %v", err, claimsErr)
	}
	if got.Machines != nil || got.Claims != nil {
		t.Fatalf("ReadStatus returned a partial snapshot with an error: %+v", got)
	}
	if provider.listCalls != 1 || provider.claimsCalls != 1 {
		t.Fatalf("calls: list=%d claims=%d, want one of each", provider.listCalls, provider.claimsCalls)
	}
}

// TestReadStatusSurfacesBatchedStatusFailureWithoutFallbackReads proves the
// fallback decision is made by capability, not by outcome: a provider that
// advertises Status must have its error surfaced rather than silently re-read
// through List+Claims.
func TestReadStatusSurfacesBatchedStatusFailureWithoutFallbackReads(t *testing.T) {
	batchedErr := errors.New("batched status unavailable")
	provider := &dqCovStatusProvider{
		dqCovProvider: dqCovProvider{machines: []Entry{{Snapshot: Snapshot{Login: "alice", Machine: "laptop"}}}},
		batchedErr:    batchedErr,
	}

	got, err := ReadStatus(context.Background(), provider)
	if !errors.Is(err, batchedErr) {
		t.Fatalf("ReadStatus error = %v, want %v", err, batchedErr)
	}
	if got.Machines != nil || got.Claims != nil {
		t.Fatalf("ReadStatus returned a partial snapshot with an error: %+v", got)
	}
	if provider.listCalls != 0 || provider.claimsCalls != 0 {
		t.Fatalf("fallback reads ran despite a Status implementation: list=%d claims=%d",
			provider.listCalls, provider.claimsCalls)
	}
}
