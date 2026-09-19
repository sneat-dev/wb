package sessiontransport

import (
	"context"
	"errors"
)

// Target names a resolved, live terminal endpoint within one transport's own
// addressing scheme (a herdr pane ID, a tmux pane name) — opaque to every
// caller above this package, exactly as REQ:single-transport-interface
// requires: nothing above [Transport] branches on what a Target actually
// names.
type Target struct {
	Kind Kind
	// ID is the transport-native identifier: a herdr pane ID, a tmux pane
	// name. It is meaningless outside the Kind that produced it.
	ID string
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
// [Transport.ResolvePane] consumes, so pane resolution never needs to know
// the session-record schema directly.
type Identity struct {
	Kind Kind
	// HarnessSessionID is the harness's own session identifier — the value
	// [Transport.ResolvePane] must match uniquely against a live candidate
	// (REQ:pane-resolved-by-session-identity-match's `agent_session` match
	// on herdr).
	HarnessSessionID string
	// Coordinates carries whatever transport-specific addressing
	// information a ResolvePane implementation needs beyond
	// HarnessSessionID — herdr's socket path, for instance. Kept as a map
	// so this package never needs to know every transport's own coordinate
	// shape; each [Transport] implementation documents the keys it reads.
	Coordinates map[string]string
}

// LaunchRequest is what a successor launch needs to start or address a new
// terminal for a resumed or moved session. Task 3 and Task 4 fill in
// whatever coordinates their own launch mechanism (tmux new-session, a
// herdr pane) actually requires; this package only fixes the seam every
// call site goes through.
type LaunchRequest struct {
	SuccessorWBSessionID string
	Coordinates          map[string]string
}

// ErrNoUniqueTarget means [Transport.ResolvePane] found zero, or more than
// one, live candidate for the given [Identity]. Callers MUST treat this
// exactly like a no-live-owner outcome: resolve to record-only, never guess
// among candidates, and never fall back to a stale previously recorded
// target (REQ:pane-resolved-by-session-identity-match,
// AC:pane-resolved-uniquely-or-record-only).
var ErrNoUniqueTarget = errors.New("sessiontransport: no unique live target for this identity")

// Transport is the one Go interface REQ:single-transport-interface
// requires: every session code path that addresses a terminal — session
// messaging, move and park/resume delivery, successor launch, the courier
// delivery boundary, and the daemon wake's record/advisory/submit path —
// goes through it, so no call site ever branches on herdr-vs-tmux directly.
// Transport selection (automatic, Task 5; explicit override,
// [ResolveOverride]) is the only place a [Kind] is chosen.
//
// Task 2 ships this interface, [NoneTransport] as its first shipped
// implementation, and [Suite] as the shared contract test both remaining
// implementations must pass (REQ:two-transport-implementations,
// AC:two-implementations-satisfy-the-same-interface). Task 3 moves today's
// tmux code behind it; Task 4 wires herdr onto the internal/herdr adapter.
// Neither task should need to change this interface to do so; if either
// legitimately does, that is a fact for that task's report, not something
// to route around silently.
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
	// (REQ:pane-resolved-by-session-identity-match). "No live owner" and
	// "ambiguous" both resolve to ErrNoUniqueTarget; the caller's job
	// (Task 7) is to treat that as record-only.
	ResolvePane(ctx context.Context, identity Identity) (Target, error)

	// Launch starts or addresses a brand-new terminal for a successor
	// session and returns the [Target] it resolves to.
	Launch(ctx context.Context, request LaunchRequest) (Target, error)

	// Deliver sends delivery to target, honoring delivery.Mode exactly:
	// [ModeRecordOnly] MUST NOT touch a live pane at all, [ModeAdvisory]
	// MUST NOT submit (REQ:advisory-mechanism-and-newline-boundary governs
	// herdr's exact mechanism), and [ModeSubmit] is reserved for the path
	// this Feature specifies but never exercises yet
	// (REQ:prompt-restricted-to-own-pr-outcome). A transport whose declared
	// [Capabilities] cannot honor the requested mode MUST downgrade to the
	// safer outcome and report it honestly in the returned [Receipt] — it
	// MUST NOT error. REQ:none-transport-is-first-class's "a supported,
	// expected outcome... instead of failing the calling command"
	// generalizes to every transport through this contract, not only none.
	Deliver(ctx context.Context, target Target, delivery Delivery) (Receipt, error)
}
