package worktreeclaims

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/worktreelayout"
	"github.com/sneat-dev/wb/internal/worktreeproof"
	"github.com/sneat-dev/wb/internal/worktreesecure"
)

const (
	ReconciliationRecordName    = "record.json"
	ReconciliationStagePlanned  = "planned"
	ReconciliationStageBundles  = "bundles_preserved"
	ReconciliationStageRemote   = "remote_retired"
	ReconciliationStageLocal    = "local_retired"
	ReconciliationStageRebound  = "branch_rebound"
	ReconciliationStageEvent    = "event_appended"
	ReconciliationStageComplete = "complete"
)

// ReconciliationRecord is private, durable recovery authority. Its JSON keys
// and stage strings remain stable across a reconciliation restart.
type ReconciliationRecord struct {
	Version      int       `json:"version"`
	EventID      string    `json:"event_id"`
	ClaimID      string    `json:"claim_id"`
	Worktree     string    `json:"worktree"`
	Repository   string    `json:"repository"`
	ClaimBranch  string    `json:"claim_branch"`
	LiveBranch   string    `json:"live_branch"`
	ExpectedHead string    `json:"expected_head"`
	LocalHead    string    `json:"local_claim_head"`
	RemoteHead   string    `json:"remote_claim_head"`
	TargetHead   string    `json:"target_head"`
	Actor        string    `json:"actor"`
	Reason       string    `json:"reason"`
	Stage        string    `json:"stage"`
	CreatedAt    time.Time `json:"created_at"`
}

type ReconciliationEvidence struct {
	Version int    `json:"version"`
	Head    string `json:"head"`
	Ref     string `json:"ref"`
	Bundle  string `json:"bundle"`
	SHA256  string `json:"sha256"`
}

type ReconciliationRequest struct {
	Worktree, EventID, LiveBranch, ExpectedHead, Actor, Reason string
}

// ReconciliationPorts binds one private claim read and record transaction.
// ReadProjection is required: the facade selects the read-only projection
// reader so dry-run cannot accidentally repair derived state.
type ReconciliationPorts struct {
	ReadProjection func(string) (Projection, error)
	OpenRun        func(string, string, string, bool) (*os.File, string, error)
	OpenChild      func(*os.File, string, bool) (*os.File, error)
	ReadClaimAt    func(*os.File, string) (Claim, error)
	ReadJSON       func(*os.File, string, any) error
	WriteJSON      func(*os.File, string, any, os.FileMode) error
}

func (p ReconciliationPorts) withDefaults() ReconciliationPorts {
	if p.OpenRun == nil {
		p.OpenRun = func(home, effort, run string, create bool) (*os.File, string, error) {
			return OpenWorkLogRun(home, effort, run, create, worktreelayout.ValidSafeSegment)
		}
	}
	if p.OpenChild == nil {
		p.OpenChild = func(parent *os.File, name string, create bool) (*os.File, error) {
			return worktreesecure.OpenPrivateChild(parent, name, create, worktreelayout.ValidSafeSegment)
		}
	}
	if p.ReadClaimAt == nil {
		p.ReadClaimAt = func(run *os.File, claimID string) (Claim, error) {
			return ReadWorkLogClaimAt[Claim](run, claimID, worktreelayout.ValidSafeSegment)
		}
	}
	if p.ReadJSON == nil {
		p.ReadJSON = filewrite.ReadJSONAt
	}
	if p.WriteJSON == nil {
		p.WriteJSON = filewrite.WriteJSONAtomicAt
	}
	return p
}

func (p ReconciliationPorts) ReadClaim(home, worktree string) (Projection, Claim, error) {
	p = p.withDefaults()
	if p.ReadProjection == nil {
		return Projection{}, Claim{}, fmt.Errorf("read-only reconciliation projection reader is required")
	}
	projection, err := p.ReadProjection(worktree)
	if err != nil {
		return Projection{}, Claim{}, err
	}
	run, _, err := p.OpenRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return Projection{}, Claim{}, err
	}
	defer func() { _ = run.Close() }()
	claim, err := p.ReadClaimAt(run, projection.ClaimID)
	if err != nil {
		return Projection{}, Claim{}, err
	}
	if err := ValidateReconciliationClaimShape(worktree, projection, claim); err != nil {
		return Projection{}, Claim{}, err
	}
	return projection, claim, nil
}

