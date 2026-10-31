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
	// `agent list`/`agent get` supply this; none cannot.
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
	// None cannot advise. This field governs daemon-originated delivery only,
	// independently of the explicit receipt-bound LineageSubmit capability.
	AdvisoryDelivery bool

	// LineageSubmit authorizes the receipt-bound successor-message path only.
	// Herdr declares it true; none false. It is consulted exclusively for
	// EventSuccessorMessage with OperationMessage and never upgrades a
	// daemon-originated event to a submitted prompt. Identity, custody and
	// at-most-once intent checks remain required at the actual delivery boundary.
	LineageSubmit bool

	// PaneEnumeration reports whether this transport can enumerate live
	// panes/agents to resolve a session's identity to a unique target
	// (REQ:pane-resolved-by-session-identity-match). Resolving to zero or
	// to more than one candidate is never treated as a target — see
	// [Transport.ResolvePane] and [ErrNoUniqueTarget].
	PaneEnumeration bool
}

// HerdrCapabilities declares the generic Herdr policy matrix by value.
// Empty-input evidence remains unavailable, so daemon wake cannot submit.
func HerdrCapabilities() Capabilities {
	return Capabilities{
		Kind:               KindHerdr,
		LiveStatus:         true,
		EmptyInputEvidence: false,
		AdvisoryDelivery:   true,
		LineageSubmit:      true,
		PaneEnumeration:    true,
	}
}

// NoneCapabilities returns the capability matrix [NoneTransport] declares
// (REQ:none-transport-is-first-class): nothing is live, so every event —
// daemon-originated or the pre-existing successor-messaging path alike —
// resolves record-only (AC:none-transport-records-only).
func NoneCapabilities() Capabilities {
	return Capabilities{Kind: KindNone}
}
