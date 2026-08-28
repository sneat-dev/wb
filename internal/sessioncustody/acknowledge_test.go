package sessioncustody

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestAcknowledgeRepairsEveryReceiptGatedCrashBoundary(t *testing.T) {
	crash := errors.New("simulated crash")
	for _, stage := range []string{"receipt", "address", "seal", "completed"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newCustodyFixture(t)
			ensureCalls := 0
			sealCalls := 0
			options := fixture.options
			options.EnsureSourceOffer = func(got worktrees.ExternalSourceOfferOptions) (worktrees.ExternalSourceOfferResult, error) {
				ensureCalls++
				state, err := got.Store.LoadUnderLock(got.ExecutionLock, got.Request.HandoffID, got.RequestDigest)
				if err != nil {
					t.Fatal(err)
				}
				if ensureCalls == 1 && state.Receipt != nil {
					t.Fatal("first source-offer repair unexpectedly followed receipt publication")
				}
				if ensureCalls == 2 && (state.Receipt == nil || *state.Receipt != fixture.receipt) {
					t.Fatalf("replay source-offer repair did not see exact local receipt: %#v", state.Receipt)
				}
				return worktrees.ExternalSourceOfferResult{Replayed: ensureCalls > 1}, nil
			}
			options.SealWorkLog = func(got worktrees.ExternalSourceSealOptions) (worktrees.ExternalSourceSealResult, error) {
				sealCalls++
				if got.Request != fixture.request || got.RequestDigest != fixture.digest || got.Receipt != fixture.receipt || got.SourceSession != fixture.source {
					t.Fatalf("SealWorkLog options = %#v", got)
				}
				return worktrees.ExternalSourceSealResult{
					SourceWorkLogReference: fixture.request.WorkLogReference,
					TargetWorkLogReference: fixture.receipt.TargetWorkLogReference,
					SealedAt:               fixture.sealedAt,
					Replayed:               sealCalls > 1,
				}, nil
			}
			options.Hooks = Hooks{
				AfterReceipt: func() error {
					if stage == "receipt" {
						return crash
					}
					return nil
				},
				AfterAddress: func() error {
					if stage == "address" {
						return crash
					}
					return nil
				},
				AfterSeal: func() error {
					if stage == "seal" {
						return crash
					}
					return nil
				},
				AfterCompleted: func() error {
					if stage == "completed" {
						return crash
					}
					return nil
				},
			}
			if _, err := Acknowledge(context.Background(), options); !errors.Is(err, crash) {
				t.Fatalf("Acknowledge first error = %v", err)
			}
			state, err := fixture.store.Load(fixture.request.HandoffID)
			if err != nil {
				t.Fatal(err)
			}
			if state.Receipt == nil || *state.Receipt != fixture.receipt {
				t.Fatalf("receipt not durable at %s boundary: %#v", stage, state.Receipt)
			}
			_, addressErr := fixture.store.LoadSuccessorAddress(fixture.request.SuccessorWBSessionID)
			wantAddress := stage != "receipt"
			if wantAddress == (addressErr != nil) {
				t.Fatalf("address error at %s boundary = %v, wantAddress=%t", stage, addressErr, wantAddress)
			}
			completedBefore := countPhase(state, sessionmove.PhaseCompleted)
			wantCompleted := 0
			if stage == "completed" {
				wantCompleted = 1
			}
			if completedBefore != wantCompleted {
				t.Fatalf("completed events at %s boundary = %d, want %d", stage, completedBefore, wantCompleted)
			}

			options.Hooks = Hooks{}
			options.Receipt = sessionmove.Receipt{} // local exact receipt must drive repair; no courier is involved.
			result, err := Acknowledge(context.Background(), options)
			if err != nil {
				t.Fatalf("Acknowledge repair: %v", err)
			}
			if !result.Replay || result.Receipt != fixture.receipt || result.Address.SuccessorWBSessionID != fixture.request.SuccessorWBSessionID ||
				result.WorkLog.TargetWorkLogReference != fixture.receipt.TargetWorkLogReference {
				t.Fatalf("repair result = %#v", result)
			}
			state, err = fixture.store.Load(fixture.request.HandoffID)
			if err != nil {
				t.Fatal(err)
			}
			if got := countPhase(state, sessionmove.PhaseCompleted); got != 1 {
				t.Fatalf("completed events after repair = %d, want exactly one", got)
			}
			if ensureCalls != 2 {
				t.Fatalf("source-offer repair calls = %d, want fresh plus local-receipt replay", ensureCalls)
			}
		})
	}
}

