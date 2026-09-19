package sessiontransport

import "time"

// Operation names one of the two terminal-addressing responsibilities that
// actually place text into a pane; every other session code path
// REQ:single-transport-interface lists (move, park, receive, successor
// launch) addresses a pane through [Transport.ResolvePane], [Transport.Launch]
// or [Transport.Inspect] instead, and confirms identity through
// [Transport.MatchesReceiptIdentity] — none of them deliver text, so none of
// them is tagged here.
type Operation string

const (
	// OperationMessage is `wb session message`'s and `wb session
	// send`'s/`recall`'s target-side write
	// (REQ:existing-successor-messaging-binding-path). It is the only
	// Operation ever paired with [EventSuccessorMessage].
	OperationMessage Operation = "message"
	// OperationWake is the daemon's own-PR-outcome and advisory wake path
	// (REQ:prompt-restricted-to-own-pr-outcome,
	// REQ:advisory-for-everything-else). It is paired with
	// [EventOwnPROutcome] or [EventAdvisory], never [EventSuccessorMessage].
	OperationWake Operation = "wake"
)

// EventClass distinguishes the two event classes ever eligible for a
// submitted (binding) delivery — the daemon's own-PR-outcome class, and the
// pre-existing, differently guarded successor-messaging path — from every
// other, advisory-or-record-only event.
type EventClass string

const (
	// EventOwnPROutcome is the session's own registered pull request
	// settling — the only daemon-originated event class
	// REQ:prompt-restricted-to-own-pr-outcome ever names as submit-eligible.
	// It never actually resolves [ModeSubmit] in this iteration, because the
	// empty-input evidence guard can never be satisfied
	// (AC:guards-are-unconditional-and-block-submission).
	EventOwnPROutcome EventClass = "own_pr_outcome"

	// EventAdvisory is every other daemon-originated event class
	// (REQ:advisory-for-everything-else). It is never submit-eligible; it
	// resolves either [ModeAdvisory] or [ModeRecordOnly].
	EventAdvisory EventClass = "advisory"

	// EventSuccessorMessage is the pre-existing successor-messaging submit
	// path — `wb session send`'s and `recall`'s target-side write
	// (REQ:existing-successor-messaging-binding-path) — guarded by that
	// receipt's lineage, never by REQ:prompt-restricted-to-own-pr-outcome's
	// own-PR-outcome restriction. It resolves [ModeSubmit] only when the
	// resolved transport declares [Capabilities.LineageSubmit]; otherwise
	// [ModeRecordOnly] (REQ:recorded-only-receipt-state: "recorded is not
	// herdr-specific: any transport... that cannot paste... produces it").
	// It never resolves [ModeAdvisory]: this path has always been a durable
	// record-or-submit decision, never an unsubmitted paste.
	EventSuccessorMessage EventClass = "successor_message"
)

// DeliveryMode is what [ResolveDeliveryMode] decided, and what
// [Transport.Deliver] must honor exactly: [ModeRecordOnly] must never touch
// a live pane, [ModeAdvisory] must never submit, and [ModeSubmit] is
// reserved for [EventSuccessorMessage] on a [Capabilities.LineageSubmit]
// transport (today, only tmux) or for [EventOwnPROutcome] once the Deferred
// empty-input evidence mechanism lands (REQ:prompt-restricted-to-own-pr-outcome).
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
	// typed and pressed enter. For [EventOwnPROutcome], no shipped
	// [Capabilities] value ever causes [ResolveDeliveryMode] to return this
	// today (Deferred in the Feature). For [EventSuccessorMessage], tmux
	// does reach it, matching its pre-existing, unchanged behavior.
	ModeSubmit DeliveryMode = "submit"
)

// Delivery is one [Transport.Deliver] call's payload: which session-layer
// Operation this is, which EventClass governs it, and the text (and, for a
// keyed mechanism, the Key) to carry. There is deliberately no Mode field —
// [Transport.Deliver] recomputes the mode itself from its own
// [Transport.Capabilities] and Class, exactly as [ResolveDeliveryMode]
// would, rather than trusting a caller-supplied mode that could be stale or
// wrong by the time Deliver actually runs.
type Delivery struct {
	Operation Operation
	Class     EventClass
	// Key is the idempotency key a keyed delivery mechanism needs — tmux's
	// buffer name is `wb-message-<Key>` (internal/sessionmessage/receive.go:189,
	// asserted by receive_test.go:97). Required for [OperationMessage];
	// [OperationWake] leaves it empty.
	Key string
	// Text is the exact bytes to deliver.
	// REQ:facts-only-templates governs what Text may ever contain for a
	// daemon-originated event; this package does not compose message text,
	// it only carries whatever the caller already built.
	Text string
}

// Receipt is the honest outcome of one [Transport.Deliver] call. Outcome
// reports what actually happened, which MUST NOT exceed
// ([DeliveryModeExceeds]) what [ResolveDeliveryMode] computes from the
// transport's declared [Capabilities], delivery.Operation and
// delivery.Class — a transport must downgrade and report [ModeRecordOnly]
// rather than claim it advised or submitted when it did not
// (REQ:recorded-only-receipt-state's principle, applied here to this
// package's own in-memory Receipt rather than to
// sessionmove.MessageReceipt's wire schema, which Task 4 owns separately).
// It MAY report a strictly less binding mode than that computed value —
// [Transport.Deliver] documents the one case every implementation shares,
// a zero [Target].
type Receipt struct {
	Operation Operation
	Outcome   DeliveryMode
	Target    Target
	At        time.Time
}

