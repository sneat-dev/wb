package herdr

import "errors"

// Sentinel errors this package returns. Every failure path wraps one of
// these with fmt.Errorf's %w, so callers match with errors.Is rather than
// string comparison. New sentinels should be added here, never inferred
// from herdr's own JSON error codes at the call site.
var (
	// ErrBinaryNotFound means the herdr binary could not be resolved, from
	// either HERDR_BIN_PATH or PATH.
	ErrBinaryNotFound = errors.New("herdr: binary not found")

	// ErrServerUnreachable means the herdr socket could not be reached, or
	// no herdr server is running behind it.
	ErrServerUnreachable = errors.New("herdr: socket unreachable or server not running")

	// ErrUnknownTarget means herdr rejected a pane, agent, workspace or tab
	// target because it does not (or no longer) exists.
	ErrUnknownTarget = errors.New("herdr: unknown target")

	// ErrUnsupportedVersion means the resolved herdr binary reports a
	// version older than [MinimumVersion].
	ErrUnsupportedVersion = errors.New("herdr: unsupported version")

	// ErrUnparseableOutput means herdr's output did not match the shape
	// this package expects. It is returned instead of panicking whenever
	// parsing fails.
	ErrUnparseableOutput = errors.New("herdr: unparseable output")

	// ErrCommandFailed means herdr reported a structured error this
	// package does not otherwise recognize (see herdrError.errorCode in
	// client.go for the recognized set). The underlying herdr message is
	// preserved in the wrapping error text.
	ErrCommandFailed = errors.New("herdr: command failed")

	// ErrInvalidPromptText means text passed to [Client.AgentPrompt] failed
	// the newline/control-character check described in doc.go and in
	// spec/ideas/daemon-as-coordinator.md's "the newline is the authority
	// boundary" section: herdr's own newline submits the prompt, so this
	// package refuses to let a caller smuggle one in through the text
	// argument.
	ErrInvalidPromptText = errors.New("herdr: invalid prompt text")

	// ErrInvalidKeyName means a key name passed to [Client.AgentSendKeys]
	// was empty or contained characters no herdr key name uses.
	ErrInvalidKeyName = errors.New("herdr: invalid key name")

	// ErrCurrentUnavailable means [Client.PaneCurrent] was called on a
	// Client configured with [WithSocketPath] or [WithSessionName].
	// "current" resolves from the calling process's own ambient
	// HERDR_PANE_ID, which is meaningless once a Client explicitly targets
	// a different socket or session; call [Client.PaneGet] with a known
	// pane id instead.
	ErrCurrentUnavailable = errors.New("herdr: pane current is unavailable on an explicitly targeted client")
)