func TestAcknowledgeRequiresPreAdmittedExactSourceAggregate(t *testing.T) {
	fixture := newCustodyFixture(t)
	emptyStore := sessionmove.NewStore(filepath.Join(t.TempDir(), sessionmove.DirName))
	options := fixture.options
	options.Store = emptyStore
	options.SealWorkLog = func(worktrees.ExternalSourceSealOptions) (worktrees.ExternalSourceSealResult, error) {
		t.Fatal("SealWorkLog called without an admitted aggregate")
		return worktrees.ExternalSourceSealResult{}, nil
	}
	if _, err := Acknowledge(context.Background(), options); err == nil || !strings.Contains(err.Error(), "admitted") {
		t.Fatalf("Acknowledge error = %v", err)
	}
}

func TestAcknowledgeRefusesPostLockPathSwapWithoutMutatingDecoy(t *testing.T) {
	for _, swapRoot := range []bool{false, true} {
		name := "handoff"
		if swapRoot {
			name = "root"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newCustodyFixture(t)
			retained := ""
			options := fixture.options
			options.Hooks.AfterLock = func() error {
				if swapRoot {
					retained = fixture.store.Root + ".retained"
					if err := os.Rename(fixture.store.Root, retained); err != nil {
						return err
					}
					if err := os.MkdirAll(filepath.Join(fixture.store.Root, fixture.request.HandoffID), 0o700); err != nil {
						return err
					}
				} else {
					handoff := filepath.Join(fixture.store.Root, fixture.request.HandoffID)
					retained = handoff + ".retained"
					if err := os.Rename(handoff, retained); err != nil {
						return err
					}
					if err := os.Mkdir(handoff, 0o700); err != nil {
						return err
					}
				}
				decoy := filepath.Join(fixture.store.Root, fixture.request.HandoffID, "request.json")
				canonical, err := sessionmove.EncodeRequest(fixture.request)
				if err != nil {
					return err
				}
				return os.WriteFile(decoy, canonical, 0o600)
			}
			if _, err := Acknowledge(context.Background(), options); err == nil || !strings.Contains(err.Error(), "exact admitted") {
				t.Fatalf("Acknowledge path-swap error = %v", err)
			}
			decoyDirectory := filepath.Join(fixture.store.Root, fixture.request.HandoffID)
			entries, err := os.ReadDir(decoyDirectory)
			if err != nil || len(entries) != 1 || entries[0].Name() != "request.json" {
				t.Fatalf("decoy mutated after refused acknowledgement: entries=%v err=%v", entries, err)
			}
			retainedHandoff := retained
			if swapRoot {
				retainedHandoff = filepath.Join(retained, fixture.request.HandoffID)
			}
			for _, name := range []string{"receipt.json", "events"} {
				if _, err := os.Stat(filepath.Join(retainedHandoff, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("retained source aggregate gained %s: %v", name, err)
				}
			}
		})
	}
}

