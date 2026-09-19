package sessiontransport

import (
	"context"
	"errors"
)

// Target names a resolved, live terminal endpoint. Every field is exactly
// what a real call site (internal/sessionlaunch, internal/sessionmessage)
// needs to address it — not an opaque bag — because the two shipped
// transports address panes in genuinely different, concrete ways: herdr by
// a socket-scoped pane ID, tmux by a session name plus a separate
// buffer-target pane ID and a PID it corroborates against a WB session
// record. A field is empty when that transport does not use it.
type Target struct {
	Kind Kind
	// ID is the transport-native pane identifier used to address a live
	// send call: herdr's pane ID (e.g. "w1:p2") for `pane send-text`, or
	// tmux's own pane ID (e.g. "%7") for `paste-buffer -t` — see
	// internal/sessionmessage/tmux.go's Pane.ID, PasteBuffer's paneID
	// argument.
	ID string
	// Name is tmux's session name, "wb-session-" + a WB session ID
	// (internal/sessionlaunch/launch.go:482,
	// internal/sessionmessage/receive.go's handoffReceipt.TmuxName). Empty
	// on herdr and none, which have no equivalent deterministic name.
	Name string
	// PID is the resolved target's live process ID — tmux's
	// list-panes `#{pane_pid}` (internal/sessionlaunch/tmux.go's PanePID,
	// internal/sessionmessage/tmux.go's Pane.PID). Zero when unknown.
	PID int
	// Socket is the herdr socket path this Target's ID is scoped to —
	// herdr's IDs are scoped to one server. Empty on tmux and none.
	Socket string
}

// IsZero reports whether t names no live endpoint at all — the honest shape
// of "no live owner" (REQ:no-live-owner-is-record-only) and of "resolution
// failed" before [ErrNoUniqueTarget] is even considered.
func (t Target) IsZero() bool {
	return t == Target{}
}

// Identity is what a session recorded about its terminal identity at
// registration time, re-walked at delivery time
// (REQ:resolution-at-delivery). Task 5 owns capturing and persisting this on
// the session record; this package only defines the shape
// [Transport.ResolvePane] consumes.
type Identity struct {
	Kind Kind
	// WBSessionID is this session's own opaque WB identity (session.NewID).
	// tmux's ResolvePane resolves this to a live pane by its deterministic
	// session-name convention, "wb-session-" + WBSessionID
	// (internal/sessionlaunch/launch.go:482); [Transport.MatchesReceiptIdentity]
	// checks the identical convention against a receipt-carried name.
	WBSessionID string
	// HarnessSessionID is the harness's own session identifier —
	// CLAUDE_CODE_SESSION_ID for Claude Code — the value herdr's
	// ResolvePane must match uniquely against a live candidate's
	// `agent_session` (REQ:pane-resolved-by-session-identity-match). Empty
	// on tmux and none, which have no such live-status API to match
	// against.
	HarnessSessionID string
	// Socket is the herdr socket path this identity's coordinates are
	// scoped to (herdr's IDs are scoped to one server). Empty on tmux and
	// none.
	Socket string
}

// LaunchRequest is what a successor launch needs to start a new terminal
// for a resumed or moved session. Cwd, Executable and Args mirror tmux's
// own StartDetached(ctx, name, cwd, executable, args)
// (internal/sessionlaunch/tmux.go) exactly, because that is the one
// concrete launch mechanism this Feature moves behind the interface today;
// herdr's Task 4 launch (if any) documents its own use of these same
// fields, or extends this type when it genuinely needs something tmux
// never did — not before that need is concrete.
//
// There is deliberately no Name field: the terminal's own name is
// SuccessorWBSessionID run through [Transport.AddressFor] — the identical
// derivation [MatchesReceiptIdentity] checks a receipt against — so
// [Transport.Launch] derives it itself rather than trusting a
// caller-supplied name that could disagree with AddressFor's own
// convention.
type LaunchRequest struct {
	SuccessorWBSessionID string
	// Cwd is the working directory the successor's process starts in.
	Cwd string
	// Executable is the absolute path to the process to start.
	Executable string
	// Args are the executable's own arguments, in order.
	Args []string
}

