package sessiontransport

import "time"

// Operation names one of the terminal-addressing responsibilities
// REQ:single-transport-interface lists. Every one of them goes through
// [Transport.Deliver], tagged by Operation, rather than a call site
// branching on transport kind.
type Operation string

const (
	// OperationMessage is `wb session message`'s and `wb session
	// send`'s/`recall`'s target-side write
	// (REQ:existing-successor-messaging-binding-path).
	OperationMessage Operation = "message"
	// OperationMove is the move/receive continuation delivery boundary.
	OperationMove Operation = "move"
	// OperationPark is the park/resume delivery boundary.
	OperationPark Operation = "park"
	// OperationReceive is the courier delivery boundary's receiver side.
	OperationReceive Operation = "receive"
	// OperationWake is the daemon's own-PR-outcome and advisory wake path
	// (REQ:prompt-restricted-to-own-pr-outcome,
	// REQ:advisory-for-everything-else).
	OperationWake Operation = "wake"
)

// EventClass distinguishes the one event class ever eligible for a
// submitted (binding) delivery from everything else
// (REQ:prompt-restricted-to-own-pr-outcome,
// REQ:advisory-for-everything-else). It matters only for [OperationWake];
// every other Operation always resolves through the [EventAdvisory] branch
// of [ResolveDeliveryMode].
type EventClass string

const (
	// EventOwnPROutcome is the session's own registered pull request
	// settling — the only event class REQ:prompt-restricted-to-own-pr-outcome
	// ever names as submit-eligible. Even it never actually resolves
	// [ModeSubmit] in this iteration, because the empty-input evidence
	// guard can never be satisfied
	// (AC:guards-are-unconditional-and-block-submission).
	EventOwnPROutcome EventClass = "own_pr_outcome"

	// EventAdvisory is every other daemon-originated event class
	// (REQ:advisory-for-everything-else). It is never submit-eligible; it
	// resolves either [ModeAdvisory] or [ModeRecordOnly].
	EventAdvisory EventClass = "advisory"
)

// DeliveryMode is what [ResolveDeliveryMode] decided, and what
// [Transport.Deliver] must honor exactly: [ModeRecordOnly] must never touch
// a live pane, [ModeAdvisory] must never submit, and [ModeSubmit] is
// reserved for the one path this Feature specifies but does not build
// (REQ:prompt-restricted-to-own-pr-outcome).
type DeliveryMode string

const (
	// ModeRecordOnly means WB records the event and reports no live
	// delivery occurred; the calling command does not fail
	// (REQ:none-transport-is-first-class, REQ:no-live-owner-is-record-only).
	ModeRecordOnly DeliveryMode = "record_only"
	// ModeAdvisory means WB pastes unsubmitted text into a live pane
	// (REQ:advisory-mechanism-and-newline-boundary).
	ModeAdvisory DeliveryMode = "advisory"
	// ModeSubmit means WB submits text as if the session's own operator had
	// typed and pressed enter. No shipped [Capabilities] value ever causes
	// [ResolveDeliveryMode] to return this today (Deferred in the
	// Feature).
	ModeSubmit DeliveryMode = "submit"
)

// Delivery is one [Transport.Deliver] call's payload: which session-layer
// Operation this is, at which Mode, carrying which text.
// REQ:facts-only-templates governs what Text may ever contain for a
// daemon-originated event; this package does not compose message text, it
// only carries whatever the caller already built.
type Delivery struct {
	Operation Operation
	Mode      DeliveryMode
	Text      string
}

// Receipt is the honest outcome of one [Transport.Deliver] call. Outcome
// reports what actually happened, which MUST never exceed what the
// transport's declared [Capabilities] allow — a transport must downgrade
// and report [ModeRecordOnly] rather than claim it advised or submitted
// when it did not (REQ:recorded-only-receipt-state's principle, applied
// here to this package's own in-memory Receipt rather than to
// sessionmove.MessageReceipt's wire schema, which Task 4 owns separately).
type Receipt struct {
	Operation Operation
	Outcome   DeliveryMode
	Target    Target
	At        time.Time
}

// ResolveDeliveryMode is the one seam every daemon-originated delivery path
// calls before doing anything to a pane (REQ:transport-capability-matrix,
// REQ:delivery-guards-enforced-and-rechecked). It only ever inspects the
// resolved transport's declared capability matrix and the event's class —
// never a specific transport [Kind] — so no call site branches on
// herdr-vs-tmux directly (REQ:single-transport-interface; Task 3's job is
// to prove no production call site does, this function only proves the
// mechanism itself does not need to).
//
// class == [EventOwnPROutcome] can resolve [ModeSubmit] only when both
// LiveStatus and EmptyInputEvidence are true. No shipped [Capabilities]
// value declares EmptyInputEvidence true today, so this branch is
// unreachable in practice, by construction, not by circumstance —
// AC:guards-are-unconditional-and-block-submission (Task 7's to exercise
// end-to-end against the real guard chain; this function is the rule that
// chain depends on). Absence of the evidence is itself a guard failure: it
// is never treated as grounds to advise instead, because
// REQ:prompt-restricted-to-own-pr-outcome reserves the own-PR-outcome class
// for submit-or-record only, never advisory.
//
// Every other class resolves [ModeAdvisory] only when AdvisoryDelivery is
// true, and [ModeRecordOnly] otherwise — which is why a Capabilities value
// with AdvisoryDelivery false (tmux's default, none's only value) always
// resolves ModeRecordOnly regardless of class
// (AC:tmux-never-delivers-unguarded, AC:none-transport-records-only).
func ResolveDeliveryMode(caps Capabilities, class EventClass) DeliveryMode {
	switch class {
	case EventOwnPROutcome:
		if caps.LiveStatus && caps.EmptyInputEvidence {
			return ModeSubmit
		}
		return ModeRecordOnly
	default:
		if caps.AdvisoryDelivery {
			return ModeAdvisory
		}
		return ModeRecordOnly
	}
}
