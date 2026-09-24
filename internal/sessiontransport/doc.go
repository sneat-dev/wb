// Package sessiontransport defines the one Go transport interface WB's
// session layer addresses a terminal through (REQ:single-transport-interface),
// the typed capability matrix each implementation declares
// (REQ:transport-capability-matrix), and the capability-driven guard that
// decides record-only vs. advisory vs. submit delivery for every event
// class, daemon-originated or the pre-existing successor-messaging path
// alike (REQ:delivery-guards-enforced-and-rechecked). See
// spec/features/herdr-session-transport/README.md and
// spec/plans/herdr-session-transport.md (Task 2) for the Feature and Plan
// this package implements.
//
// # What this package ships
//
// [Transport] is the interface every session code path that addresses a
// terminal must go through: [Transport.ResolvePane] and [Transport.Launch]
// find or start a live [Target], [Transport.Inspect] polls its liveness,
// [Transport.MatchesReceiptIdentity] confirms a receipt-carried name
// against the transport's own naming convention, and [Transport.Deliver]
// sends text at whatever [DeliveryMode] its own [Capabilities] and the
// delivery's [EventClass] resolve to — recomputed inside Deliver itself,
// never trusted from a caller. [Capabilities] is the typed matrix an
// implementation declares, including [Capabilities.LineageSubmit], the
// pre-existing successor-messaging submit path's own capability, distinct
// from the daemon-wake capabilities. [NoneTransport] is the first shipped
// implementation (REQ:none-transport-is-first-class). [ResolveDeliveryMode]
// is the guard every delivery path calls before touching a pane, encoding
// two rules: absence of empty-input evidence is itself a guard failure for
// the daemon's own-PR-outcome class, never grounds to submit
// (REQ:delivery-guards-enforced-and-rechecked); and LineageSubmit is
// consulted only for [EventSuccessorMessage], never for a daemon-originated
// class, so a transport claiming it (tmux) still never delivers a daemon
// event unguarded (AC:tmux-never-delivers-unguarded).
// [ResolveOverride] and [LoadOverride] are the explicit `wb.yaml`
// `session.transport` / command-flag override and its fail-closed
// validation (REQ:explicit-transport-override), applying a default
// per-[Kind] prerequisite check even when the caller passes no check of its
// own. internal/sessiontransport/transporttest.Suite is the shared contract
// test both remaining implementations must pass
// (REQ:two-transport-implementations); it lives in its own package so this
// one never carries a "testing" dependency into production code.
//
// # What this package does not ship
//
// No production call site is rewired onto [Transport] yet. Task 3 moves
// today's tmux code (internal/sessionlaunch, internal/sessionmessage, and
// related packages) behind this interface with no behavior change; Task 4
// wires the herdr implementation onto internal/herdr (Task 1, already
// landed). Automatic transport selection
// (REQ:automatic-transport-selection), identity capture
// (REQ:automatic-transport-identity-capture), and persisting the resolved
// Kind on a session record (REQ:selected-transport-recorded) are Task 5's.
// The delivery-intent coalescing store, the watcher, and the facts-only
// templates are Tasks 6-9's. This package carries no delivery policy beyond
// the capability-driven mode decision itself: no message templates, no
// watcher, no persistence.
//
// # Capability declarations are documentation, not discovery
//
// [HerdrCapabilities] and [TmuxCapabilities] describe what Task 4's and
// Task 3's implementations will declare once built. This package states
// them now so [ResolveDeliveryMode] and the contract suite have something
// concrete to prove the mechanism against ahead of that wiring — see
// AC:tmux-never-delivers-unguarded and AC:none-transport-records-only,
// which this package's own tests exercise directly. Once Task 3 or Task 4
// lands, its own [Transport.Capabilities] method is the live source of
// truth, not these package-level functions; a future change to one should
// be paired with the implementation it documents. They are functions, not
// exported vars, so nothing outside this package can corrupt the shared
// declaration for the rest of the process.
package sessiontransport
