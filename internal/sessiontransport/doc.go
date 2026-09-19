// Package sessiontransport defines the one Go transport interface WB's
// session layer addresses a terminal through (REQ:single-transport-interface),
// the typed capability matrix each implementation declares
// (REQ:transport-capability-matrix), and the capability-driven guard that
// decides record-only vs. advisory vs. submit delivery for a
// daemon-originated event (REQ:delivery-guards-enforced-and-rechecked). See
// spec/features/herdr-session-transport/README.md and
// spec/plans/herdr-session-transport.md (Task 2) for the Feature and Plan
// this package implements.
//
// # What this package ships
//
// [Transport] is the interface every session code path that addresses a
// terminal must go through. [Capabilities] is the typed matrix an
// implementation declares. [NoneTransport] is the first shipped
// implementation (REQ:none-transport-is-first-class). [ResolveDeliveryMode]
// is the guard every daemon-originated delivery path calls before touching
// a pane, encoding the rule that absence of empty-input evidence is itself
// a guard failure, never grounds to submit
// (REQ:delivery-guards-enforced-and-rechecked). [ResolveOverride] and
// [LoadOverride] are the explicit `wb.yaml` `session.transport` /
// command-flag override and its fail-closed validation
// (REQ:explicit-transport-override). [Suite] is the shared contract test
// both remaining implementations must pass
// (REQ:two-transport-implementations).
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
// them now so [ResolveDeliveryMode] and [Suite] have something concrete to
// prove the mechanism against ahead of that wiring — see
// AC:tmux-never-delivers-unguarded and AC:none-transport-records-only,
// which this package's own tests exercise directly. Once Task 3 or Task 4
// lands, its own [Transport.Capabilities] method is the live source of
// truth, not these package-level values; a future change to a value here
// should be paired with the implementation it documents.
package sessiontransport
