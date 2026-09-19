// Package herdr is a thin, mechanical adapter over the herdr CLI
// (https://herdr.dev), the terminal workspace manager that hosts the
// founder's live agent panes. It answers two questions any later caller
// needs — "am I running inside herdr, and under which harness session?" and
// "what does the herdr socket API say about panes and agents?" — and does
// nothing else.
//
// This package carries no delivery policy: no decision about who may be
// woken, no message templates, no authority rules. See
// spec/ideas/daemon-as-coordinator.md for why those questions exist and how
// later work answers them; this package is only the foundation they build
// on.
//
// # Identity
//
// [ReadIdentity] reads the herdr and harness environment variables a
// process inherits when it runs inside a herdr-managed pane, through an
// injected [EnvLookup] so tests never depend on the real environment.
// [Identity.InHerdr] reports whether the process is inside herdr at all.
//
// # Client
//
// [NewClient] resolves the herdr binary — HERDR_BIN_PATH first, then PATH —
// and returns a [Client] that shells out to it for every call, always as
// argv (never through a shell), always under a bounded per-call timeout.
// Every [Client] method takes a context.Context in addition to that
// timeout, so a caller can cancel a call early.
//
// [Client.AgentPrompt], [Client.AgentSendKeys] and [Client.PaneSendText]
// are the commands that can affect a live session (see "the newline is the
// authority boundary" in the idea above); this package validates their
// arguments defensively but makes no decision about when it is safe to
// call them. [Client.AgentRead] and [Client.PaneCurrent]/[Client.PaneList]/
// [Client.PaneGet]/[Client.AgentList]/[Client.AgentGet]/[Client.AgentWait]
// only ever read.
//
// herdr's own agent and pane IDs are scoped to one server (herdr --skill:
// "IDs and live agent names are scoped to one server"). A caller that must
// reach a specific session's herdr — a daemon coordinating several, for
// instance — configures that explicitly with [WithSocketPath] and/or
// [WithSessionName] rather than relying on whichever HERDR_SOCKET_PATH the
// calling process's own ambient environment happens to carry.
//
// # Errors
//
// Failures are reported as one of the sentinel errors declared in
// errors.go ([ErrBinaryNotFound], [ErrServerUnreachable],
// [ErrUnknownTarget], [ErrUnsupportedVersion], [ErrUnparseableOutput]),
// wrapped with context via fmt.Errorf's %w so callers can match with
// errors.Is. Malformed or unexpected herdr output is always reported as
// [ErrUnparseableOutput]; this package never panics on it.
package herdr
