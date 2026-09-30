package worktreeclaims

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// PublicationPorts contains only the effects needed by one publication.
// The caller retains its run descriptor for the whole claim/projection/outbox
// sequence; no package-global hooks or filesystem state are installed.
type PublicationPorts struct {
	OpenPrivateChild     func(*os.File, string, bool) (*os.File, error)
	ReadClaimAt          func(*os.File, string) (Claim, error)
	ReadClaimNames       func(*os.File) ([]string, error)
	CorroborateExisting  func(Claim) error
	RequiredSessionID    string
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
	Claim             Claim
}

// SamePublicationRequest ignores only observations that can change between
// retries after the original immutable claim became durable.
func SamePublicationRequest(existing, requested Claim, requiredSessionID string) bool {
	if existing.RecordedAt.IsZero() {
		return false
	}
	if requiredSessionID != "" && existing.WBSessionID != requiredSessionID {
		return false
	}
	existing.RecordedAt = requested.RecordedAt
	existing.WBSessionID = requested.WBSessionID
	existing.HarnessSessionID = requested.HarnessSessionID
	existing.Harness = requested.Harness
	existing.EffortLevel = requested.EffortLevel
	existing.ToolUseID = requested.ToolUseID
	existing.WBVersion = requested.WBVersion
	return reflect.DeepEqual(existing, requested)
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
	existing, readErr := p.ReadClaimAt(runDir, claim.ClaimID)
	if readErr == nil {
		if !SamePublicationRequest(existing, claim, p.RequiredSessionID) {
			return receipt, fmt.Errorf("existing immutable work-log claim conflicts with this publication request")
		}
		if err := p.CorroborateExisting(existing); err != nil {
			return receipt, fmt.Errorf("corroborate existing immutable work-log claim: %w", err)
		}
		claim = existing
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return receipt, fmt.Errorf("read existing immutable work-log claim: %w", readErr)
	} else {
		// A retry with a changed checkout identity has a different digest and
		// therefore a different filename. Do not publish a second authority for
		// the same worktree while its first projection is still missing.
		names, listErr := p.ReadClaimNames(claims)
		if listErr != nil {
			return receipt, fmt.Errorf("list immutable work-log claims: %w", listErr)
		}
		for _, name := range names {
			if strings.HasPrefix(name, ".") {
				continue // incomplete atomic-write scratch, never authority
			}
			otherID := strings.TrimSuffix(name, ".json")
			if name != otherID+".json" || !ValidClaimID(otherID) {
				return receipt, fmt.Errorf("unsafe immutable work-log claim entry %q", name)
			}
			other, otherErr := p.ReadClaimAt(runDir, otherID)
			if otherErr != nil {
				return receipt, fmt.Errorf("read immutable work-log claim %s: %w", otherID, otherErr)
			}
			if other.ClaimID != otherID {
				return receipt, fmt.Errorf("immutable work-log claim %s has mismatched identity", otherID)
			}
			if otherID != claim.ClaimID && filepath.Clean(other.Worktree) == filepath.Clean(claim.Worktree) {
				return receipt, fmt.Errorf("existing immutable work-log claim conflicts with this worktree publication request")
			}
		}
	}
	if readErr != nil {
		if err := p.WriteJSONImmutableAt(claims, claimName, claim, true); err != nil {
			// A concurrent first writer may have published after the read. Re-read
			// through the descriptor and prove its identity before any derivative.
			existing, readErr = p.ReadClaimAt(runDir, claim.ClaimID)
			if readErr != nil || !SamePublicationRequest(existing, claim, p.RequiredSessionID) {
				return receipt, fmt.Errorf("write immutable work-log claim: %w", err)
			}
			if corroborateErr := p.CorroborateExisting(existing); corroborateErr != nil {
				return receipt, fmt.Errorf("corroborate concurrently published work-log claim: %w", corroborateErr)
			}
			claim = existing
		}
	}
	receipt.ClaimPath = filepath.Join(runPath, "claims", claimName)
	receipt.ClaimWritten = true
	receipt.Claim = claim
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
