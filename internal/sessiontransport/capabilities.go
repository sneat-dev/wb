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
	// daemon-originated delivery; it does not describe `wb session send`'s
	// or `recall`'s pre-existing, differently guarded successor-messaging
	// submit path (REQ:existing-successor-messaging-binding-path), which is
	// unaffected and keeps working on tmux exactly as it does today.
	AdvisoryDelivery bool

	// PaneEnumeration reports whether this transport can enumerate live
	// panes/agents to resolve a session's identity to a unique target
	// (REQ:pane-resolved-by-session-identity-match). Resolving to zero or
	// to more than one candidate is never treated as a target — see
	// [Transport.ResolvePane] and [ErrNoUniqueTarget].
	PaneEnumeration bool
}

// HerdrCapabilities is the capability matrix Task 4's herdr transport
// declares. Task 2 states it here as the documented contract Task 4
// implements against; Task 4 owns actually wiring the herdr transport.
var HerdrCapabilities = Capabilities{
	Kind:               KindHerdr,
	LiveStatus:         true,
	EmptyInputEvidence: false,
	AdvisoryDelivery:   true,
	PaneEnumeration:    true,
}

// TmuxCapabilities is the capability matrix Task 3's tmux transport
// declares by default (REQ:tmux-record-only-for-daemon-events): no live
// status, no empty-input evidence, and — the lead's default, not a fixed
// founder decision (see the Feature's Open Questions) — no advisory
// delivery either, so every daemon-originated event resolves record-only on
// tmux (AC:tmux-never-delivers-unguarded). This does not describe `wb
// session send`'s/`recall`'s pre-existing, differently guarded
// successor-messaging submit path
// (REQ:existing-successor-messaging-binding-path), which this matrix does
// not govern at all.
var TmuxCapabilities = Capabilities{
	Kind:               KindTmux,
	LiveStatus:         false,
	EmptyInputEvidence: false,
	AdvisoryDelivery:   false,
	PaneEnumeration:    false,
}

// NoneCapabilities is the capability matrix [NoneTransport] declares
// (REQ:none-transport-is-first-class): nothing is live, so every
// daemon-originated event resolves record-only
// (AC:none-transport-records-only).
var NoneCapabilities = Capabilities{Kind: KindNone}