// ValidateReconciliationClaimShape intentionally accepts only the historical
// ordinary and listed local successor acquisitions. External and parked
// claims require different evidence and were never reconciliation authority.
func ValidateReconciliationClaimShape(worktree string, projection Projection, claim Claim) error {
	if (claim.Version != 1 && claim.Version != 2) || claim.EffortID != projection.EffortID || claim.RunID != projection.RunID || claim.ClaimID != projection.ClaimID || claim.Lifecycle != "active" || filepath.Clean(claim.Worktree) != filepath.Clean(worktree) {
		return fmt.Errorf("work-log projection does not match immutable active claim")
	}
	if !worktreelayout.ValidSafeSegment(claim.EffortID) || !worktreelayout.ValidSafeSegment(claim.RunID) || !worktreelayout.ValidSafeSegment(claim.Task) || !ValidClaimID(claim.ClaimID) || !worktreeproof.IsGitObjectID(claim.BaseSHA) {
		return fmt.Errorf("private work-log claim identity is invalid")
	}
	want := WorkLogClaimID(claim.EffortID, CreationResult{Repository: claim.Repository, WorktreeDir: claim.Worktree, Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA})
	if claim.ParentClaimID != "" {
		if !ValidClaimID(claim.ParentClaimID) || claim.AgentID == "" || (claim.AcquiredVia != "handoff" && claim.AcquiredVia != "not_landed" && claim.AcquiredVia != "recycle_failed") {
			return fmt.Errorf("private successor claim metadata is invalid")
		}
		if claim.Version == 2 && claim.AcquiredVia != "recycle_failed" {
			want = DeclaredSuccessorWorkLogClaimID(claim.ParentClaimID, claim.AgentID, claim.AcquiredVia, ClaimExecutionIdentity{Model: claim.Model, CLI: claim.CLI, Provider: claim.Provider})
		} else {
			want = SuccessorWorkLogClaimID(claim.ParentClaimID, claim.AgentID, claim.AcquiredVia)
		}
	}
	if want != claim.ClaimID {
		return fmt.Errorf("private work-log claim digest mismatch")
	}
	return nil
}

func (p ReconciliationPorts) OpenEvent(home string, claim Claim, eventID string, create bool) (*os.File, error) {
	p = p.withDefaults()
	run, _, err := p.OpenRun(home, claim.EffortID, claim.RunID, create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = run.Close() }()
	reconciliations, err := p.OpenChild(run, "branch-reconciliations", create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reconciliations.Close() }()
	return p.OpenChild(reconciliations, eventID, create)
}

func (p ReconciliationPorts) CreateRecord(home string, claim Claim, record ReconciliationRecord) (*os.File, error) {
	directory, err := p.OpenEvent(home, claim, record.EventID, true)
	if err != nil {
		return nil, err
	}
	if err := p.WriteRecord(directory, record); err != nil {
		_ = directory.Close()
		return nil, err
	}
	return directory, nil
}

func (p ReconciliationPorts) ReadRecord(home string, claim Claim, eventID string) (ReconciliationRecord, *os.File, error) {
	p = p.withDefaults()
	directory, err := p.OpenEvent(home, claim, eventID, false)
	if err != nil {
		return ReconciliationRecord{}, nil, err
	}
	var record ReconciliationRecord
	if err := p.ReadJSON(directory, ReconciliationRecordName, &record); err != nil {
		_ = directory.Close()
		return ReconciliationRecord{}, nil, err
	}
	return record, directory, nil
}

func (p ReconciliationPorts) WriteRecord(directory *os.File, record ReconciliationRecord) error {
	p = p.withDefaults()
	return p.WriteJSON(directory, ReconciliationRecordName, record, 0o600)
}

func CorroborateReconciliationRecord(record ReconciliationRecord, claim Claim, request ReconciliationRequest) error {
	if record.Version != 1 || record.EventID != request.EventID || record.ClaimID != claim.ClaimID || filepath.Clean(record.Worktree) != filepath.Clean(request.Worktree) ||
		record.ClaimBranch != claim.Branch || record.LiveBranch != request.LiveBranch || record.ExpectedHead != request.ExpectedHead || record.Actor != request.Actor || record.Reason != request.Reason ||
		!worktreeproof.IsGitObjectID(record.LocalHead) || !worktreeproof.IsGitObjectID(record.RemoteHead) || !worktreeproof.IsGitObjectID(record.TargetHead) {
		return fmt.Errorf("branch reconciliation record does not match immutable claim and requested recovery")
	}
	return nil
}