func TestAcknowledgeRepairsSourceOfferBeforeReceiptProcessing(t *testing.T) {
	fixture := newCustodyFixture(t)
	ensureCalls := 0
	options := fixture.options
	options.EnsureSourceOffer = func(got worktrees.ExternalSourceOfferOptions) (worktrees.ExternalSourceOfferResult, error) {
		ensureCalls++
		if got.Store != fixture.store || got.ExecutionLock == nil || got.ProjectsRoot != options.ProjectsRoot ||
			got.Request != fixture.request || got.RequestDigest != fixture.digest || got.SourceSession != fixture.source {
			t.Fatalf("EnsureSourceOffer options = %#v", got)
		}
		state, err := got.Store.LoadUnderLock(got.ExecutionLock, got.Request.HandoffID, got.RequestDigest)
		if err != nil {
			t.Fatal(err)
		}
		if state.Receipt != nil {
			t.Fatal("source offer repair ran after receipt publication")
		}
		return worktrees.ExternalSourceOfferResult{}, nil
	}
	options.SealWorkLog = successfulSeal(fixture)
	if _, err := Acknowledge(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if ensureCalls != 1 {
		t.Fatalf("EnsureSourceOffer calls = %d, want 1", ensureCalls)
	}
}

func TestAcknowledgeRefusesReceiptWhenSourceOfferRepairFails(t *testing.T) {
	fixture := newCustodyFixture(t)
	blocked := errors.New("source offer repair blocked")
	options := fixture.options
	options.EnsureSourceOffer = func(worktrees.ExternalSourceOfferOptions) (worktrees.ExternalSourceOfferResult, error) {
		return worktrees.ExternalSourceOfferResult{}, blocked
	}
	options.SealWorkLog = func(worktrees.ExternalSourceSealOptions) (worktrees.ExternalSourceSealResult, error) {
		t.Fatal("SealWorkLog called after source offer repair failure")
		return worktrees.ExternalSourceSealResult{}, nil
	}
	if _, err := Acknowledge(context.Background(), options); !errors.Is(err, blocked) {
		t.Fatalf("Acknowledge error = %v", err)
	}
	state, err := fixture.store.Load(fixture.request.HandoffID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Receipt != nil {
		t.Fatalf("receipt persisted after source offer repair failure: %#v", state.Receipt)
	}
}

func TestAcknowledgeRefusesReceiptConflictBeforeSourceSeal(t *testing.T) {
	fixture := newCustodyFixture(t)
	firstOptions := fixture.options
	firstOptions.SealWorkLog = successfulSeal(fixture)
	if _, err := Acknowledge(context.Background(), firstOptions); err != nil {
		t.Fatal(err)
	}
	conflict := fixture.receipt
	conflict.NativeHarnessID = "different-native-session"
	options := fixture.options
	options.Receipt = conflict
	options.SealWorkLog = func(worktrees.ExternalSourceSealOptions) (worktrees.ExternalSourceSealResult, error) {
		t.Fatal("SealWorkLog called with a conflicting local receipt")
		return worktrees.ExternalSourceSealResult{}, nil
	}
	if _, err := Acknowledge(context.Background(), options); !errors.Is(err, sessionmove.ErrHandoffConflict) {
		t.Fatalf("Acknowledge conflict error = %v", err)
	}
}

type custodyFixture struct {
	store    sessionmove.Store
	request  sessionmove.Request
	digest   sessionmove.Digest
	receipt  sessionmove.Receipt
	source   session.Record
	sealedAt time.Time
	options  Options
}

func newCustodyFixture(t *testing.T) custodyFixture {
	t.Helper()
	const sourceOfferMessage = "Session handoff offered"
	const sourceOfferNextAction = "Continue from the immutable handover document"
	request := sessionmove.Request{
		SchemaVersion: sessionmove.RequestSchemaVersion,
		HandoffID:     "handoff-123", SuccessorWBSessionID: "wbs-successor", PredecessorWBSessionID: "wbs-source",
		SourceMachine: "macbook-air", TargetMachine: "hetzner-vm1", RepositoryRemote: "git@github.com:acme/app.git",
		Branch: "feature/session", SourceWorkCommit: strings.Repeat("a", 40), BundleCommit: strings.Repeat("b", 40),
		HandoverPath: ".wb/handoffs/handoff-123.md", HandoverDigest: sessionmove.DigestBytes([]byte("handover\n")),
		SourceRuntime: "codex", SourceModel: "gpt-5",
		WorkLogReference:   "worklog:effort/run/" + strings.Repeat("c", 64),
		SourceOfferMessage: sourceOfferMessage, SourceOfferNextAction: sourceOfferNextAction,
		SourceOfferDigest: sessionmove.DigestSourceOffer(sourceOfferMessage, sourceOfferNextAction),
		CreatedAt:         time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC),
	}
	raw, err := sessionmove.EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(raw)
	store := sessionmove.NewStore(filepath.Join(t.TempDir(), sessionmove.DirName))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveRoute(sessionmove.Route{
		HandoffID: request.HandoffID, RequestDigest: digest, TargetMachine: request.TargetMachine,
		Courier: sessionmove.CourierSSH, SSH: &sessionmove.SSHConfig{Host: "hetzner-vm1", WBPath: "/home/ai/go/bin/wb"},
	}); err != nil {
		t.Fatal(err)
	}
	targetReference, err := sessionmove.ExpectedTargetWorkLogReference(request, digest)
	if err != nil {
		t.Fatal(err)
	}
	receipt := sessionmove.Receipt{
		SchemaVersion: sessionmove.ReceiptSchemaVersion, HandoffID: request.HandoffID, RequestDigest: digest,
		SuccessorWBSessionID: request.SuccessorWBSessionID, PredecessorWBSessionID: request.PredecessorWBSessionID,
		TargetMachine: request.TargetMachine, TmuxName: "wb-session-" + request.SuccessorWBSessionID,
		Runtime: "codex", Model: "gpt-5", NativeHarnessID: "codex-native-123",
		AttemptID: "000001-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AttemptIndex: 1, PID: 4242,
		TargetWorkLogReference: targetReference.String(), PinnedCommit: request.BundleCommit,
		StartedAt: time.Date(2026, 8, 25, 12, 30, 0, 0, time.UTC),
	}
	source := session.Record{
		PID: 3131, WBSessionID: request.PredecessorWBSessionID, Machine: request.SourceMachine,
		Runtime: request.SourceRuntime, Model: request.SourceModel, StartedAt: time.Date(2026, 8, 25, 11, 0, 0, 0, time.UTC),
	}
	sealedAt := time.Date(2026, 8, 25, 12, 31, 0, 0, time.UTC)
	projectsRoot := t.TempDir()
	options := Options{
		Store: store, ProjectsRoot: projectsRoot, Request: request, RequestDigest: digest,
		Receipt: receipt, SourceSession: source, Now: func() time.Time { return sealedAt },
		EnsureSourceOffer: func(worktrees.ExternalSourceOfferOptions) (worktrees.ExternalSourceOfferResult, error) {
			return worktrees.ExternalSourceOfferResult{Replayed: true}, nil
		},
	}
	return custodyFixture{store: store, request: request, digest: digest, receipt: receipt, source: source, sealedAt: sealedAt, options: options}
}

func successfulSeal(fixture custodyFixture) func(worktrees.ExternalSourceSealOptions) (worktrees.ExternalSourceSealResult, error) {
	return func(worktrees.ExternalSourceSealOptions) (worktrees.ExternalSourceSealResult, error) {
		return worktrees.ExternalSourceSealResult{
			SourceWorkLogReference: fixture.request.WorkLogReference,
			TargetWorkLogReference: fixture.receipt.TargetWorkLogReference,
			SealedAt:               fixture.sealedAt,
		}, nil
	}
}

func countPhase(state sessionmove.State, phase sessionmove.Phase) int {
	count := 0
	for _, event := range state.Events {
		if event.Phase == phase {
			count++
		}
	}
	return count
}
