package transporttest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessiontransport"
)

// fakeTransport is a fully configurable sessiontransport.Transport used to
// exercise both the passing path and, per field, exactly one broken
// behavior at a time (M1: "test each check directly against a deliberately
// broken fake, and cover every failure branch").
type fakeTransport struct {
	kind sessiontransport.Kind
	caps sessiontransport.Capabilities

	kindMismatch bool
	deliverErr   error
	// forceOutcome, when non-empty, is reported as Receipt.Outcome
	// regardless of what ResolveDeliveryMode would have computed — the
	// deliberate violation CheckAdvisoryNeverExceedsCapability,
	// CheckSuccessorMessageNeverExceedsCapability and
	// CheckDaemonClassNeverUsesLineageSubmit each need to catch.
	forceOutcome sessiontransport.DeliveryMode
	// ignoreOperationAndTarget reproduces round 3's exact finding: Deliver
	// computes its mode from Class and Capabilities alone, exactly like the
	// pre-fix two-argument ResolveDeliveryMode signature — ignoring both
	// Operation (so a wake mislabelled EventSuccessorMessage still submits
	// under LineageSubmit) and whether target is zero.
	ignoreOperationAndTarget bool

	resolveErr    error
	resolveTarget sessiontransport.Target
}

func conformingFake(kind sessiontransport.Kind, caps sessiontransport.Capabilities) fakeTransport {
	return fakeTransport{kind: kind, caps: caps, resolveErr: sessiontransport.ErrNoUniqueTarget}
}

func (f fakeTransport) Kind() sessiontransport.Kind { return f.kind }

func (f fakeTransport) Capabilities() sessiontransport.Capabilities {
	caps := f.caps
	if f.kindMismatch {
		caps.Kind = "mismatched-on-purpose"
	}
	return caps
}

func (f fakeTransport) ResolvePane(context.Context, sessiontransport.Identity) (sessiontransport.Target, error) {
	if f.resolveErr != nil {
		return sessiontransport.Target{}, f.resolveErr
	}
	return f.resolveTarget, nil
}

func (fakeTransport) Launch(context.Context, sessiontransport.LaunchRequest) (sessiontransport.Target, error) {
	return sessiontransport.Target{}, nil
}

func (fakeTransport) Inspect(context.Context, sessiontransport.Target) (sessiontransport.Inspection, error) {
	return sessiontransport.Inspection{}, nil
}

func (fakeTransport) AddressFor(id string) string { return "wb-session-" + id }

func (f fakeTransport) Deliver(_ context.Context, target sessiontransport.Target, delivery sessiontransport.Delivery) (sessiontransport.Receipt, error) {
	if f.deliverErr != nil {
		return sessiontransport.Receipt{}, f.deliverErr
	}
	outcome := f.forceOutcome
	if outcome == "" {
		if f.ignoreOperationAndTarget {
			outcome = bugriddenResolve(f.caps, delivery.Class)
		} else {
			outcome = sessiontransport.ResolveDeliveryMode(f.caps, delivery.Operation, delivery.Class)
			if target.IsZero() {
				outcome = sessiontransport.ModeRecordOnly
			}
		}
	}
	return sessiontransport.Receipt{Operation: delivery.Operation, Outcome: outcome, Target: target, At: time.Now().UTC()}, nil
}

// bugriddenResolve reproduces round 3's exact bug, ignoring Operation
// entirely — the same shape the pre-fix two-argument ResolveDeliveryMode
// signature had.
func bugriddenResolve(caps sessiontransport.Capabilities, class sessiontransport.EventClass) sessiontransport.DeliveryMode {
	switch class {
	case sessiontransport.EventOwnPROutcome:
		if caps.LiveStatus && caps.EmptyInputEvidence {
			return sessiontransport.ModeSubmit
		}
	case sessiontransport.EventSuccessorMessage:
		if caps.LineageSubmit {
			return sessiontransport.ModeSubmit
		}
	case sessiontransport.EventAdvisory:
		if caps.AdvisoryDelivery {
			return sessiontransport.ModeAdvisory
		}
	}
	return sessiontransport.ModeRecordOnly
}

