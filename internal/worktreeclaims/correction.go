package worktreeclaims

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type CorrectionPorts struct {
	OpenRun              func(home, effort, run string, create bool) (*os.File, string, error)
	LockClaim            func(*os.File, string) (func(), error)
	OpenPrivateChild     func(*os.File, string, bool) (*os.File, error)
	ReadJSONAt           func(*os.File, string, any) error
	WriteJSONImmutableAt func(*os.File, string, any, bool) error
	OpenOutbox           func(string, string, bool) (*os.File, error)
	ValidSafeSegment     func(string) bool
	ReadNames            func(*os.File) ([]string, error)
	Now                  func() time.Time
}

type CorrectionOutboxEvent struct {
	Version      int       `json:"version"`
	Type         string    `json:"type"`
	At           time.Time `json:"at"`
	EffortID     string    `json:"effort_id"`
	RunID        string    `json:"run_id"`
	ClaimID      string    `json:"claim_id"`
	Repository   string    `json:"repository"`
	Branch       string    `json:"branch"`
	Base         string    `json:"base"`
	BaseSHA      string    `json:"base_sha"`
	Lifecycle    string    `json:"lifecycle"`
	CorrectionID string    `json:"correction_id,omitempty"`
}

func IdentityFromPublicationClaim(claim Claim) ExecutionIdentity {
	return IdentityFromClaim(ClaimIdentity{Model: claim.Model, ModelProvenance: claim.ModelProvenance, ModelDeclaredBy: claim.ModelDeclaredBy, CLI: claim.CLI, Provider: claim.Provider})
}

func (p CorrectionPorts) CurrentExecutionIdentity(home string, claim Claim) (ExecutionIdentity, error) {
	runDir, _, err := p.OpenRun(home, claim.EffortID, claim.RunID, false)
	if err != nil {
		return ExecutionIdentity{}, err
	}
	defer func() { _ = runDir.Close() }()
	identity, _, err := p.ProjectExecutionIdentity(runDir, claim)
	return identity, err
}

func (p CorrectionPorts) CorrectExecutionIdentity(home string, options CorrectionOptions) (CorrectionResult, error) {
	var result CorrectionResult
	runDir, _, err := p.OpenRun(home, options.EffortID, options.RunID, false)
	if err != nil {
		return result, fmt.Errorf("open correction Work Log run: %w", err)
	}
	defer func() { _ = runDir.Close() }()
	unlock, err := p.LockClaim(runDir, options.ClaimID)
	if err != nil {
		return result, fmt.Errorf("lock correction claim: %w", err)
	}
	defer unlock()
	claims, err := p.OpenPrivateChild(runDir, "claims", false)
	if err != nil {
		return result, fmt.Errorf("open correction claims: %w", err)
	}
	var claim Claim
	err = p.ReadJSONAt(claims, options.ClaimID+".json", &claim)
	_ = claims.Close()
	if err != nil {
		return result, fmt.Errorf("read immutable claim %s: %w", options.ClaimID, err)
	}
	if claim.ClaimID != options.ClaimID || claim.EffortID != options.EffortID || claim.RunID != options.RunID || (claim.Version != 1 && claim.Version != 2) {
		return result, fmt.Errorf("claim does not match the supplied Work Log identity")
	}
	identity, corrections, err := p.ProjectExecutionIdentity(runDir, claim)
	if err != nil {
		return result, fmt.Errorf("project identity before correction: %w", err)
	}
	for _, correction := range corrections {
		if correction.CorrectionID == options.EventID {
			if !SameCorrectionRequest(correction, options) {
				return result, fmt.Errorf("correction event ID %q already denotes different immutable evidence", options.EventID)
			}
			return p.WriteCorrectionOutbox(home, claim, correction, identity)
		}
	}
	previous := ""
	if len(corrections) != 0 {
		previous = corrections[len(corrections)-1].CorrectionID
	}
	event := IdentityCorrection{Version: 1, Type: "worktree.execution_identity_corrected", CorrectionID: options.EventID,
		ClaimID: claim.ClaimID, Sequence: len(corrections) + 1, PredecessorID: previous, At: p.Now().UTC(),
		Actor: strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason), Initiator: strings.TrimSpace(options.Initiator), Model: NormalizedPointer(options.Model), CLI: NormalizedPointer(options.CLI), Provider: NormalizedPointer(options.Provider)}
	correctionsDir, err := p.OpenWorkLogCorrections(runDir, claim.ClaimID, true)
	if err != nil {
		return result, fmt.Errorf("open correction history: %w", err)
	}
	defer func() { _ = correctionsDir.Close() }()
	name := event.CorrectionID + ".json"
	if _, readErr := p.ReadIdentityCorrection(correctionsDir, name); readErr == nil {
		return result, fmt.Errorf("correction event ID %q appeared concurrently; retry the exact command", event.CorrectionID)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return result, readErr
	} else if err := p.WriteJSONImmutableAt(correctionsDir, name, event, false); err != nil {
		return result, fmt.Errorf("append immutable execution-identity correction: %w", err)
	}
	identity, _, err = p.ProjectExecutionIdentity(runDir, claim)
	if err != nil {
		return result, fmt.Errorf("project identity after correction: %w", err)
	}
	return p.WriteCorrectionOutbox(home, claim, event, identity)
}

