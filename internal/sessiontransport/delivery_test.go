package sessiontransport

import "testing"

// TestResolveDeliveryModeTmuxNeverDeliversUnguarded proves
// AC:tmux-never-delivers-unguarded directly against the capability matrix
// tmux declares by default: no live status and no empty-input evidence, so
// the daemon never performs a submitted delivery — and, per
// REQ:tmux-record-only-for-daemon-events, never an advisory paste either.
// Every daemon-originated event, including the own-PR-outcome class, must
// resolve record-only.
func TestResolveDeliveryModeTmuxNeverDeliversUnguarded(t *testing.T) {
	t.Parallel()
	for _, class := range []EventClass{EventOwnPROutcome, EventAdvisory} {
		if got := ResolveDeliveryMode(TmuxCapabilities, class); got != ModeRecordOnly {
			t.Errorf("ResolveDeliveryMode(TmuxCapabilities, %q) = %q, want %q (tmux must never deliver unguarded)",
				class, got, ModeRecordOnly)
		}
	}
}

// TestResolveDeliveryModeNoneTransportRecordsOnly proves
// AC:none-transport-records-only: with no live transport at all, every
// event class resolves record-only.
func TestResolveDeliveryModeNoneTransportRecordsOnly(t *testing.T) {
	t.Parallel()
	for _, class := range []EventClass{EventOwnPROutcome, EventAdvisory} {
		if got := ResolveDeliveryMode(NoneCapabilities, class); got != ModeRecordOnly {
			t.Errorf("ResolveDeliveryMode(NoneCapabilities, %q) = %q, want %q",
				class, got, ModeRecordOnly)
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
	if got := ResolveDeliveryMode(HerdrCapabilities, EventOwnPROutcome); got != ModeRecordOnly {
		t.Errorf("ResolveDeliveryMode(HerdrCapabilities, EventOwnPROutcome) = %q, want %q", got, ModeRecordOnly)
	}
}

// TestResolveDeliveryModeHerdrAdvisoryForEverythingElse proves
// AC:non-pr-events-stay-advisory's herdr half: an event outside the
// own-PR-outcome class is delivered advisory-only on herdr.
func TestResolveDeliveryModeHerdrAdvisoryForEverythingElse(t *testing.T) {
	t.Parallel()
	if got := ResolveDeliveryMode(HerdrCapabilities, EventAdvisory); got != ModeAdvisory {
		t.Errorf("ResolveDeliveryMode(HerdrCapabilities, EventAdvisory) = %q, want %q", got, ModeAdvisory)
	}
}

// TestResolveDeliveryModeGuardIsUnconditionalNotCapabilitySpecific proves
// the guard rule generically, independent of any shipped transport: absence
// of positive empty-input evidence is itself a guard failure for the
// own-PR-outcome class, even when live status is otherwise available. This
// documents why AC:guards-are-unconditional-and-block-submission holds
// "regardless of whether the owning session is idle, working, blocked, or
// done" — LiveStatus alone is never sufficient.
func TestResolveDeliveryModeGuardIsUnconditionalNotCapabilitySpecific(t *testing.T) {
	t.Parallel()
	caps := Capabilities{Kind: "hypothetical", LiveStatus: true, EmptyInputEvidence: false, AdvisoryDelivery: true}
	if got := ResolveDeliveryMode(caps, EventOwnPROutcome); got != ModeRecordOnly {
		t.Errorf("ResolveDeliveryMode(live-status-only, EventOwnPROutcome) = %q, want %q", got, ModeRecordOnly)
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
	if got := ResolveDeliveryMode(caps, EventOwnPROutcome); got != ModeSubmit {
		t.Errorf("ResolveDeliveryMode(fully-capable, EventOwnPROutcome) = %q, want %q", got, ModeSubmit)
	}
	// Every other class stays advisory even once submission exists for the
	// own-PR-outcome class — submission is never generalized to it.
	if got := ResolveDeliveryMode(caps, EventAdvisory); got != ModeAdvisory {
		t.Errorf("ResolveDeliveryMode(fully-capable, EventAdvisory) = %q, want %q", got, ModeAdvisory)
	}
}

func TestDeclaredCapabilitiesMatrixShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		caps Capabilities
	}{
		{"herdr", HerdrCapabilities},
		{"tmux", TmuxCapabilities},
		{"none", NoneCapabilities},
	}
	for _, tc := range cases {
		if tc.caps.EmptyInputEvidence {
			t.Errorf("%s capabilities declare EmptyInputEvidence = true; the Feature defers that mechanism entirely", tc.name)
		}
	}
	if !HerdrCapabilities.LiveStatus {
		t.Error("herdr capabilities must declare LiveStatus: herdr's agent list/get supply it")
	}
	if TmuxCapabilities.LiveStatus {
		t.Error("tmux capabilities must not declare LiveStatus: tmux structurally cannot report it")
	}
	if TmuxCapabilities.AdvisoryDelivery {
		t.Error("tmux capabilities must not declare AdvisoryDelivery by default (REQ:tmux-record-only-for-daemon-events)")
	}
	if NoneCapabilities != (Capabilities{Kind: KindNone}) {
		t.Errorf("NoneCapabilities = %#v, want every capability field false", NoneCapabilities)
	}
}