var _ sessiontransport.Transport = fakeTransport{}

func TestSuitePassesAConformingFake(t *testing.T) {
	t.Parallel()
	Suite(t, func() sessiontransport.Transport {
		return conformingFake(sessiontransport.KindNone, sessiontransport.NoneCapabilities())
	})
}

func TestSuitePassesNoneTransport(t *testing.T) {
	t.Parallel()
	Suite(t, func() sessiontransport.Transport { return sessiontransport.NoneTransport{} })
}

func TestRunReturnsEmptyStringWhenEveryCheckPasses(t *testing.T) {
	t.Parallel()
	factory := func() sessiontransport.Transport {
		return conformingFake(sessiontransport.KindHerdr, sessiontransport.HerdrCapabilities())
	}
	if got := run(context.Background(), factory); got != "" {
		t.Fatalf("run() = %q, want empty", got)
	}
}

func TestRunCollectsEveryFailureNotJustTheFirst(t *testing.T) {
	t.Parallel()
	broken := fakeTransport{
		kind:         sessiontransport.KindTmux,
		caps:         sessiontransport.TmuxCapabilities(),
		kindMismatch: true,                                      // breaks CheckKindMatchesCapabilities
		deliverErr:   errors.New("deliberately broken Deliver"), // breaks CheckDeliverNeverErrors and every check downstream of it
	}
	message := run(context.Background(), func() sessiontransport.Transport { return broken })
	if message == "" {
		t.Fatal("run() = \"\", want a non-empty failure report")
	}
	for _, want := range []string{"KindMatchesCapabilities", "DeliverNeverErrors"} {
		if !strings.Contains(message, want) {
			t.Errorf("run() report %q does not mention failing check %q", message, want)
		}
	}
}

// --- CheckKindMatchesCapabilities ---

func TestCheckKindMatchesCapabilitiesPasses(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindHerdr, sessiontransport.HerdrCapabilities())
	if err := CheckKindMatchesCapabilities(context.Background(), fake); err != nil {
		t.Fatalf("CheckKindMatchesCapabilities() = %v, want nil", err)
	}
}

func TestCheckKindMatchesCapabilitiesCatchesMismatch(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindHerdr, sessiontransport.HerdrCapabilities())
	fake.kindMismatch = true
	if err := CheckKindMatchesCapabilities(context.Background(), fake); err == nil {
		t.Fatal("CheckKindMatchesCapabilities() = nil, want an error for a mismatched Kind")
	}
}

// --- CheckDeliverNeverErrors ---

func TestCheckDeliverNeverErrorsPasses(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindNone, sessiontransport.NoneCapabilities())
	if err := CheckDeliverNeverErrors(context.Background(), fake); err != nil {
		t.Fatalf("CheckDeliverNeverErrors() = %v, want nil", err)
	}
}

func TestCheckDeliverNeverErrorsCatchesAnErroringDeliver(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindNone, sessiontransport.NoneCapabilities())
	fake.deliverErr = errors.New("deliberately broken")
	if err := CheckDeliverNeverErrors(context.Background(), fake); err == nil {
		t.Fatal("CheckDeliverNeverErrors() = nil, want an error when Deliver errors")
	}
}

// TestCheckDeliverNeverErrorsCatchesRound3sExactBug proves this check alone
// would have caught round 3's finding: an Outcome that exceeds what
// ResolveDeliveryMode computes for the given (operation, class) pair —
// here, a wake mislabelled as a successor message submitting because the
// transport ignored Operation.
func TestCheckDeliverNeverErrorsCatchesRound3sExactBug(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindTmux, sessiontransport.TmuxCapabilities())
	fake.ignoreOperationAndTarget = true
	if err := CheckDeliverNeverErrors(context.Background(), fake); err == nil {
		t.Fatal("CheckDeliverNeverErrors() = nil, want an error: Outcome exceeded the ResolveDeliveryMode bound")
	}
}

