package worktreeclaims

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// PublicationPorts contains only the effects needed by one publication.
// The caller retains its run descriptor for the whole claim/projection/outbox
// sequence; no package-global hooks or filesystem state are installed.
type PublicationPorts struct {
	OpenPrivateChild     func(*os.File, string, bool) (*os.File, error)
	WriteJSONImmutableAt func(*os.File, string, any, bool) error
	EnsureRunIndex       func(*os.File, string, string) error
	WriteProjection      func(string, Projection) error
	WriteCreationJournal func(Claim) error
	OpenOutbox           func(string, string, bool) (*os.File, error)
}

type PublicationHooks struct {
	AfterClaim      func() error
	AfterProjection func() error
}

type PublicationReceipt struct {
	ClaimPath         string
	ClaimWritten      bool
	ProjectionWritten bool
	OutboxWritten     bool
}

type ClaimPublicEvent struct {
	Version    int       `json:"version"`
	Type       string    `json:"type"`
	At         time.Time `json:"at"`
	EffortID   string    `json:"effort_id"`
	RunID      string    `json:"run_id"`
	ClaimID    string    `json:"claim_id"`
	Repository string    `json:"repository"`
	Branch     string    `json:"branch"`
	Base       string    `json:"base"`
	BaseSHA    string    `json:"base_sha"`
	Lifecycle  string    `json:"lifecycle"`
}

// PublishClaim writes immutable authority before any derivative, then records
// the run index, worktree pointer, recovery journal, and outbox in that order.
func (p PublicationPorts) PublishClaim(home string, runDir *os.File, runPath string, claim Claim, hooks PublicationHooks) (PublicationReceipt, error) {
	var receipt PublicationReceipt
	claims, err := p.OpenPrivateChild(runDir, "claims", true)
	if err != nil {
		return receipt, err
	}
	defer func() { _ = claims.Close() }()
	claimName := claim.ClaimID + ".json"
	if err := p.WriteJSONImmutableAt(claims, claimName, claim, true); err != nil {
		return receipt, fmt.Errorf("write immutable work-log claim: %w", err)
	}
	receipt.ClaimPath = filepath.Join(runPath, "claims", claimName)
	receipt.ClaimWritten = true
	if hooks.AfterClaim != nil {
		if err := hooks.AfterClaim(); err != nil {
			return receipt, fmt.Errorf("after immutable work-log claim publication: %w", err)
		}
	}
	if err := p.EnsureRunIndex(runDir, claim.EffortID, claim.RunID); err != nil {
		return receipt, err
	}
	projection := Projection{Version: 1, EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID, Lifecycle: "active"}
	if err := p.WriteProjection(claim.Worktree, projection); err != nil {
		return receipt, err
	}
	receipt.ProjectionWritten = true
	if err := p.WriteCreationJournal(claim); err != nil {
		return receipt, err
	}
	if hooks.AfterProjection != nil {
		if err := hooks.AfterProjection(); err != nil {
			return receipt, fmt.Errorf("after work-log recovery projection publication: %w", err)
		}
	}
	outbox, err := p.OpenOutbox(home, claim.EffortID, true)
	if err != nil {
		return receipt, err
	}
	defer func() { _ = outbox.Close() }()
	event := ClaimPublicEvent{Version: 1, Type: "worktree.claimed", At: claim.RecordedAt,
		EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID, Repository: claim.Repository,
		Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA, Lifecycle: "active"}
	if err := p.WriteJSONImmutableAt(outbox, claim.RunID+"-"+claim.ClaimID+"-claimed.json", event, true); err != nil {
		return receipt, err
	}
	receipt.OutboxWritten = true
	return receipt, nil
}
