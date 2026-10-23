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
	// Landed is the proof behind a `landed` disposition that cleanup sealed:
	// which target received the work and at which commit. It is nil for every
	// other disposition and for a `landed` terminal `wb worktree log finalize`
	// sealed, which is the agent's own declaration.
	Landed *LandedEvidence `json:"landed,omitempty"`
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

// The proofs a LandedEvidence may name: how cleanup established that the
// sealed head's work is on the target.
const (
	// LandedProofContained: the sealed head is an ancestor of the freshly
	// fetched target (a fast-forward, a merge commit or a direct push).
	LandedProofContained = "contained"
	// LandedProofMergedPullRequest: the head is contained in the target and
	// GitHub reports a merged pull request for that exact head.
	LandedProofMergedPullRequest = "merged_pull_request"
	// LandedProofRebaseMerged: a merged pull request replayed the head's
	// commits onto the target as new commits.
	LandedProofRebaseMerged = "rebase_merged"
	// LandedProofAbsorbed: another commit on the target carries the work (a
	// squash merge, a batched integration or an acknowledged absorption).
	LandedProofAbsorbed = "absorbed"
)

// LandedEvidence says where sealed work landed. Target is the branch that
// received it, LandedSHA the commit on that branch that carries it (the sealed
// head itself when the target contains it, else the squash, merge or
// integration commit) and Proof one of the LandedProof values. PullRequest is
// the merged pull request's number when one is the proof's source.
type LandedEvidence struct {
	Target      string `json:"target"`
	LandedSHA   string `json:"landed_sha"`
	Proof       string `json:"proof"`
	PullRequest int    `json:"pull_request,omitempty"`
}

// ValidLandedEvidence reports whether evidence is complete: a target, a full
// commit id and a known proof.
func ValidLandedEvidence(evidence *LandedEvidence) bool {
	if evidence == nil || evidence.Target == "" || !worktreeproof.IsGitObjectID(evidence.LandedSHA) || evidence.PullRequest < 0 {
		return false
	}
	switch evidence.Proof {
	case LandedProofContained, LandedProofMergedPullRequest, LandedProofRebaseMerged, LandedProofAbsorbed:
		return true
	}
	return false
}

// SameLandedEvidence reports whether two optional evidences are equal.
func SameLandedEvidence(left, right *LandedEvidence) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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
	// Landed mirrors the sealed terminal's landing proof: the target and the
	// commit are public Git facts.
	Landed *LandedEvidence `json:"landed,omitempty"`
	// FinalizeReport mirrors the sealed terminal's finalize evidence into the
	// outbox receipt so a downstream Synchestra consumer sees the same
	// terminal_result/terminal_message/report_path a local reader gets from
	// wb worktree list/summary/log show.
	FinalizeReport *FinalizeReport `json:"finalize_report,omitempty"`
}