// TestCheckDeliverNeverErrorsAllowsAConservativeDowngrade proves "MUST NOT
// exceed" permits a transport to be more conservative than its
// Capabilities strictly allow - always record-only, even when e.g.
// AdvisoryDelivery is true - without failing this check.
func TestCheckDeliverNeverErrorsAllowsAConservativeDowngrade(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindHerdr, sessiontransport.HerdrCapabilities())
	fake.forceOutcome = sessiontransport.ModeRecordOnly
	if err := CheckDeliverNeverErrors(context.Background(), fake); err != nil {
		t.Fatalf("CheckDeliverNeverErrors() = %v, want nil: a conservative downgrade must never fail this check", err)
	}
}

// --- CheckAdvisoryNeverExceedsCapability ---

func TestCheckAdvisoryNeverExceedsCapabilityPasses(t *testing.T) {
	t.Parallel()
	for _, fake := range []fakeTransport{
		conformingFake(sessiontransport.KindTmux, sessiontransport.TmuxCapabilities()),   // AdvisoryDelivery false
		conformingFake(sessiontransport.KindHerdr, sessiontransport.HerdrCapabilities()), // AdvisoryDelivery true
	} {
		if err := CheckAdvisoryNeverExceedsCapability(context.Background(), fake); err != nil {
			t.Fatalf("CheckAdvisoryNeverExceedsCapability(%s) = %v, want nil", fake.Kind(), err)
		}
	}
}

func TestCheckAdvisoryNeverExceedsCapabilityCatchesForcedAdvisoryWithoutCapability(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindTmux, sessiontransport.TmuxCapabilities())
	fake.forceOutcome = sessiontransport.ModeAdvisory
	if err := CheckAdvisoryNeverExceedsCapability(context.Background(), fake); err == nil {
		t.Fatal("CheckAdvisoryNeverExceedsCapability() = nil, want an error when a non-advisory-capable transport reports advisory")
	}
}

func TestCheckAdvisoryNeverExceedsCapabilityCatchesForcedSubmit(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindHerdr, sessiontransport.HerdrCapabilities())
	fake.forceOutcome = sessiontransport.ModeSubmit
	if err := CheckAdvisoryNeverExceedsCapability(context.Background(), fake); err == nil {
		t.Fatal("CheckAdvisoryNeverExceedsCapability() = nil, want an error when an advisory delivery reports submit")
	}
}

func TestCheckAdvisoryNeverExceedsCapabilityCatchesDeliverError(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindHerdr, sessiontransport.HerdrCapabilities())
	fake.deliverErr = errors.New("deliberately broken")
	if err := CheckAdvisoryNeverExceedsCapability(context.Background(), fake); err == nil {
		t.Fatal("CheckAdvisoryNeverExceedsCapability() = nil, want an error when Deliver errors")
	}
}

// --- CheckSuccessorMessageNeverExceedsCapability ---

func TestCheckSuccessorMessageNeverExceedsCapabilityPasses(t *testing.T) {
	t.Parallel()
	for _, fake := range []fakeTransport{
		conformingFake(sessiontransport.KindTmux, sessiontransport.TmuxCapabilities()),   // LineageSubmit true
		conformingFake(sessiontransport.KindHerdr, sessiontransport.HerdrCapabilities()), // LineageSubmit false
	} {
		if err := CheckSuccessorMessageNeverExceedsCapability(context.Background(), fake); err != nil {
			t.Fatalf("CheckSuccessorMessageNeverExceedsCapability(%s) = %v, want nil", fake.Kind(), err)
		}
	}
}

func TestCheckSuccessorMessageNeverExceedsCapabilityCatchesForcedAdvisory(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindTmux, sessiontransport.TmuxCapabilities())
	fake.forceOutcome = sessiontransport.ModeAdvisory
	if err := CheckSuccessorMessageNeverExceedsCapability(context.Background(), fake); err == nil {
		t.Fatal("CheckSuccessorMessageNeverExceedsCapability() = nil, want an error: this path is never advisory")
	}
}