// Inspection is what [Transport.Inspect] learns about a [Target]: whether
// it is live, and when it is not, whatever terminal evidence the transport
// can find. It replaces tmux's separate PanePID (live/PID probe) and
// PaneFailure (dead/exit-status/diagnostic probe)
// (internal/sessionlaunch/tmux.go) with the one question every poll loop
// actually asks: is it live, and if not, why not.
type Inspection struct {
	// Live reports whether the target's process is running right now. PID
	// is meaningful only when Live is true.
	Live bool
	PID  int
	// Terminated reports whether the transport found terminal evidence — a
	// dead-but-still-inspectable pane, on tmux — distinct from the target
	// never having existed, or already having been cleaned up. ExitStatus
	// and Diagnostic are meaningful only when Terminated is true.
	Terminated bool
	ExitStatus int
	Diagnostic string
}

// ErrNoUniqueTarget means [Transport.ResolvePane] found zero, or more than
// one, live candidate for the given [Identity] — herdr's zero-or-multiple
// `agent list` match (REQ:pane-resolved-by-session-identity-match), or
// tmux's `list-panes` returning other than exactly one line
// (internal/sessionmessage/tmux.go's Inspect: "want exactly one").
//
// Scope: this governs daemon-wake resolution (Task 7's
// EventOwnPROutcome/EventAdvisory delivery). A daemon-wake caller MUST
// treat it exactly like a no-live-owner outcome — resolve to record-only,
// never guess among candidates, and never fall back to a stale previously
// recorded target (AC:pane-resolved-uniquely-or-record-only).
//
// It does NOT retroactively relax `wb session receive-message`'s
// pre-existing successor-messaging behavior
// (REQ:existing-successor-messaging-binding-path,
// internal/sessionmessage/receive.go): today, a pane-count mismatch or a
// paste failure there is a hard error, exactly as REQ:tmux-parity-preserved
// requires, because today's MessageReceipt schema has no way to express a
// non-fatal "recorded, not delivered" outcome. Only once Task 4's
// `delivery: recorded` receipt state (REQ:recorded-only-receipt-state)
// exists does an EventSuccessorMessage resolution failure stop being a
// hard error and start following this same record-only rule.
var ErrNoUniqueTarget = errors.New("sessiontransport: no unique live target for this identity")