func (p CorrectionPorts) WriteCorrectionOutbox(home string, claim Claim, event IdentityCorrection, identity ExecutionIdentity) (CorrectionResult, error) {
	var result CorrectionResult
	outbox, err := p.OpenOutbox(home, claim.EffortID, true)
	if err != nil {
		return result, fmt.Errorf("open correction outbox: %w", err)
	}
	defer func() { _ = outbox.Close() }()
	public := CorrectionOutboxEvent{Version: 1, Type: event.Type, At: event.At, EffortID: claim.EffortID,
		RunID: claim.RunID, ClaimID: claim.ClaimID, Repository: claim.Repository, Branch: claim.Branch,
		Base: claim.Base, BaseSHA: claim.BaseSHA, Lifecycle: claim.Lifecycle, CorrectionID: event.CorrectionID}
	outboxName := claim.RunID + "-" + claim.ClaimID + "-identity-" + event.CorrectionID + ".json"
	if err := p.WriteJSONImmutableAt(outbox, outboxName, public, true); err != nil {
		return result, fmt.Errorf("write execution-identity correction outbox receipt: %w", err)
	}
	return CorrectionResult{ClaimID: claim.ClaimID, CorrectionID: event.CorrectionID,
		Identity: identity, OutboxPath: filepath.Join(home, "worklogs", claim.EffortID, "outbox", outboxName)}, nil
}

func NormalizedPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := strings.TrimSpace(*value)
	return &copy
}

func SameCorrectionRequest(correction IdentityCorrection, options CorrectionOptions) bool {
	return correction.Actor == strings.TrimSpace(options.Actor) && correction.Reason == strings.TrimSpace(options.Reason) && correction.Initiator == strings.TrimSpace(options.Initiator) &&
		SameStringPointer(correction.Model, NormalizedPointer(options.Model)) && SameStringPointer(correction.CLI, NormalizedPointer(options.CLI)) && SameStringPointer(correction.Provider, NormalizedPointer(options.Provider))
}
func SameStringPointer(left, right *string) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func (p CorrectionPorts) OpenWorkLogCorrections(runDir *os.File, claimID string, create bool) (*os.File, error) {
	if !ValidClaimID(claimID) {
		return nil, fmt.Errorf("invalid correction claim ID")
	}
	root, err := p.OpenPrivateChild(runDir, "corrections", create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return p.OpenPrivateChild(root, claimID, create)
}

func (p CorrectionPorts) ReadIdentityCorrection(directory *os.File, name string) (IdentityCorrection, error) {
	var correction IdentityCorrection
	err := p.ReadJSONAt(directory, name, &correction)
	return correction, err
}

// projectExecutionIdentity proves there is one linear, complete correction
// chain. It deliberately uses sequence/predecessor, not timestamp ordering.
func (p CorrectionPorts) ProjectExecutionIdentity(runDir *os.File, claim Claim) (ExecutionIdentity, []IdentityCorrection, error) {
	identity := IdentityFromPublicationClaim(claim)
	directory, err := p.OpenWorkLogCorrections(runDir, claim.ClaimID, false)
	if errors.Is(err, os.ErrNotExist) {
		return identity, nil, nil
	}
	if err != nil {
		return ExecutionIdentity{}, nil, err
	}
	defer func() { _ = directory.Close() }()
	names, err := p.ReadNames(directory)
	if err != nil {
		return ExecutionIdentity{}, nil, err
	}
	sort.Strings(names)
	corrections := make([]IdentityCorrection, 0, len(names))
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") || !p.ValidSafeSegment(strings.TrimSuffix(name, ".json")) {
			return ExecutionIdentity{}, nil, fmt.Errorf("malformed execution-identity correction filename %q", name)
		}
		correction, err := p.ReadIdentityCorrection(directory, name)
		if err != nil {
			return ExecutionIdentity{}, nil, err
		}
		if correction.Version != 1 || correction.Type != "worktree.execution_identity_corrected" || correction.ClaimID != claim.ClaimID ||
			correction.CorrectionID != strings.TrimSuffix(name, ".json") || correction.Sequence < 1 || correction.At.IsZero() ||
			strings.TrimSpace(correction.Actor) == "" || strings.TrimSpace(correction.Reason) == "" {
			return ExecutionIdentity{}, nil, fmt.Errorf("malformed execution-identity correction %q", name)
		}
		if correction.Model == nil && correction.CLI == nil && correction.Provider == nil {
			return ExecutionIdentity{}, nil, fmt.Errorf("execution-identity correction %q changes no field", name)
		}
		if correction.Model != nil && (strings.TrimSpace(*correction.Model) == "" || !ValidExecutionIdentifier(*correction.Model, true)) {
			return ExecutionIdentity{}, nil, fmt.Errorf("execution-identity correction %q has invalid model", name)
		}
		for _, value := range []*string{correction.CLI, correction.Provider} {
			if value != nil && *value != "" && !ValidExecutionIdentifier(*value, false) {
				return ExecutionIdentity{}, nil, fmt.Errorf("execution-identity correction %q has invalid route", name)
			}
		}
		corrections = append(corrections, correction)
	}
	sort.Slice(corrections, func(i, j int) bool { return corrections[i].Sequence < corrections[j].Sequence })
	previous := ""
	for index, correction := range corrections {
		if correction.Sequence != index+1 || correction.PredecessorID != previous {
			return ExecutionIdentity{}, nil, fmt.Errorf("execution-identity correction chain is forked, incomplete, or cyclic")
		}
		if correction.Model != nil {
			identity.Model = *correction.Model
			identity.ModelDeclaredBy = correction.Actor
			if identity.Model == "unknown" {
				identity.ModelProvenance = ModelProvenanceUnknown
			} else {
				identity.ModelProvenance = ModelProvenanceCallerDeclared
			}
		}
		if correction.CLI != nil {
			identity.CLI = *correction.CLI
		}
		if correction.Provider != nil {
			identity.Provider = *correction.Provider
		}
		identity.CorrectionIDs = append(identity.CorrectionIDs, correction.CorrectionID)
		previous = correction.CorrectionID
	}
	return identity, corrections, nil
}
