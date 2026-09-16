package sessioncustody

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// sdCovHandoffDir returns the retained aggregate directory of the fixture.
func sdCovHandoffDir(fixture custodyFixture) string {
	return filepath.Join(fixture.store.Root, fixture.request.HandoffID)
}

func TestSdCovAcknowledgeRejectsNilContextWithoutDurableState(t *testing.T) {
	fixture := newCustodyFixture(t)
	// The nil context is exactly what this test asserts is rejected.
	//nolint:staticcheck // SA1012: passing nil is the behaviour under test.
	_, err := Acknowledge(nil, fixture.options)
	if err == nil || !strings.Contains(err.Error(), "context is required") {
		t.Fatalf("Acknowledge(nil) error = %v, want context requirement", err)
	}
	state, loadErr := fixture.store.Load(fixture.request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Receipt != nil || len(state.Events) != 0 {
		t.Fatalf("nil context mutated durable source custody: %#v", state)
	}
}

func TestSdCovAcknowledgeValidateOptionsRejections(t *testing.T) {
	fixture := newCustodyFixture(t)
	for _, tc := range []struct {
		name    string
		mutate  func(*Options)
		wantErr string
	}{
		{
			name:    "unencoable request",
			mutate:  func(o *Options) { o.Request.Branch = "" },
			wantErr: "branch must be non-empty and single-line",
		},
		{
			name:    "invalid request digest",
			mutate:  func(o *Options) { o.RequestDigest = "not-a-digest" },
			wantErr: "request digest",
		},
		{
			name:    "relative projects root",
			mutate:  func(o *Options) { o.ProjectsRoot = "relative/projects" },
			wantErr: "projects root must be one clean absolute path",
		},
		{
			name: "unclean projects root",
			mutate: func(o *Options) {
				o.ProjectsRoot = filepath.Dir(o.ProjectsRoot) + string(filepath.Separator) + "projects/../projects"
			},
			wantErr: "projects root must be one clean absolute path",
		},
		{
			name:    "source session without pid",
			mutate:  func(o *Options) { o.SourceSession.PID = 0 },
			wantErr: "source session does not match admitted predecessor identity",
		},
		{
			name:    "source session from another machine",
			mutate:  func(o *Options) { o.SourceSession.Machine = "some-other-vm" },
			wantErr: "source session does not match admitted predecessor identity",
		},
		{
			name:    "source session with a mismatched runtime",
			mutate:  func(o *Options) { o.SourceSession.Runtime = "claude" },
			wantErr: "source session does not match admitted predecessor identity",
		},
		{
			name:    "receipt for a different tmux session",
			mutate:  func(o *Options) { o.Receipt.TmuxName = "wb-session-somebody-else" },
			wantErr: sessionmove.ErrHandoffConflict.Error(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := fixture.options
			tc.mutate(&options)
			options.EnsureSourceOffer = func(worktrees.ExternalSourceOfferOptions) (worktrees.ExternalSourceOfferResult, error) {
				t.Fatal("source offer ensured before options were validated")
				return worktrees.ExternalSourceOfferResult{}, nil
			}
			_, err := Acknowledge(context.Background(), options)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Acknowledge error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
	// No rejection above may leave evidence behind.
	state, err := fixture.store.Load(fixture.request.HandoffID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Receipt != nil || len(state.Events) != 0 {
		t.Fatalf("rejected options produced durable custody: %#v", state)
	}
}

func TestSdCovAcknowledgeRejectsRequestThatDiffersFromAdmittedAggregate(t *testing.T) {
	fixture := newCustodyFixture(t)
	options := fixture.options
	options.Request.Branch = "feature/different-branch"
	options.EnsureSourceOffer = func(worktrees.ExternalSourceOfferOptions) (worktrees.ExternalSourceOfferResult, error) {
		t.Fatal("source offer ensured for a request that does not match the admitted aggregate")
		return worktrees.ExternalSourceOfferResult{}, nil
	}
	_, err := Acknowledge(context.Background(), options)
	if !errors.Is(err, sessionmove.ErrHandoffConflict) || !strings.Contains(err.Error(), "does not match pre-admitted exact request") {
		t.Fatalf("Acknowledge error = %v, want pre-admitted request conflict", err)
	}
	state, loadErr := fixture.store.Load(fixture.request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Receipt != nil || len(state.Events) != 0 {
		t.Fatalf("conflicting request produced durable custody: %#v", state)
	}
}

func TestSdCovAcknowledgeRequiresReceiptWhenNoLocalReceiptExists(t *testing.T) {
	fixture := newCustodyFixture(t)
	options := fixture.options
	options.Receipt = sessionmove.Receipt{}
	options.SealWorkLog = func(worktrees.ExternalSourceSealOptions) (worktrees.ExternalSourceSealResult, error) {
		t.Fatal("source Work Log sealed without any exact target receipt")
		return worktrees.ExternalSourceSealResult{}, nil
	}
	_, err := Acknowledge(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "target completion receipt is required") {
		t.Fatalf("Acknowledge error = %v, want missing-receipt refusal", err)
	}
	state, loadErr := fixture.store.Load(fixture.request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Receipt != nil || len(state.Events) != 0 {
		t.Fatalf("missing receipt still mutated source custody: %#v", state)
	}
}

func TestSdCovAcknowledgeRejectsSealThatDoesNotMatchReceiptLineage(t *testing.T) {
	fixture := newCustodyFixture(t)
	options := fixture.options
	options.SealWorkLog = func(worktrees.ExternalSourceSealOptions) (worktrees.ExternalSourceSealResult, error) {
		return worktrees.ExternalSourceSealResult{
			SourceWorkLogReference: "worklog:other/run/" + strings.Repeat("d", 64),
			TargetWorkLogReference: fixture.receipt.TargetWorkLogReference,
			SealedAt:               fixture.sealedAt,
		}, nil
	}
	_, err := Acknowledge(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "does not match receipt lineage") {
		t.Fatalf("Acknowledge error = %v, want seal lineage refusal", err)
	}
	state, loadErr := fixture.store.Load(fixture.request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Receipt == nil || *state.Receipt != fixture.receipt {
		t.Fatalf("receipt was not durable before seal validation: %#v", state.Receipt)
	}
	if countPhase(state, sessionmove.PhaseCompleted) != 0 {
		t.Fatalf("completed phase recorded for a rejected seal: %#v", state.Events)
	}
}

func TestSdCovAcknowledgeRunsAfterLockHookBeforeSourceOfferRepair(t *testing.T) {
	fixture := newCustodyFixture(t)
	blocked := errors.New("lock boundary blocked")
	offered := false
	options := fixture.options
	options.EnsureSourceOffer = func(worktrees.ExternalSourceOfferOptions) (worktrees.ExternalSourceOfferResult, error) {
		offered = true
		return worktrees.ExternalSourceOfferResult{}, nil
	}
	options.SealWorkLog = successfulSeal(fixture)
	options.Hooks = Hooks{AfterLock: func() error { return blocked }}
	_, err := Acknowledge(context.Background(), options)
	if !errors.Is(err, blocked) {
		t.Fatalf("Acknowledge error = %v, want injected after-lock failure", err)
	}
	if !strings.Contains(err.Error(), "after source acknowledgement lock") {
		t.Fatalf("Acknowledge error = %v, want the named lock boundary", err)
	}
	if offered {
		t.Fatal("source offer repair ran after the after-lock hook failed")
	}
}

func TestSdCovAcknowledgeFailsWhenAggregateEventsAreUnreadable(t *testing.T) {
	fixture := newCustodyFixture(t)
	// A regular file where the events directory belongs makes the durable
	// projection unreadable while leaving request admission and the lock intact.
	if err := os.WriteFile(filepath.Join(sdCovHandoffDir(fixture), "events"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := fixture.options
	options.SealWorkLog = func(worktrees.ExternalSourceSealOptions) (worktrees.ExternalSourceSealResult, error) {
		t.Fatal("source Work Log sealed without a readable source aggregate")
		return worktrees.ExternalSourceSealResult{}, nil
	}
	_, err := Acknowledge(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "events directory") {
		t.Fatalf("Acknowledge error = %v, want unreadable-events failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(sdCovHandoffDir(fixture), "receipt.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("receipt published without a readable source aggregate: %v", statErr)
	}
}

func TestSdCovAcknowledgeFailsWhenSealSeamCorruptsAggregate(t *testing.T) {
	fixture := newCustodyFixture(t)
	options := fixture.options
	options.SealWorkLog = func(worktrees.ExternalSourceSealOptions) (worktrees.ExternalSourceSealResult, error) {
		if err := os.Chmod(filepath.Join(sdCovHandoffDir(fixture), "request.json"), 0o644); err != nil {
			return worktrees.ExternalSourceSealResult{}, err
		}
		return worktrees.ExternalSourceSealResult{
			SourceWorkLogReference: fixture.request.WorkLogReference,
			TargetWorkLogReference: fixture.receipt.TargetWorkLogReference,
			SealedAt:               fixture.sealedAt,
		}, nil
	}
	_, err := Acknowledge(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "handoff request") {
		t.Fatalf("Acknowledge error = %v, want post-seal aggregate read failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(sdCovHandoffDir(fixture), "events")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("completed event appended after the source aggregate became unreadable: %v", statErr)
	}
}

func TestSdCovAcknowledgeFailsWhenCompletedEventCannotBeAppended(t *testing.T) {
	fixture := newCustodyFixture(t)
	options := fixture.options
	options.SealWorkLog = func(worktrees.ExternalSourceSealOptions) (worktrees.ExternalSourceSealResult, error) {
		// A directory holding the exact next event name is skipped by the
		// projection reader but can never be replaced by an immutable file.
		events := filepath.Join(sdCovHandoffDir(fixture), "events")
		if err := os.MkdirAll(filepath.Join(events, "00000000000000000001.json"), 0o700); err != nil {
			return worktrees.ExternalSourceSealResult{}, err
		}
		return worktrees.ExternalSourceSealResult{
			SourceWorkLogReference: fixture.request.WorkLogReference,
			TargetWorkLogReference: fixture.receipt.TargetWorkLogReference,
			SealedAt:               fixture.sealedAt,
		}, nil
	}
	_, err := Acknowledge(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "record source completed phase") {
		t.Fatalf("Acknowledge error = %v, want completed-event append failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(sdCovHandoffDir(fixture), "receipt.json")); statErr != nil {
		t.Fatalf("receipt was not durable before the completed-event append failed: %v", statErr)
	}
}

func TestSdCovAcknowledgeFailsWhenReceiptPublicationIsBlocked(t *testing.T) {
	fixture := newCustodyFixture(t)
	options := fixture.options
	options.EnsureSourceOffer = func(worktrees.ExternalSourceOfferOptions) (worktrees.ExternalSourceOfferResult, error) {
		// A directory at the immutable receipt name can never be replaced by a
		// regular 0600 file, so publication must fail rather than silently split
		// evidence.
		if err := os.MkdirAll(filepath.Join(sdCovHandoffDir(fixture), "receipt.json"), 0o700); err != nil {
			return worktrees.ExternalSourceOfferResult{}, err
		}
		return worktrees.ExternalSourceOfferResult{}, nil
	}
	options.SealWorkLog = func(worktrees.ExternalSourceSealOptions) (worktrees.ExternalSourceSealResult, error) {
		t.Fatal("source Work Log sealed after receipt publication failed")
		return worktrees.ExternalSourceSealResult{}, nil
	}
	_, err := Acknowledge(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "handoff receipt") {
		t.Fatalf("Acknowledge error = %v, want blocked receipt publication", err)
	}
	if _, statErr := os.Stat(filepath.Join(sdCovHandoffDir(fixture), "events")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("completed evidence recorded after receipt publication failed: %v", statErr)
	}
}

func TestSdCovAcknowledgeFailsWhenSuccessorAddressPublicationIsBlocked(t *testing.T) {
	fixture := newCustodyFixture(t)
	options := fixture.options
	options.Hooks.AfterReceipt = func() error {
		// The immutable courier route is corroborated while publishing the
		// successor address; a widened mode must refuse publication.
		return os.Chmod(filepath.Join(sdCovHandoffDir(fixture), "route.json"), 0o644)
	}
	options.SealWorkLog = func(worktrees.ExternalSourceSealOptions) (worktrees.ExternalSourceSealResult, error) {
		t.Fatal("source Work Log sealed without a corroborated successor address")
		return worktrees.ExternalSourceSealResult{}, nil
	}
	_, err := Acknowledge(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "route") {
		t.Fatalf("Acknowledge error = %v, want successor-address publication failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(sdCovHandoffDir(fixture), "events")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("completed evidence recorded without a corroborated successor address: %v", statErr)
	}
}

func TestSdCovAcknowledgeDefaultsToProductionSourceOfferSeam(t *testing.T) {
	fixture := newCustodyFixture(t)
	options := fixture.options
	options.EnsureSourceOffer = nil
	options.SealWorkLog = func(worktrees.ExternalSourceSealOptions) (worktrees.ExternalSourceSealResult, error) {
		t.Fatal("source Work Log sealed after the production source-offer seam refused")
		return worktrees.ExternalSourceSealResult{}, nil
	}
	_, err := Acknowledge(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "ensure immutable source offer evidence") {
		t.Fatalf("Acknowledge error = %v, want production source-offer refusal", err)
	}
}

func TestSdCovAcknowledgeDefaultsToProductionSealSeam(t *testing.T) {
	fixture := newCustodyFixture(t)
	options := fixture.options
	options.SealWorkLog = nil
	_, err := Acknowledge(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "seal source Work Log") {
		t.Fatalf("Acknowledge error = %v, want production Work Log seal refusal", err)
	}
	state, loadErr := fixture.store.Load(fixture.request.HandoffID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if state.Receipt == nil || *state.Receipt != fixture.receipt {
		t.Fatalf("receipt was not durable before the production seal seam ran: %#v", state.Receipt)
	}
	if countPhase(state, sessionmove.PhaseCompleted) != 0 {
		t.Fatalf("completed phase recorded despite seal refusal: %#v", state.Events)
	}
}

func TestSdCovAcknowledgeRejectsSourceSessionWithoutRecordedIdentity(t *testing.T) {
	fixture := newCustodyFixture(t)
	options := fixture.options
	options.SourceSession = session.Record{}
	_, err := Acknowledge(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "admitted predecessor identity") {
		t.Fatalf("Acknowledge error = %v, want predecessor identity refusal", err)
	}
}

func TestSdCovAcknowledgeFailsWhenExecutionLockCannotBeAcquired(t *testing.T) {
	fixture := newCustodyFixture(t)
	// A directory where the per-handoff execution fence belongs can never be
	// opened, so source custody must not advance or publish any evidence.
	lockPath := filepath.Join(sdCovHandoffDir(fixture), "receive.lock")
	if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.MkdirAll(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	options := fixture.options
	options.EnsureSourceOffer = func(worktrees.ExternalSourceOfferOptions) (worktrees.ExternalSourceOfferResult, error) {
		t.Fatal("source offer ensured without the acknowledgement execution fence")
		return worktrees.ExternalSourceOfferResult{}, nil
	}
	options.SealWorkLog = func(worktrees.ExternalSourceSealOptions) (worktrees.ExternalSourceSealResult, error) {
		t.Fatal("source Work Log sealed without the acknowledgement execution fence")
		return worktrees.ExternalSourceSealResult{}, nil
	}
	_, err := Acknowledge(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "execution lock") {
		t.Fatalf("Acknowledge error = %v, want execution-lock refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(sdCovHandoffDir(fixture), "receipt.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("receipt published without the acknowledgement fence: %v", statErr)
	}
}
