package sessiontransport

import "testing"

// TestResolveDeliveryModeTmuxNeverDeliversUnguarded proves
// AC:tmux-never-delivers-unguarded directly against the capability matrix
// tmux declares by default: no live status and no empty-input evidence, so
// the daemon never performs a submitted delivery — and, per
// REQ:tmux-record-only-for-daemon-events, never an advisory paste either.
// Every daemon-originated event, including the own-PR-outcome class, must
// resolve record-only, even though tmux declares LineageSubmit true for its
// unrelated, pre-existing successor-messaging path.
func TestResolveDeliveryModeTmuxNeverDeliversUnguarded(t *testing.T) {
	t.Parallel()
	for _, class := range []EventClass{EventOwnPROutcome, EventAdvisory} {
		if got := ResolveDeliveryMode(TmuxCapabilities(), OperationWake, class); got != ModeRecordOnly {
			t.Errorf("ResolveDeliveryMode(TmuxCapabilities(), wake, %q) = %q, want %q (tmux must never deliver unguarded)",
				class, got, ModeRecordOnly)
		}
	}
}

// TestResolveDeliveryModeNoneTransportRecordsOnly proves
// AC:none-transport-records-only: with no live transport at all, every
// event class resolves record-only.
func TestResolveDeliveryModeNoneTransportRecordsOnly(t *testing.T) {
	t.Parallel()
	for _, pair := range []struct {
		Operation Operation
		Class     EventClass
	}{
		{OperationWake, EventOwnPROutcome}, {OperationWake, EventAdvisory}, {OperationMessage, EventSuccessorMessage},
	} {
		if got := ResolveDeliveryMode(NoneCapabilities(), pair.Operation, pair.Class); got != ModeRecordOnly {
			t.Errorf("ResolveDeliveryMode(NoneCapabilities(), %q, %q) = %q, want %q",
				pair.Operation, pair.Class, got, ModeRecordOnly)
		}
	}
}

// TestResolveDeliveryModeHerdrOwnPROutcomeIsRecordOnlyThisIteration proves
// AC:own-pr-outcome-is-recorded-and-visible's negative half and
// AC:guards-are-unconditional-and-block-submission for herdr specifically:
// herdr has live status but never empty-input evidence in this iteration,
// so the own-PR-outcome class is always record-only, never advisory and
// never submitted, even though herdr can advise for every other class.
func TestResolveDeliveryModeHerdrOwnPROutcomeIsRecordOnlyThisIteration(t *testing.T) {
	t.Parallel()
	if got := ResolveDeliveryMode(HerdrCapabilities(), OperationWake, EventOwnPROutcome); got != ModeRecordOnly {
		t.Errorf("ResolveDeliveryMode(HerdrCapabilities(), wake, EventOwnPROutcome) = %q, want %q", got, ModeRecordOnly)
	}
}

// TestResolveDeliveryModeHerdrAdvisoryForEverythingElse proves
// AC:non-pr-events-stay-advisory's herdr half: an event outside the
// own-PR-outcome class is delivered advisory-only on herdr.
func TestResolveDeliveryModeHerdrAdvisoryForEverythingElse(t *testing.T) {
	t.Parallel()
	if got := ResolveDeliveryMode(HerdrCapabilities(), OperationWake, EventAdvisory); got != ModeAdvisory {
		t.Errorf("ResolveDeliveryMode(HerdrCapabilities(), wake, EventAdvisory) = %q, want %q", got, ModeAdvisory)
	}
}

// TestResolveDeliveryModeHerdrNeverSubmitsSuccessorMessageEither proves that
// herdr's live status and advisory capability do not leak into
// EventSuccessorMessage: only LineageSubmit governs that class, and herdr
// does not declare it (Deferred: "Successor messaging's herdr submit
// path").
func TestResolveDeliveryModeHerdrNeverSubmitsSuccessorMessageEither(t *testing.T) {
	t.Parallel()
	if got := ResolveDeliveryMode(HerdrCapabilities(), OperationMessage, EventSuccessorMessage); got != ModeRecordOnly {
		t.Errorf("ResolveDeliveryMode(HerdrCapabilities(), message, EventSuccessorMessage) = %q, want %q", got, ModeRecordOnly)
	}
}

// TestResolveDeliveryModeTmuxSubmitsSuccessorMessageOnly proves B1's fix:
// tmux's LineageSubmit grounds ModeSubmit for EventSuccessorMessage — its
// pre-existing, unaffected paste-with-newline path — and only that class;
// TestResolveDeliveryModeTmuxNeverDeliversUnguarded above already proves the
// daemon-originated classes stay record-only on the identical Capabilities
// value.
func TestResolveDeliveryModeTmuxSubmitsSuccessorMessageOnly(t *testing.T) {
	t.Parallel()
	if got := ResolveDeliveryMode(TmuxCapabilities(), OperationMessage, EventSuccessorMessage); got != ModeSubmit {
		t.Errorf("ResolveDeliveryMode(TmuxCapabilities(), message, EventSuccessorMessage) = %q, want %q", got, ModeSubmit)
	}
}

