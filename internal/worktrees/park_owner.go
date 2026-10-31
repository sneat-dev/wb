package worktrees

import (
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionauthority"
)

// ParkOwnerEvidence describes current custody separately from immutable claim
// attribution. Only the latest appended owner event controls current ownership.
type ParkOwnerEvidence struct {
	WBSessionID      string            `json:"wb_session_id,omitempty"`
	ClaimWBSessionID string            `json:"claim_wb_session_id,omitempty"`
	Binding          string            `json:"binding"`
	EventID          string            `json:"event_id,omitempty"`
	Owner            OwnerRegistration `json:"owner,omitzero"`
	HasOwner         bool              `json:"has_owner"`
}

func (e ParkOwnerEvidence) IsZero() bool { return e.Binding == "" }

func effectiveParkOwner(claimSessionID string, events []LocalWorkLogEvent) ParkOwnerEvidence {
	evidence := ParkOwnerEvidence{ClaimWBSessionID: claimSessionID, Binding: "original", WBSessionID: claimSessionID}
	count := 0
	var latest LocalWorkLogEvent
	for _, event := range events {
		if event.Type == LocalEventOwner && event.Owner != nil {
			latest = event
			count++
		}
	}
	if count == 0 {
		return evidence
	}
	evidence.HasOwner = true
	evidence.Owner = *latest.Owner
	evidence.EventID = latest.ID
	bound := ""
	for _, key := range []string{"successor_wb_session_id", "owner_wb_session_id"} {
		if value, exists := latest.Extra[key]; exists {
			id, ok := value.(string)
			if !ok || !sessionauthority.ValidID(id) || bound != "" && bound != id {
				evidence.Binding = "invalid"
				evidence.WBSessionID = ""
				return evidence
			}
			bound = id
		}
	}
	if bound != "" {
		runtime, id, ok := strings.Cut(evidence.Owner.Agent, "/")
		if !ok || runtime == "" || id != bound || evidence.Owner.PID <= 0 || evidence.Owner.At.IsZero() {
			evidence.Binding = "invalid"
			evidence.WBSessionID = ""
			return evidence
		}
		evidence.WBSessionID = bound
		evidence.Binding = "bound"
		return evidence
	}
	if count > 1 {
		evidence.Binding = "unbound"
		evidence.WBSessionID = ""
	}
	return evidence
}

// MatchesSource is shared by selection and locked capture. Strong foreign
// IDs never fall back to a shared PID. Legacy PID-only records are admitted
// only for nonshared unscoped sources lacking a contradictory agent identity.
func (e ParkOwnerEvidence) MatchesSource(source session.Record) (bool, error) {
	if source.Scope != "" {
		if err := source.ValidateScope(); err != nil {
			return false, err
		}
		if source.ProcessStartedAt.IsZero() {
			return false, fmt.Errorf("scoped owner has no native process generation")
		}
	}
	native := source.NativeHarnessID
	if native == "" {
		native = source.AgentID
	}
	ownerMatches := e.Owner.Agent == source.Runtime+"/"+source.WBSessionID || native != "" && e.Owner.Agent == source.Runtime+"/"+native
	if e.Binding == "invalid" || e.Binding == "unbound" {
		if e.ClaimWBSessionID == source.WBSessionID || ownerMatches {
			return false, fmt.Errorf("current owner has no valid explicit WB session binding")
		}
		return false, nil
	}
	if e.WBSessionID != "" && e.WBSessionID != source.WBSessionID {
		return false, nil
	}
	if !e.HasOwner {
		return e.WBSessionID == source.WBSessionID, nil
	}
	if e.Owner.PID != source.PID || e.Owner.At.Before(source.StartedAt) {
		return false, nil
	}
	if !source.ProcessStartedAt.IsZero() {
		observed, known := session.ObserveProcessGeneration(source.PID)
		if !known || !observed.Equal(source.ProcessStartedAt) {
			return false, fmt.Errorf("current owner process generation changed")
		}
	}
	if e.Binding == "bound" {
		if e.Owner.Agent != source.Runtime+"/"+source.WBSessionID {
			return false, fmt.Errorf("bound owner runtime contradicts source")
		}
		return true, nil
	}
	if ownerMatches {
		return true, nil
	}
	allowPID := source.Scope == "" && !session.IsRuntimeProcess(source.PID, "codex")
	if allowPID && (e.Owner.Agent == "" || e.Owner.Agent == source.Runtime || e.Owner.Agent == "unknown") {
		return true, nil
	}
	if e.ClaimWBSessionID == source.WBSessionID {
		return false, fmt.Errorf("original claim has a different current native owner")
	}
	return false, nil
}
