package worktreeclaims

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/sneat-dev/wb/internal/worktreeproof"
)

var ErrImmutableTerminalConflict = errors.New("immutable terminal conflicts with requested transition")

// TerminalEvidence holds the optional, immutable authority for one seal. The
// negative orphaned proof stays private; the public outbox never copies it.
type TerminalEvidence struct {
	ExternalHandoff *ExternalHandoffEvidence
	Orphaned        *worktreeproof.OrphanedEvidence
	DirtyCapture    *worktreeproof.DirtyWorktreeEvidence
	Supersession    *worktreeproof.SupersessionReceipt
	Landed          *LandedEvidence
	FinalizeReport  *FinalizeReport
}

type TerminalSealRequest struct {
	Claim            Claim
	FinalCommit      string
	Disposition      string
	SuccessorClaimID string
	SuccessorAgentID string
	Evidence         TerminalEvidence
}

// TerminalPorts is scoped to one seal, including both durable writes. The
// caller holds its claim fence until SealTerminal and projection publication
// return; a retry observes the original timestamp from the terminal record.
type TerminalPorts struct {
	OpenPrivateChild   func(*os.File, string, bool) (*os.File, error)
	ReadJSONAt         func(*os.File, string, any) error
	WriteJSONImmutable func(*os.File, string, any, bool) error
	OpenOutbox         func(string, string, bool) (*os.File, error)
	Now                func() time.Time
}

func SameFinalizeReport(left, right *FinalizeReport) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func SameOrphanedEvidence(left, right *worktreeproof.OrphanedEvidence) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func SameDirtyWorktreeEvidence(left, right *worktreeproof.DirtyWorktreeEvidence) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func ValidateOrphanedEvidence(evidence *worktreeproof.OrphanedEvidence) error {
	if evidence == nil || evidence.Version != 1 || evidence.Actor == "" || evidence.Reason == "" ||
		!evidence.WorktreeAbsent || !evidence.RegistrationAbsent || !evidence.LocalBranchAbsent ||
		!evidence.RemoteBranchAbsent || !evidence.TerminalAbsent {
		return fmt.Errorf("orphaned terminal requires complete negative authority evidence")
	}
	return nil
}

func (ports TerminalPorts) SealTerminal(home string, runDir *os.File, request TerminalSealRequest) (time.Time, error) {
	if request.Disposition == "orphaned" {
		if err := ValidateOrphanedEvidence(request.Evidence.Orphaned); err != nil {
			return time.Time{}, err
		}
	}
	if request.Evidence.Landed != nil && (request.Disposition != "landed" || !ValidLandedEvidence(request.Evidence.Landed)) {
		return time.Time{}, fmt.Errorf("landed evidence requires the landed disposition, a target, a commit and a known proof")
	}
	sealedAt := ports.Now().UTC()
	claim := request.Claim
	claim.Lifecycle = "terminal"
	terminal := TerminalRecord{Claim: claim, FinalCommit: request.FinalCommit,
		Disposition: request.Disposition, SealedAt: sealedAt, SuccessorClaimID: request.SuccessorClaimID,
		SuccessorAgentID: request.SuccessorAgentID, ExternalHandoff: request.Evidence.ExternalHandoff,
		Orphaned: request.Evidence.Orphaned, DirtyCapture: request.Evidence.DirtyCapture,
		Supersession: request.Evidence.Supersession, Landed: request.Evidence.Landed,
		FinalizeReport: request.Evidence.FinalizeReport}
	terminals, err := ports.OpenPrivateChild(runDir, "terminals", true)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = terminals.Close() }()
	terminalName := claim.ClaimID + ".json"
	var existing TerminalRecord
	if err := ports.ReadJSONAt(terminals, terminalName, &existing); err == nil {
		if existing.ClaimID != claim.ClaimID || existing.FinalCommit != request.FinalCommit || existing.Disposition != request.Disposition || existing.Lifecycle != "terminal" ||
			existing.SuccessorClaimID != request.SuccessorClaimID || existing.SuccessorAgentID != request.SuccessorAgentID ||
			!sameExternalHandoff(existing.ExternalHandoff, request.Evidence.ExternalHandoff) ||
			!SameOrphanedEvidence(existing.Orphaned, request.Evidence.Orphaned) ||
			!SameDirtyWorktreeEvidence(existing.DirtyCapture, request.Evidence.DirtyCapture) ||
			!worktreeproof.SameSupersessionReceipt(existing.Supersession, request.Evidence.Supersession) ||
			!SameLandedEvidence(existing.Landed, request.Evidence.Landed) ||
			!SameFinalizeReport(existing.FinalizeReport, request.Evidence.FinalizeReport) {
			return time.Time{}, ErrImmutableTerminalConflict
		}
		sealedAt = existing.SealedAt
	} else if !errors.Is(err, os.ErrNotExist) {
		return time.Time{}, fmt.Errorf("inspect immutable terminal: %w", err)
	} else if err := ports.WriteJSONImmutable(terminals, terminalName, terminal, false); err != nil {
		return time.Time{}, fmt.Errorf("write immutable terminal: %w", err)
	}
	outbox, err := ports.OpenOutbox(home, claim.EffortID, true)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = outbox.Close() }()
	event := PublicEvent{Version: 1, Type: "worktree.sealed", At: sealedAt, EffortID: claim.EffortID,
		RunID: claim.RunID, ClaimID: claim.ClaimID, Repository: claim.Repository, Branch: claim.Branch,
		Base: claim.Base, BaseSHA: claim.BaseSHA, FinalCommit: request.FinalCommit, Lifecycle: "terminal",
		Disposition: request.Disposition, ExternalHandoff: request.Evidence.ExternalHandoff,
		DirtyCapture: request.Evidence.DirtyCapture, Supersession: request.Evidence.Supersession,
		Landed: request.Evidence.Landed, FinalizeReport: request.Evidence.FinalizeReport}
	if err := ports.WriteJSONImmutable(outbox, claim.RunID+"-"+claim.ClaimID+"-sealed.json", event, true); err != nil {
		return time.Time{}, fmt.Errorf("write immutable terminal outbox: %w", err)
	}
	return sealedAt, nil
}

func sameExternalHandoff(left, right *ExternalHandoffEvidence) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
