package herdr

// EnvLookup reads one environment variable, reporting whether it was set at
// all (as os.LookupEnv does). Identity is read through this seam so tests
// never depend on, and never risk touching, the founder's real herdr
// environment.
type EnvLookup func(key string) (string, bool)

// Identity describes the herdr and coding-agent-harness environment a
// process inherits. Every field is read once, through the injected
// [EnvLookup], and never re-read from the live environment.
type Identity struct {
	// PaneID, WorkspaceID, TabID and SocketPath are herdr's own coordinates
	// for the pane hosting this process. They are empty outside herdr.
	PaneID      string
	WorkspaceID string
	TabID       string
	SocketPath  string

	// BinPath is HERDR_BIN_PATH, herdr's own hint for where its CLI binary
	// lives. It is empty when herdr did not set it, in which case a Client
	// falls back to PATH.
	BinPath string

	// Harness names the coding-agent harness this process believes it is
	// running under, such as "claude-code". It is empty when no known
	// harness environment variable was found.
	Harness string

	// HarnessSessionID is the harness's own session identifier —
	// CLAUDE_CODE_SESSION_ID for Claude Code — or empty when the harness
	// does not expose one, or none was found.
	HarnessSessionID string

	// HarnessPID is the harness's own process identifier — CLAUDE_PID for
	// Claude Code — as reported, or empty when not set. It is kept as a
	// string because it identifies a harness process, not a herdr count.
	HarnessPID string
}

// herdrEnvVar names the herdr-set environment variables ReadIdentity reads.
// Keeping them as a table (rather than inline lookup calls) makes it
// obvious, at the definition, that only these five are read — and keeps
// identity_test.go's coverage of "every field" mechanically checkable
// against this same list.
const (
	envPaneID      = "HERDR_PANE_ID"
	envWorkspaceID = "HERDR_WORKSPACE_ID"
	envTabID       = "HERDR_TAB_ID"
	envSocketPath  = "HERDR_SOCKET_PATH"
	envBinPath     = "HERDR_BIN_PATH"

	// envClaudeCodeSessionID and envClaudePID are Claude Code's own
	// environment variables, verified present in a live Claude Code
	// session running inside herdr (2026-09-19). No Codex equivalent is
	// documented in `codex --help` or its subcommands' help text as of
	// that same check, so none is read here; add one only once a Codex
	// release documents it.
	envClaudeCodeSessionID = "CLAUDE_CODE_SESSION_ID"
	envClaudePID           = "CLAUDE_PID"

	// HarnessClaudeCode is the [Identity.Harness] value reported when
	// envClaudeCodeSessionID is set.
	HarnessClaudeCode = "claude-code"
)

// OSLookupEnv is the production [EnvLookup]: os.LookupEnv. Production code
// passes it to [ReadIdentity] and [NewClient]; tests pass a map-backed
// lookup instead so they never read, and never risk depending on, the
// caller's real environment.
func OSLookupEnv(key string) (string, bool) {
	return osLookupEnv(key)
}

// ReadIdentity reads the herdr and harness environment through lookup and
// returns the resulting [Identity]. lookup must not be nil.
func ReadIdentity(lookup EnvLookup) Identity {
	id := Identity{
		PaneID:      valueOrEmpty(lookup, envPaneID),
		WorkspaceID: valueOrEmpty(lookup, envWorkspaceID),
		TabID:       valueOrEmpty(lookup, envTabID),
		SocketPath:  valueOrEmpty(lookup, envSocketPath),
		BinPath:     valueOrEmpty(lookup, envBinPath),
	}
	if sessionID, ok := lookup(envClaudeCodeSessionID); ok && sessionID != "" {
		id.Harness = HarnessClaudeCode
		id.HarnessSessionID = sessionID
		id.HarnessPID = valueOrEmpty(lookup, envClaudePID)
	}
	return id
}

// InHerdr reports whether this Identity was read inside a herdr-managed
// pane. HERDR_PANE_ID is the one variable herdr sets on every pane it
// hosts, so its presence is the check.
func (id Identity) InHerdr() bool {
	return id.PaneID != ""
}

func valueOrEmpty(lookup EnvLookup, key string) string {
	value, ok := lookup(key)
	if !ok {
		return ""
	}
	return value
}