// TestResolveDeliveryModeLineageSubmitNeverGroundsADaemonClass is the B1
// review round's explicit probe, expressed directly against
// ResolveDeliveryMode: a hypothetical transport that claims LineageSubmit
// but nothing else must still never submit a daemon-originated event, even
// when paired with OperationWake (the correct Operation for those classes).
func TestResolveDeliveryModeLineageSubmitNeverGroundsADaemonClass(t *testing.T) {
	t.Parallel()
	caps := Capabilities{Kind: "hypothetical", LineageSubmit: true}
	for _, class := range []EventClass{EventOwnPROutcome, EventAdvisory} {
		if got := ResolveDeliveryMode(caps, OperationWake, class); got != ModeRecordOnly {
			t.Errorf("ResolveDeliveryMode(LineageSubmit-only, wake, %q) = %q, want %q", class, got, ModeRecordOnly)
		}
	}
	// The one class LineageSubmit actually governs still resolves submit,
	// paired with its own correct Operation.
	if got := ResolveDeliveryMode(caps, OperationMessage, EventSuccessorMessage); got != ModeSubmit {
		t.Errorf("ResolveDeliveryMode(LineageSubmit-only, message, EventSuccessorMessage) = %q, want %q", got, ModeSubmit)
	}
}

// TestResolveDeliveryModeMismatchedOperationNeverSubmits is round 3's
// explicit finding, expressed directly against ResolveDeliveryMode: a wake
// mislabelled with EventSuccessorMessage must never inherit
// EventSuccessorMessage's submit path just because the resolved transport
// declares LineageSubmit — that path belongs only to OperationMessage.
// Symmetrically, EventOwnPROutcome/EventAdvisory paired with the wrong
// Operation (OperationMessage) must never resolve anything but record-only
// either, however capable the transport.
func TestResolveDeliveryModeMismatchedOperationNeverSubmits(t *testing.T) {
	t.Parallel()
	fullyCapable := Capabilities{
		Kind: "hypothetical-fully-capable", LiveStatus: true, EmptyInputEvidence: true, AdvisoryDelivery: true, LineageSubmit: true,
	}
	mismatches := []struct {
		Operation Operation
		Class     EventClass
	}{
		{OperationWake, EventSuccessorMessage}, // round 3's exact finding
		{OperationMessage, EventOwnPROutcome},
		{OperationMessage, EventAdvisory},
	}
	for _, mismatch := range mismatches {
		if got := ResolveDeliveryMode(fullyCapable, mismatch.Operation, mismatch.Class); got != ModeRecordOnly {
			t.Errorf("ResolveDeliveryMode(fully-capable, %q, %q) = %q, want %q (mismatched operation/class must never submit or advise)",
				mismatch.Operation, mismatch.Class, got, ModeRecordOnly)
		}
	}
}

// TestResolveDeliveryModeGuardIsUnconditionalNotCapabilitySpecific proves
// the own-PR-outcome guard rule generically, independent of any shipped
// transport: absence of positive empty-input evidence is itself a guard
// failure, even when live status is otherwise available. This documents
// why AC:guards-are-unconditional-and-block-submission holds "regardless of
// whether the owning session is idle, working, blocked, or done" —
// LiveStatus alone is never sufficient.
func TestResolveDeliveryModeGuardIsUnconditionalNotCapabilitySpecific(t *testing.T) {
	t.Parallel()
	caps := Capabilities{Kind: "hypothetical", LiveStatus: true, EmptyInputEvidence: false, AdvisoryDelivery: true}
	if got := ResolveDeliveryMode(caps, OperationWake, EventOwnPROutcome); got != ModeRecordOnly {
		t.Errorf("ResolveDeliveryMode(live-status-only, wake, EventOwnPROutcome) = %q, want %q", got, ModeRecordOnly)
	}
}

