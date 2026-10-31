// Package sessiontransport defines the generic terminal capability-policy
// contract and record/advisory/submit guards. The active kind set is Herdr and
// none; legacy tmux receipt names remain immutable historical data only.
//
// NoneTransport is the record-only implementation. The production Herdr
// launcher and successor-message paths use bounded typed clients in their own
// packages; this generic interface is not a claim that all lifecycle adapters
// implement it. Original Herdr TUI 0.9.1/protocol 22 is pinned by those clients.
//
// ResolveDeliveryMode requires operation/class-specific authority. LineageSubmit
// permits explicit receipt-bound successor messages only. Daemon own-PR wake
// submission still requires positive idle and empty-input evidence; lack of an
// empty-input signal never becomes permission to submit. Literal advisory text
// remains separate from actionable prompts. None never touches a terminal.
//
// ResolveOverride accepts only shipped kinds and fails closed on unsupported or
// unavailable selections. No active tmux prerequisite, override or fallback is
// provided. The shared transporttest suite exercises this generic contract.
//
// The daemon watcher, read-inbox/hook extensions and empty-input detection in
// the Feature remain deferred. This package does not implement those services.
package sessiontransport
