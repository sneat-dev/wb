package herdr

// AgentStatus is one of herdr's agent lifecycle states, as reported by
// `herdr agent list`, `herdr agent get` and `herdr agent wait`.
type AgentStatus string

// The five states herdr's --skill documentation and live `agent list`/`get`
// output use. "idle" and "done" both mean the agent is ready for input;
// herdr distinguishes them by whether the completion has been seen, which
// this package does not interpret further.
const (
	StatusIdle    AgentStatus = "idle"
	StatusWorking AgentStatus = "working"
	StatusBlocked AgentStatus = "blocked"
	StatusDone    AgentStatus = "done"
	StatusUnknown AgentStatus = "unknown"
)

// AgentSession identifies the coding-agent session herdr has associated
// with a pane, as herdr itself reports it — not this package's own
// [Identity]. Observed live: {"agent":"claude","kind":"id","source":
// "herdr:claude","value":"<CLAUDE_CODE_SESSION_ID>"}.
type AgentSession struct {
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Value  string `json:"value"`
}

// HarnessSessionID returns the underlying harness session identifier when
// herdr reports one by id (Kind == "id"), or "" when session is nil or
// herdr identified the session some other way.
func (session *AgentSession) HarnessSessionID() string {
	if session == nil || session.Kind != "id" {
		return ""
	}
	return session.Value
}

// Pane describes one herdr pane, as `pane current`, `pane list` and
// `pane get` report it. Field set verified against herdr's own socket API
// schema (`herdr api schema --json`, read-only): PaneInfo's fields not
// listed here — label, title, display_agent, state_labels, tokens, scroll,
// terminal_title_stripped — are not yet exposed; add them when a caller
// needs them.
type Pane struct {
	PaneID        string
	WorkspaceID   string
	TabID         string
	AgentKind     string
	AgentStatus   AgentStatus
	AgentSession  *AgentSession
	CWD           string
	ForegroundCWD string
	Focused       bool
	TerminalID    string
	TerminalTitle string
	Revision      uint64
}

// Agent describes one live agent, as `agent list`, `agent get` and
// `agent wait` report it. It carries the pane, workspace and harness
// session identity a caller needs to target or recognize the agent again,
// plus StateChangeSeq so a caller can confirm nothing changed between an
// observation and a later action (e.g. the wake flow's check-then-send).
// Field set verified against AgentInfo in herdr's own socket API schema
// (`herdr api schema --json`, read-only); fields not listed here — name,
// title, display_agent, interactive_ready, launch_pending,
// screen_detection_skipped, state_labels, tokens, terminal_title_stripped —
// are not yet exposed.
type Agent struct {
	PaneID         string
	WorkspaceID    string
	TabID          string
	Kind           string
	Status         AgentStatus
	Session        *AgentSession
	CWD            string
	ForegroundCWD  string
	Focused        bool
	TerminalID     string
	TerminalTitle  string
	Revision       uint64
	StateChangeSeq uint64
}

// HarnessSessionID returns the harness session identifier herdr associated
// with this agent, or "" when herdr did not expose one.
func (agent Agent) HarnessSessionID() string {
	return agent.Session.HarnessSessionID()
}

// rawPaneOrAgent is the JSON shape shared by every pane- and agent-shaped
// herdr response this package decodes: `pane`/`panes` and `agent`/`agents`
// results all use the same fields (agent list and get additionally carry
// state_change_seq, which neither [Pane] nor [Agent] currently exposes).
// Keeping one raw struct, converted into the two exported types below,
// means a field herdr adds shows up here once rather than being missed in
// one of several near-duplicate structs.
type rawPaneOrAgent struct {
	PaneID         string        `json:"pane_id"`
	WorkspaceID    string        `json:"workspace_id"`
	TabID          string        `json:"tab_id"`
	Agent          string        `json:"agent"`
	AgentStatus    AgentStatus   `json:"agent_status"`
	AgentSession   *AgentSession `json:"agent_session"`
	CWD            string        `json:"cwd"`
	ForegroundCWD  string        `json:"foreground_cwd"`
	Focused        bool          `json:"focused"`
	TerminalID     string        `json:"terminal_id"`
	TerminalTitle  string        `json:"terminal_title"`
	Revision       uint64        `json:"revision"`
	StateChangeSeq uint64        `json:"state_change_seq"`
}

func (raw rawPaneOrAgent) toPane() Pane {
	return Pane{
		PaneID:        raw.PaneID,
		WorkspaceID:   raw.WorkspaceID,
		TabID:         raw.TabID,
		AgentKind:     raw.Agent,
		AgentStatus:   raw.AgentStatus,
		AgentSession:  raw.AgentSession,
		CWD:           raw.CWD,
		ForegroundCWD: raw.ForegroundCWD,
		Focused:       raw.Focused,
		TerminalID:    raw.TerminalID,
		TerminalTitle: raw.TerminalTitle,
		Revision:      raw.Revision,
	}
}

func (raw rawPaneOrAgent) toAgent() Agent {
	return Agent{
		PaneID:         raw.PaneID,
		WorkspaceID:    raw.WorkspaceID,
		TabID:          raw.TabID,
		Kind:           raw.Agent,
		Status:         raw.AgentStatus,
		Session:        raw.AgentSession,
		CWD:            raw.CWD,
		ForegroundCWD:  raw.ForegroundCWD,
		Focused:        raw.Focused,
		TerminalID:     raw.TerminalID,
		TerminalTitle:  raw.TerminalTitle,
		Revision:       raw.Revision,
		StateChangeSeq: raw.StateChangeSeq,
	}
}

// valid reports whether raw decoded at least a pane id, the one field every
// observed shape carries. Callers use this to tell "decoded, but the
// wrong shape" apart from "decoded successfully" without a panic.
func (raw rawPaneOrAgent) valid() bool {
	return raw.PaneID != ""
}
