package sessiontransport

// Capabilities is the typed capability matrix REQ:transport-capability-matrix
// requires: what a resolved transport can prove about a live session, never
// what it might be able to approximate. Every field defaults false, so a
// transport that declares nothing gets the safest outcome — never submit,
// never advise, always record — from [ResolveDeliveryMode] by construction.
type Capabilities struct {
	// Kind names which shipped transport this matrix describes. It exists
	// so a Capabilities value carries its own identity when logged or
	// compared in a test, never so a caller branches on it directly —
	// REQ:single-transport-interface reserves that judgement for transport
	// selection, not for call sites above [Transport].
	Kind Kind

	// LiveStatus reports whether this transport can ask, right now, whether
	// the owning session is idle, working, blocked, or done. herdr's
	// `agent list`/`agent get` supply this; tmux structurally cannot.
	LiveStatus bool

	// EmptyInputEvidence reports whether this transport can produce
	// positive evidence that a pane's input box is empty. No shipped
	// transport declares this true today: the mechanism is Deferred in the
	// Feature (see "Empty-input evidence mechanism"), so
	// REQ:delivery-guards-enforced-and-rechecked's guard can never be
	// satisfied yet — by construction, not by circumstance.
	EmptyInputEvidence bool

	// AdvisoryDelivery reports whether this transport can paste unsubmitted
	// text into a live pane (herdr's `pane send-text`) for a
	// daemon-originated event outside the session's own-PR-outcome class.
	// tmux declares this false by default
	// (REQ:tmux-record-only-for-daemon-events): it has no live status and
	// no input evidence at all, so there is nothing to guard even an
	// advisory paste against a mid-typing human. This field governs only
	// daemon-originated delivery ([EventOwnPROutcome], [EventAdvisory]); it
	// has no bearing on [LineageSubmit].
	AdvisoryDelivery bool

	// LineageSubmit reports whether this transport submits the pre-existing
	// successor-messaging path — `wb session send`'s and `recall`'s
	// target-side write ([EventSuccessorMessage],
	// REQ:existing-successor-messaging-binding-path) — guarded by that
	// receipt's lineage rather than by the daemon wake's own-PR-outcome
	// restriction. tmux declares this true: its paste-buffer payload
	// already ends in a newline today
	// (internal/sessionmove/types.go:622's marshalJSON) and
	// REQ:tmux-parity-preserved keeps that unchanged. herdr and none
	// declare it false: no herdr submit path is built in this iteration
	// (Deferred: "Successor messaging's herdr submit path"), and none never
	// submits anything. LineageSubmit is never consulted for
	// [EventOwnPROutcome] or [EventAdvisory] — only [EventSuccessorMessage]
	// — so a transport declaring it true still never submits a
	// daemon-originated event (AC:tmux-never-delivers-unguarded's guarantee
	// extends to this field too).
	LineageSubmit bool

	// PaneEnumeration reports whether this transport can enumerate live
	// panes/agents to resolve a session's identity to a unique target
	// (REQ:pane-resolved-by-session-identity-match). Resolving to zero or
	// to more than one candidate is never treated as a target — see
	// [Transport.ResolvePane] and [ErrNoUniqueTarget].
	PaneEnumeration bool
}

// HerdrCapabilities returns the capability matrix Task 4's herdr transport
// declares. Task 2 states it here as the documented contract Task 4
// implements against; Task 4 owns actually wiring the herdr transport. It
// is a function, not an exported var, so no caller can corrupt the shared
// declaration by mutating what it got back (a struct assignment already
// copies by value, but an exported var remains directly assignable from
// any importer — a function returning a fresh value each call closes that
// off entirely).
func HerdrCapabilities() Capabilities {
	return Capabilities{
		Kind:               KindHerdr,
		LiveStatus:         true,
		EmptyInputEvidence: false,
		AdvisoryDelivery:   true,
		LineageSubmit:      false,
		PaneEnumeration:    true,
	}
}

// TmuxCapabilities returns the capability matrix Task 3's tmux transport
// declares by default (REQ:tmux-record-only-for-daemon-events): no live
// status, no empty-input evidence, and — the lead's default, not a fixed
// founder decision (see the Feature's Open Questions) — no advisory
// delivery either, so every daemon-originated event resolves record-only on
// tmux (AC:tmux-never-delivers-unguarded). LineageSubmit is true: tmux's
// pre-existing successor-messaging paste already submits today and keeps
// doing so unchanged (REQ:tmux-parity-preserved) — a fact about a
// different, differently guarded path
// (REQ:existing-successor-messaging-binding-path), not a relaxation of the
// daemon-wake guarantee above.
func TmuxCapabilities() Capabilities {
	return Capabilities{
		Kind:               KindTmux,
		LiveStatus:         false,
		EmptyInputEvidence: false,
		AdvisoryDelivery:   false,
		LineageSubmit:      true,
		PaneEnumeration:    false,
	}
}

// NoneCapabilities returns the capability matrix [NoneTransport] declares
// (REQ:none-transport-is-first-class): nothing is live, so every event —
// daemon-originated or the pre-existing successor-messaging path alike —
// resolves record-only (AC:none-transport-records-only).
func NoneCapabilities() Capabilities {
	return Capabilities{Kind: KindNone}
}