// ResolveDeliveryMode is the one seam every delivery path calls before doing
// anything to a pane (REQ:transport-capability-matrix,
// REQ:delivery-guards-enforced-and-rechecked). It only ever inspects the
// resolved transport's declared capability matrix, the Operation, and the
// event's class — never a specific transport [Kind] — so no call site
// branches on herdr-vs-tmux directly (REQ:single-transport-interface;
// Task 3's job is to prove no production call site does, this function
// only proves the mechanism itself does not need to). [Transport.Deliver]
// implementations call this themselves rather than trusting a
// caller-supplied mode; its result is an upper bound a transport MAY
// downgrade further (a zero [Target], for instance — see
// [Transport.Deliver]) but MUST NOT exceed ([DeliveryModeExceeds]).
//
// Operation matters because a caller can mislabel a [Delivery]: passing
// [OperationWake] with class [EventSuccessorMessage], for instance, must
// never inherit [EventSuccessorMessage]'s submit path — that path belongs
// only to [OperationMessage]. Each class therefore has exactly one
// Operation it is ever evaluated for; every other Operation forces
// [ModeRecordOnly] regardless of capabilities, before any capability is
// even consulted.
//
// [EventOwnPROutcome] (only ever evaluated for [OperationWake]) can resolve
// [ModeSubmit] only when both LiveStatus and EmptyInputEvidence are true.
// No shipped [Capabilities] value declares EmptyInputEvidence true today,
// so this branch is unreachable in practice, by construction, not by
// circumstance — AC:guards-are-unconditional-and-block-submission. Absence
// of the evidence is itself a guard failure: it is never treated as
// grounds to advise instead, because
// REQ:prompt-restricted-to-own-pr-outcome reserves the own-PR-outcome
// class for submit-or-record only, never advisory.
//
// [EventSuccessorMessage] (only ever evaluated for [OperationMessage]) can
// resolve [ModeSubmit] only when LineageSubmit is true — never because of
// LiveStatus, EmptyInputEvidence, or AdvisoryDelivery, which govern only
// the daemon-wake classes. A transport that declares LineageSubmit true
// (tmux) still never reaches ModeSubmit for [EventOwnPROutcome] or
// [EventAdvisory]: LineageSubmit is consulted in exactly one branch, this
// one, and only when Operation is OperationMessage
// (AC:tmux-never-delivers-unguarded's guarantee holds regardless of
// LineageSubmit, and regardless of a caller mislabeling Operation).
//
// [EventAdvisory] (only ever evaluated for [OperationWake]) resolves
// [ModeAdvisory] only when AdvisoryDelivery is true, and [ModeRecordOnly]
// otherwise — which is why a Capabilities value with AdvisoryDelivery
// false (tmux's default, none's only value) always resolves ModeRecordOnly
// for it (AC:tmux-never-delivers-unguarded, AC:none-transport-records-only).
//
// Any other, unrecognized, or empty EventClass resolves [ModeRecordOnly]:
// an unrecognized class is never grounds to advise or submit, only to
// record.
func ResolveDeliveryMode(caps Capabilities, operation Operation, class EventClass) DeliveryMode {
	switch class {
	case EventOwnPROutcome:
		if operation != OperationWake {
			return ModeRecordOnly
		}
		if caps.LiveStatus && caps.EmptyInputEvidence {
			return ModeSubmit
		}
		return ModeRecordOnly
	case EventSuccessorMessage:
		if operation != OperationMessage {
			return ModeRecordOnly
		}
		if caps.LineageSubmit {
			return ModeSubmit
		}
		return ModeRecordOnly
	case EventAdvisory:
		if operation != OperationWake {
			return ModeRecordOnly
		}
		if caps.AdvisoryDelivery {
			return ModeAdvisory
		}
		return ModeRecordOnly
	default:
		return ModeRecordOnly
	}
}

// deliveryModeRank orders [DeliveryMode] values from least to most binding.
// It is the table [DeliveryModeExceeds] compares against; a mode absent
// from it (never expected in practice) ranks below every real mode, so an
// unrecognized value is treated as strictly less binding, never more.
var deliveryModeRank = map[DeliveryMode]int{
	ModeRecordOnly: 0,
	ModeAdvisory:   1,
	ModeSubmit:     2,
}

// DeliveryModeExceeds reports whether got is a strictly more binding
// delivery mode than allowed — submit exceeds advisory and record-only;
// advisory exceeds record-only; nothing exceeds submit. It is exported so
// a contract test (internal/sessiontransport/transporttest) can assert
// that a real [Transport.Deliver] outcome never exceeds what
// [ResolveDeliveryMode] computed, while still permitting a transport to
// downgrade further for its own additional reasons — a zero [Target], most
// importantly, which has nowhere to advise or submit into regardless of
// what the capability matrix alone would otherwise allow.
func DeliveryModeExceeds(got, allowed DeliveryMode) bool {
	return deliveryModeRank[got] > deliveryModeRank[allowed]
}