func TestCheckSuccessorMessageNeverExceedsCapabilityCatchesForcedSubmitWithoutLineageSubmit(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindHerdr, sessiontransport.HerdrCapabilities())
	fake.forceOutcome = sessiontransport.ModeSubmit
	if err := CheckSuccessorMessageNeverExceedsCapability(context.Background(), fake); err == nil {
		t.Fatal("CheckSuccessorMessageNeverExceedsCapability() = nil, want an error when a non-LineageSubmit transport reports submit")
	}
}

func TestCheckSuccessorMessageNeverExceedsCapabilityCatchesDeliverError(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindTmux, sessiontransport.TmuxCapabilities())
	fake.deliverErr = errors.New("deliberately broken")
	if err := CheckSuccessorMessageNeverExceedsCapability(context.Background(), fake); err == nil {
		t.Fatal("CheckSuccessorMessageNeverExceedsCapability() = nil, want an error when Deliver errors")
	}
}

// --- CheckDaemonClassNeverUsesLineageSubmit (the B1 review round's probe) ---

func TestCheckDaemonClassNeverUsesLineageSubmitPasses(t *testing.T) {
	t.Parallel()
	// tmux declares LineageSubmit true, but a conforming Deliver still never
	// lets a daemon-originated class reach submit.
	fake := conformingFake(sessiontransport.KindTmux, sessiontransport.TmuxCapabilities())
	if err := CheckDaemonClassNeverUsesLineageSubmit(context.Background(), fake); err != nil {
		t.Fatalf("CheckDaemonClassNeverUsesLineageSubmit() = %v, want nil", err)
	}
}

func TestCheckDaemonClassNeverUsesLineageSubmitCatchesAMisbehavingTransport(t *testing.T) {
	t.Parallel()
	// A transport that (incorrectly) lets LineageSubmit leak into a
	// daemon-originated class must be caught.
	fake := conformingFake(sessiontransport.KindTmux, sessiontransport.TmuxCapabilities())
	fake.forceOutcome = sessiontransport.ModeSubmit
	if err := CheckDaemonClassNeverUsesLineageSubmit(context.Background(), fake); err == nil {
		t.Fatal("CheckDaemonClassNeverUsesLineageSubmit() = nil, want an error when a daemon-class delivery reports submit")
	}
}

func TestCheckDaemonClassNeverUsesLineageSubmitCatchesDeliverError(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindTmux, sessiontransport.TmuxCapabilities())
	fake.deliverErr = errors.New("deliberately broken")
	if err := CheckDaemonClassNeverUsesLineageSubmit(context.Background(), fake); err == nil {
		t.Fatal("CheckDaemonClassNeverUsesLineageSubmit() = nil, want an error when Deliver errors")
	}
}

// --- CheckMismatchedOperationNeverSubmits (round 3's exact finding) ---

func TestCheckMismatchedOperationNeverSubmitsPasses(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindTmux, sessiontransport.TmuxCapabilities())
	if err := CheckMismatchedOperationNeverSubmits(context.Background(), fake); err != nil {
		t.Fatalf("CheckMismatchedOperationNeverSubmits() = %v, want nil", err)
	}
}

// TestCheckMismatchedOperationNeverSubmitsCatchesRound3sExactBug reproduces
// round 3's finding exactly: Delivery{Operation: OperationWake, Class:
// EventSuccessorMessage} against a transport that (like the pre-fix
// ResolveDeliveryMode) ignores Operation must be caught, because tmux
// declares LineageSubmit true.
func TestCheckMismatchedOperationNeverSubmitsCatchesRound3sExactBug(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindTmux, sessiontransport.TmuxCapabilities())
	fake.ignoreOperationAndTarget = true
	if err := CheckMismatchedOperationNeverSubmits(context.Background(), fake); err == nil {
		t.Fatal("CheckMismatchedOperationNeverSubmits() = nil, want an error: a wake mislabelled as a successor message must never submit")
	}
}

