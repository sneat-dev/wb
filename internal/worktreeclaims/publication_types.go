package worktreeclaims

import "time"

// ExternalHandoffEvidence is immutable, transport-neutral lineage that
// links the source terminal and target active claim without manufacturing a
// source-local successor claim.
type ExternalHandoffEvidence struct {
	Version                int    `json:"version"`
	Protocol               string `json:"protocol,omitempty"`
	HandoffID              string `json:"handoff_id"`
	MemberID               string `json:"member_id,omitempty"`
	RequestDigest          string `json:"request_digest"`
	PredecessorWBSessionID string `json:"predecessor_wb_session_id"`
	SuccessorWBSessionID   string `json:"successor_wb_session_id"`
	SourceMachine          string `json:"source_machine"`
	TargetMachine          string `json:"target_machine"`
	SourceWorkLogReference string `json:"source_work_log_reference"`
	TargetWorkLogReference string `json:"target_work_log_reference"`
	SuccessorTmuxName      string `json:"successor_tmux_name"`
}

// Projection is an untrusted pointer. It contains no path, prompt,
// repository, branch, or model data and is never used without loading and
// corroborating the immutable private claim.
type Projection struct {
	Version   int    `json:"version"`
	EffortID  string `json:"effort_id"`
	RunID     string `json:"run_id"`
	ClaimID   string `json:"claim_id"`
	Lifecycle string `json:"lifecycle"`
}

type Claim struct {
	Version         int                      `json:"version"`
	EffortID        string                   `json:"effort_id"`
	RunID           string                   `json:"run_id"`
	ClaimID         string                   `json:"claim_id"`
	Task            string                   `json:"task"`
	Repository      string                   `json:"repository"`
	Worktree        string                   `json:"worktree"`
	Branch          string                   `json:"branch"`
	Base            string                   `json:"base"`
	BaseSHA         string                   `json:"base_sha"`
	Lifecycle       string                   `json:"lifecycle"`
	RecordedAt      time.Time                `json:"recorded_at"`
	Initiator       string                   `json:"initiator,omitempty"`
	AgentID         string                   `json:"agent_id,omitempty"`
	AgentRuntime    string                   `json:"agent_runtime,omitempty"`
	Model           string                   `json:"model,omitempty"`
	ModelProvenance string                   `json:"model_provenance,omitempty"`
	ModelDeclaredBy string                   `json:"model_declared_by,omitempty"`
	CLI             string                   `json:"cli,omitempty"`
	Provider        string                   `json:"provider,omitempty"`
	TaskSummary     string                   `json:"task_summary,omitempty"`
	WBSessionID     string                   `json:"wb_session_id,omitempty"`
	PromptArchive   string                   `json:"prompt_archive,omitempty"` // run-relative
	PromptDigest    string                   `json:"prompt_sha256,omitempty"`
	ParentClaimID   string                   `json:"parent_claim_id,omitempty"`
	AcquiredVia     string                   `json:"acquired_via,omitempty"`
	ExternalHandoff *ExternalHandoffEvidence `json:"external_handoff,omitempty"`

	// Provenance fields (wb#631, SDLC logging-gap analysis 2026-09-18): IDs
	// only, read at zero cost from the environment by
	// internal/provenance.FromEnv, never a prompt or response body. Additive
	// and omitempty, so an older WB reading this claim sees nothing new and a
	// claim written before this change decodes with every one of them empty
	// — no schema version bump was needed for that.
	//
	// HarnessSessionID is stable across every subagent one harness session
	// dispatches, unlike WBSessionID above, which every subagent shares
	// because it derives from the orchestrator's PID.
	HarnessSessionID string `json:"harness_session_id,omitempty"`
	Harness          string `json:"harness,omitempty"`
	EffortLevel      string `json:"effort_level,omitempty"`
	// ToolUseID identifies the exact tool call that created this claim, set
	// by the agent guard's export prefix (internal/agentguard, wb#637) when
	// this claim was created from a subagent's Bash call.
	ToolUseID string `json:"tool_use_id,omitempty"`
	// WBVersion is the wb binary that wrote this claim.
	WBVersion string `json:"wb_version,omitempty"`
}

// IdentityCorrection is immutable evidence. Field presence, rather
// than an empty value convention, makes clearing optional fields auditable.
type IdentityCorrection struct {
	Version       int       `json:"version"`
	Type          string    `json:"type"`
	CorrectionID  string    `json:"correction_id"`
	ClaimID       string    `json:"claim_id"`
	Sequence      int       `json:"sequence"`
	PredecessorID string    `json:"predecessor_id,omitempty"`
	At            time.Time `json:"at"`
	Actor         string    `json:"actor"`
	Reason        string    `json:"reason"`
	Initiator     string    `json:"initiator,omitempty"`
	Model         *string   `json:"model,omitempty"`
	CLI           *string   `json:"cli,omitempty"`
	Provider      *string   `json:"provider,omitempty"`
}

// CorrectionOptions changes only explicitly selected fields.
// Nil means leave unchanged; a pointer to "" clears CLI/provider. Model cannot
// be cleared: use the explicit value "unknown" instead.
type CorrectionOptions struct {
	ProjectsRoot string
	EffortID     string
	RunID        string
	ClaimID      string
	EventID      string
	Actor        string
	Reason       string
	Initiator    string
	Model        *string
	CLI          *string
	Provider     *string
}

type CorrectionResult struct {
	ClaimID      string            `json:"claim_id"`
	CorrectionID string            `json:"correction_id"`
	Identity     ExecutionIdentity `json:"identity"`
	OutboxPath   string            `json:"outbox_path"`
}
