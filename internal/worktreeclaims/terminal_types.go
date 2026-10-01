package worktreeclaims

import (
	"github.com/sneat-dev/wb/internal/worktreeproof"
	"time"
)

type TerminalRecord struct {
	Claim
	FinalCommit      string                               `json:"final_commit"`
	Disposition      string                               `json:"worktree_disposition"`
	SealedAt         time.Time                            `json:"sealed_at"`
	SuccessorClaimID string                               `json:"successor_claim_id,omitempty"`
	SuccessorAgentID string                               `json:"successor_agent_id,omitempty"`
	ExternalHandoff  *ExternalHandoffEvidence             `json:"external_handoff_completion,omitempty"`
	Orphaned         *worktreeproof.OrphanedEvidence      `json:"orphaned_evidence,omitempty"`
	DirtyCapture     *worktreeproof.DirtyWorktreeEvidence `json:"dirty_capture,omitempty"`
	Supersession     *worktreeproof.SupersessionReceipt   `json:"supersession,omitempty"`
	// FinalizeReport is set only when this terminal was sealed by
	// `wb worktree log finalize`. It is nil for every other disposition
	// (recycled, removed, superseded, orphaned, handoff, ...).
	FinalizeReport *FinalizeReport `json:"finalize_report,omitempty"`
}

// FinalizeReport is the optional completion evidence `wb worktree log
// finalize --report/--report-stdin` attaches to a sealed terminal. ReportPath
// names the private copy of the report body under WB_HOME; the body itself is
// never stored inline here and never enters source Git. FinalizedAt is not
// tracked separately -- it is the terminal's own SealedAt, since a
// FinalizeReport exists only on a terminal that finalize itself sealed.
type FinalizeReport struct {
	Result     string `json:"terminal_result"`
	Message    string `json:"terminal_message,omitempty"`
	ReportPath string `json:"report_path,omitempty"`
}

type PublicEvent struct {
	Version         int                                  `json:"version"`
	Type            string                               `json:"type"`
	At              time.Time                            `json:"at"`
	EffortID        string                               `json:"effort_id"`
	RunID           string                               `json:"run_id"`
	ClaimID         string                               `json:"claim_id"`
	Repository      string                               `json:"repository"`
	Branch          string                               `json:"branch"`
	Base            string                               `json:"base"`
	BaseSHA         string                               `json:"base_sha"`
	FinalCommit     string                               `json:"final_commit,omitempty"`
	Lifecycle       string                               `json:"lifecycle"`
	Disposition     string                               `json:"disposition,omitempty"`
	CorrectionID    string                               `json:"correction_id,omitempty"`
	ExternalHandoff *ExternalHandoffEvidence             `json:"external_handoff,omitempty"`
	DirtyCapture    *worktreeproof.DirtyWorktreeEvidence `json:"dirty_capture,omitempty"`
	Supersession    *worktreeproof.SupersessionReceipt   `json:"supersession,omitempty"`
	// FinalizeReport mirrors the sealed terminal's finalize evidence into the
	// outbox receipt so a downstream Synchestra consumer sees the same
	// terminal_result/terminal_message/report_path a local reader gets from
	// wb worktree list/summary/log show.
	FinalizeReport *FinalizeReport `json:"finalize_report,omitempty"`
}