func TestCheckMismatchedOperationNeverSubmitsCatchesDeliverError(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindTmux, sessiontransport.TmuxCapabilities())
	fake.deliverErr = errors.New("deliberately broken")
	if err := CheckMismatchedOperationNeverSubmits(context.Background(), fake); err == nil {
		t.Fatal("CheckMismatchedOperationNeverSubmits() = nil, want an error when Deliver errors")
	}
}

// --- CheckZeroTargetAlwaysRecordsOnly ---

func TestCheckZeroTargetAlwaysRecordsOnlyPasses(t *testing.T) {
	t.Parallel()
	for _, fake := range []fakeTransport{
		conformingFake(sessiontransport.KindNone, sessiontransport.NoneCapabilities()),
		conformingFake(sessiontransport.KindTmux, sessiontransport.TmuxCapabilities()),
		conformingFake(sessiontransport.KindHerdr, sessiontransport.HerdrCapabilities()),
	} {
		if err := CheckZeroTargetAlwaysRecordsOnly(context.Background(), fake); err != nil {
			t.Fatalf("CheckZeroTargetAlwaysRecordsOnly(%s) = %v, want nil", fake.Kind(), err)
		}
	}
}

// TestCheckZeroTargetAlwaysRecordsOnlyCatchesAMisbehavingTransport proves
// this is the check that would catch a transport that computes its mode
// purely from Capabilities/Operation/Class and never looks at target at
// all — a fully-capable transport that ignores a zero Target and reports
// Outcome = advisory anyway.
func TestCheckZeroTargetAlwaysRecordsOnlyCatchesAMisbehavingTransport(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindHerdr, sessiontransport.HerdrCapabilities())
	fake.forceOutcome = sessiontransport.ModeAdvisory
	if err := CheckZeroTargetAlwaysRecordsOnly(context.Background(), fake); err == nil {
		t.Fatal("CheckZeroTargetAlwaysRecordsOnly() = nil, want an error when a zero-target delivery reports advisory")
	}
}

func TestCheckZeroTargetAlwaysRecordsOnlyCatchesDeliverError(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindHerdr, sessiontransport.HerdrCapabilities())
	fake.deliverErr = errors.New("deliberately broken")
	if err := CheckZeroTargetAlwaysRecordsOnly(context.Background(), fake); err == nil {
		t.Fatal("CheckZeroTargetAlwaysRecordsOnly() = nil, want an error when Deliver errors")
	}
}

// --- CheckResolvePaneRequiresNoUniqueTarget (S2) ---

func TestCheckResolvePaneRequiresNoUniqueTargetPasses(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindHerdr, sessiontransport.HerdrCapabilities())
	if err := CheckResolvePaneRequiresNoUniqueTarget(context.Background(), fake); err != nil {
		t.Fatalf("CheckResolvePaneRequiresNoUniqueTarget() = %v, want nil", err)
	}
}

func TestCheckResolvePaneRequiresNoUniqueTargetCatchesAGuessedTarget(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindHerdr, sessiontransport.HerdrCapabilities())
	fake.resolveErr = nil
	fake.resolveTarget = sessiontransport.Target{Kind: sessiontransport.KindHerdr, ID: "w1:p2"}
	if err := CheckResolvePaneRequiresNoUniqueTarget(context.Background(), fake); err == nil {
		t.Fatal("CheckResolvePaneRequiresNoUniqueTarget() = nil, want an error when ResolvePane guesses a target for an empty identity")
	}
}

func TestCheckResolvePaneRequiresNoUniqueTargetCatchesTheWrongError(t *testing.T) {
	t.Parallel()
	fake := conformingFake(sessiontransport.KindHerdr, sessiontransport.HerdrCapabilities())
	fake.resolveErr = errors.New("some other, unrelated failure")
	if err := CheckResolvePaneRequiresNoUniqueTarget(context.Background(), fake); err == nil {
		t.Fatal("CheckResolvePaneRequiresNoUniqueTarget() = nil, want an error when ResolvePane fails with the wrong sentinel")
	}
}