// Transport is the one Go interface REQ:single-transport-interface
// requires: every session code path that addresses a terminal — session
// messaging, move/park/resume delivery's identity confirmation, successor
// launch, and the daemon wake's record/advisory/submit path — goes through
// it, so no call site ever branches on herdr-vs-tmux directly. Transport
// selection (automatic, Task 5; explicit override, [ResolveOverride]) is
// the only place a [Kind] is chosen.
//
// Task 2 ships this interface, [NoneTransport] as its first shipped
// implementation, and internal/sessiontransport/transporttest.Suite as the
// shared contract test both remaining implementations must pass
// (REQ:two-transport-implementations, AC:two-implementations-satisfy-the-same-interface).
// Task 3 moves today's tmux code behind it; Task 4 wires herdr onto the
// internal/herdr adapter.
type Transport interface {
	// Kind reports which shipped transport this is. It exists for logging
	// and for a Capabilities().Kind cross-check, never for a caller to
	// branch on — that would violate REQ:single-transport-interface.
	Kind() Kind

	// Capabilities reports this transport's declared, fixed capability
	// matrix (REQ:transport-capability-matrix). An implementation returns a
	// constant value; [ResolveDeliveryMode] is the only place that
	// interprets it into a delivery decision.
	Capabilities() Capabilities

	// ResolvePane resolves identity to a live [Target]. It MUST return
	// [ErrNoUniqueTarget] — never guess, and never fall back to a stale
	// recorded target — when zero or more than one live candidate matches
	// (REQ:pane-resolved-by-session-identity-match). See
	// [ErrNoUniqueTarget]'s own doc for exactly which callers must treat
	// that as record-only today, and which pre-existing caller does not yet.
	ResolvePane(ctx context.Context, identity Identity) (Target, error)

	// Launch starts a brand-new terminal for a successor session and
	// returns the [Target] it resolves to (internal/sessionlaunch's
	// StartDetached, moved behind this method by Task 3). It derives the
	// terminal's own name itself, via
	// AddressFor(request.SuccessorWBSessionID) — see [LaunchRequest] for
	// why that field does not exist on the request instead.
	Launch(ctx context.Context, request LaunchRequest) (Target, error)

	// Inspect reports target's current liveness and, when it is not live,
	// whatever terminal evidence is available — replacing tmux's separate
	// PanePID/PaneFailure poll (internal/sessionlaunch's waitReady,
	// waitExecSuccess, selectAttemptForStart, failedAttemptRetryable,
	// validateAbandonment all poll this repeatedly).
	Inspect(ctx context.Context, target Target) (Inspection, error)

	// AddressFor returns the deterministic address this transport would use
	// to reach wbSessionID's live terminal, in this transport's own naming
	// scheme. tmux's is "wb-session-" + wbSessionID
	// (internal/sessionlaunch/launch.go:482,
	// internal/sessionmove/route.go:428,
	// internal/sessionmove/store.go:521,
	// internal/sessionpark/protocol.go:167,
	// internal/sessioncourier/receiver.go:121,
	// internal/sessioncourier/message.go:106,
	// internal/worktrees/session_custody.go,
	// internal/worktrees/session_park_custody.go — every one of those
	// builds or checks the identical "wb-session-"+id string today; Task 3
	// routes them all through this one method instead). [Transport.Launch]
	// derives a new terminal's own name by calling this itself, rather than
	// accepting a name from its caller (see [LaunchRequest]). herdr and
	// none have no address derivable purely from a WB session ID — herdr
	// resolves a live pane by `agent_session` match instead
	// (REQ:pane-resolved-by-session-identity-match) — so they return "".
	AddressFor(wbSessionID string) string

	// Deliver sends delivery to target. It recomputes the delivery mode
	// itself from Capabilities(), delivery.Operation and delivery.Class via
	// [ResolveDeliveryMode] — it MUST NOT trust a caller-supplied mode,
	// because none exists on [Delivery] to trust: the resolved mode could
	// otherwise go stale between a caller's own check and this call.
	// [ModeRecordOnly] MUST NOT touch a live pane at all, [ModeAdvisory]
	// MUST NOT submit (REQ:advisory-mechanism-and-newline-boundary governs
	// herdr's exact mechanism), and [ModeSubmit] is reachable only for
	// [EventSuccessorMessage] paired with [OperationMessage] on a transport
	// declaring [Capabilities.LineageSubmit] (tmux, unchanged,
	// REQ:tmux-parity-preserved) or, once Deferred work lands, for
	// [EventOwnPROutcome] paired with [OperationWake].
	//
	// The returned [Receipt] reports the mode that was actually used, which
	// MUST NOT exceed ([DeliveryModeExceeds]) what [ResolveDeliveryMode]
	// computed — a transport MUST NOT claim to have advised or submitted
	// when its own declared capabilities (or the Operation/Class pairing)
	// forbid it (REQ:none-transport-is-first-class's "a supported, expected
	// outcome... instead of failing the calling command" generalizes to
	// every transport through this contract, not only none). It MAY report
	// a strictly less binding mode: whenever target [Target.IsZero] is
	// true — there is nowhere to advise or submit into — Outcome MUST be
	// [ModeRecordOnly] regardless of what [ResolveDeliveryMode] computed.
	Deliver(ctx context.Context, target Target, delivery Delivery) (Receipt, error)
}

// MatchesReceiptIdentity reports whether name — a receipt-carried address —
// is the exact address transport would use for successorWBSessionID:
// name == transport.AddressFor(successorWBSessionID). It is a package-level
// helper, not a Transport method, because [Transport.AddressFor] alone
// already carries every transport-specific fact this comparison needs;
// every implementation would otherwise repeat the identical one-line body.
func MatchesReceiptIdentity(transport Transport, successorWBSessionID, name string) bool {
	return name == transport.AddressFor(successorWBSessionID)
}
