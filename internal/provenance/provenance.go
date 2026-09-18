// Package provenance reads the harness identity a WB record can safely
// carry, at zero cost: environment variables and the already-resolved wb
// build version, nothing else. No network, no process spawn, no file I/O.
//
// This exists because a WB record today says which worktree and which
// repository, but not which agent session did the work — the SDLC
// logging-gap analysis (2026-09-18, wb#631) found claims carry
// `wb_session_id` derived from a PID that every subagent shares with its
// orchestrator, and `wb session register` ran in only 7 of 188 transcripts.
// Every field here is an opaque ID. Never a prompt, a response body, or an
// email: internal/worktrees' `model_declared_by` has carried an email once
// (see its own doc), and this package exists so that mistake is structurally
// harder to repeat — there is nowhere in Fields free text can go.
package provenance

import (
	"os"
	"regexp"
	"strings"

	"github.com/sneat-dev/wb/internal/buildinfo"
)

// Environment variables this package reads. WB_AGENT_ID and WB_TOOL_USE_ID
// are the same variables the agent guard's rewrite stamps onto a Bash call
// that invokes wb (internal/agentguard, wb#637) — this package is the other
// half of that loop: the guard writes them into the child process's
// environment, and every WB record writer reads them back.
const (
	EnvHarnessSessionID = "CLAUDE_CODE_SESSION_ID"
	EnvHarness          = "AI_AGENT"
	EnvEffortLevel      = "CLAUDE_EFFORT"
	EnvAgentID          = "WB_AGENT_ID"
	EnvToolUseID        = "WB_TOOL_USE_ID"
)

// Fields is machine identity, cheap enough to attach to every WB record:
// worktree claims, fleet events, `wb run` events, and wait records.
type Fields struct {
	// HarnessSessionID is the harness's own session identity
	// (CLAUDE_CODE_SESSION_ID). Unlike WB's PID-derived wb_session_id, this
	// is stable across every subagent a Claude Code session dispatches.
	HarnessSessionID string `json:"harness_session_id,omitempty"`
	// Harness names the driving harness (AI_AGENT), e.g. "claude-code".
	Harness string `json:"harness,omitempty"`
	// EffortLevel is the declared reasoning/effort tier (CLAUDE_EFFORT).
	EffortLevel string `json:"effort_level,omitempty"`
	// AgentID identifies a subagent (WB_AGENT_ID), set by the agent guard's
	// export prefix when a subagent's Bash call invokes wb.
	AgentID string `json:"agent_id,omitempty"`
	// ToolUseID identifies the exact tool call that ran this command
	// (WB_TOOL_USE_ID), set the same way as AgentID.
	ToolUseID string `json:"tool_use_id,omitempty"`
	// WBVersion is the running wb binary's own version. It is never empty:
	// buildinfo.Version() reports "unknown" rather than "".
	WBVersion string `json:"wb_version,omitempty"`
}

// FromEnv reads every field above from the process environment and the
// already-resolved build info. It performs no I/O beyond that, so every
// record writer can call it on every write without a latency or reliability
// cost.
func FromEnv() Fields {
	return Fields{
		HarnessSessionID: safeValue(os.Getenv(EnvHarnessSessionID)),
		Harness:          strings.TrimSpace(os.Getenv(EnvHarness)),
		EffortLevel:      strings.TrimSpace(os.Getenv(EnvEffortLevel)),
		AgentID:          safeValue(os.Getenv(EnvAgentID)),
		ToolUseID:        safeValue(os.Getenv(EnvToolUseID)),
		WBVersion:        buildinfo.Version(),
	}
}

// safeIDPattern is the charset an ID must clear before this package trusts
// it: the same compact-token shape the agent guard requires before
// interpolating WB_AGENT_ID/WB_TOOL_USE_ID into a rewritten shell command
// (internal/agentguard, wb#637). A record is safer with an omitted field
// than with one that carries whatever an untrusted environment put there.
var safeIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// SafeID reports whether value is a compact token safe to record verbatim
// and, where applicable, to interpolate unescaped into a shell command: no
// whitespace, quotes, or shell metacharacters.
func SafeID(value string) bool {
	return safeIDPattern.MatchString(value)
}

func safeValue(value string) string {
	value = strings.TrimSpace(value)
	if !SafeID(value) {
		return ""
	}
	return value
}