// TestResolveDeliveryModeSubmitsOnceEvidenceExists documents the mechanism's
// forward compatibility: REQ:prompt-restricted-to-own-pr-outcome says
// submission "activates without a design change once the Deferred evidence
// mechanism lands." No shipped Capabilities value reaches this branch
// today; this test proves the branch itself is correct and reachable, so a
// later change need only start declaring EmptyInputEvidence true, not
// rewrite this function.
func TestResolveDeliveryModeSubmitsOnceEvidenceExists(t *testing.T) {
	t.Parallel()
	caps := Capabilities{Kind: "hypothetical-future-herdr", LiveStatus: true, EmptyInputEvidence: true, AdvisoryDelivery: true}
	if got := ResolveDeliveryMode(caps, OperationWake, EventOwnPROutcome); got != ModeSubmit {
		t.Errorf("ResolveDeliveryMode(fully-capable, wake, EventOwnPROutcome) = %q, want %q", got, ModeSubmit)
	}
	// Every other class stays advisory even once submission exists for the
	// own-PR-outcome class — submission is never generalized to it.
	if got := ResolveDeliveryMode(caps, OperationWake, EventAdvisory); got != ModeAdvisory {
		t.Errorf("ResolveDeliveryMode(fully-capable, wake, EventAdvisory) = %q, want %q", got, ModeAdvisory)
	}
}

// TestResolveDeliveryModeUnknownOrEmptyClassRecordsOnly proves M3: an
// unrecognized or empty EventClass is never grounds to advise or submit,
// even against a fully-capable Capabilities value.
func TestResolveDeliveryModeUnknownOrEmptyClassRecordsOnly(t *testing.T) {
	t.Parallel()
	caps := Capabilities{Kind: "hypothetical", LiveStatus: true, EmptyInputEvidence: true, AdvisoryDelivery: true, LineageSubmit: true}
	for _, class := range []EventClass{"", "some_unrecognized_class"} {
		for _, op := range []Operation{OperationWake, OperationMessage} {
			if got := ResolveDeliveryMode(caps, op, class); got != ModeRecordOnly {
				t.Errorf("ResolveDeliveryMode(fully-capable, %q, %q) = %q, want %q", op, class, got, ModeRecordOnly)
			}
		}
	}
}

// TestDeliveryModeExceeds proves the ranking DeliveryModeExceeds encodes:
// submit exceeds advisory and record-only; advisory exceeds record-only;
// nothing exceeds submit; a mode never exceeds itself.
func TestDeliveryModeExceeds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		got, allowed DeliveryMode
		want         bool
	}{
		{ModeRecordOnly, ModeRecordOnly, false},
		{ModeRecordOnly, ModeAdvisory, false},
		{ModeRecordOnly, ModeSubmit, false},
		{ModeAdvisory, ModeRecordOnly, true},
		{ModeAdvisory, ModeAdvisory, false},
		{ModeAdvisory, ModeSubmit, false},
		{ModeSubmit, ModeRecordOnly, true},
		{ModeSubmit, ModeAdvisory, true},
		{ModeSubmit, ModeSubmit, false},
	}
	for _, tc := range cases {
		if got := DeliveryModeExceeds(tc.got, tc.allowed); got != tc.want {
			t.Errorf("DeliveryModeExceeds(%q, %q) = %v, want %v", tc.got, tc.allowed, got, tc.want)
		}
	}
}

func TestDeclaredCapabilitiesMatrixShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		caps Capabilities
	}{
		{"herdr", HerdrCapabilities()},
		{"tmux", TmuxCapabilities()},
		{"none", NoneCapabilities()},
	}
	for _, tc := range cases {
		if tc.caps.EmptyInputEvidence {
			t.Errorf("%s capabilities declare EmptyInputEvidence = true; the Feature defers that mechanism entirely", tc.name)
		}
	}
	if !HerdrCapabilities().LiveStatus {
		t.Error("herdr capabilities must declare LiveStatus: herdr's agent list/get supply it")
	}
	if TmuxCapabilities().LiveStatus {
		t.Error("tmux capabilities must not declare LiveStatus: tmux structurally cannot report it")
	}
	if TmuxCapabilities().AdvisoryDelivery {
		t.Error("tmux capabilities must not declare AdvisoryDelivery by default (REQ:tmux-record-only-for-daemon-events)")
	}
	if !TmuxCapabilities().LineageSubmit {
		t.Error("tmux capabilities must declare LineageSubmit: its successor-messaging paste already submits today")
	}
	if HerdrCapabilities().LineageSubmit {
		t.Error("herdr capabilities must not declare LineageSubmit: no herdr submit path is built in this iteration")
	}
	if NoneCapabilities() != (Capabilities{Kind: KindNone}) {
		t.Errorf("NoneCapabilities() = %#v, want every capability field false", NoneCapabilities())
	}
}

func TestCapabilitiesFunctionsReturnIndependentValues(t *testing.T) {
	t.Parallel()
	// M4: these are functions, not shared mutable vars, so mutating one
	// caller's copy must never affect the next caller's.
	first := HerdrCapabilities()
	first.LiveStatus = false
	if second := HerdrCapabilities(); !second.LiveStatus {
		t.Fatal("mutating one HerdrCapabilities() result affected a later call; want independent values")
	}
}
