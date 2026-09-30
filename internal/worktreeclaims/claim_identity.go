package worktreeclaims

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/sneat-dev/wb/internal/worktreelayout"
)

const (
	ModelProvenanceRuntimeObserved = "runtime_observed"
	ModelProvenanceCallerDeclared  = "caller_declared"
	ModelProvenanceUnknown         = "unknown"
)

type ExecutionIdentity struct {
	Model           string   `json:"model"`
	ModelProvenance string   `json:"model_provenance"`
	ModelDeclaredBy string   `json:"model_declared_by,omitempty"`
	CLI             string   `json:"cli,omitempty"`
	Provider        string   `json:"provider,omitempty"`
	CorrectionIDs   []string `json:"correction_ids,omitempty"`
}
type ClaimExecutionIdentity struct{ Model, CLI, Provider string }
type CorrectionIdentity struct {
	EffortID, RunID, ClaimID, EventID, Actor, Reason string
	Model, CLI, Provider                             *string
}
type ClaimIdentity struct {
	Version                                                int
	EffortID, Repository, Worktree, Branch, Base, BaseSHA  string
	Model, ModelProvenance, ModelDeclaredBy, CLI, Provider string
	ParentClaimID, AcquiredVia, AgentID                    string
}

var executionIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+-]{0,127}$`)

func ValidateNewExecutionIdentity(identity ClaimExecutionIdentity) error {
	model := strings.TrimSpace(identity.Model)
	if model == "" {
		return fmt.Errorf("--model is required for every new Work Log claim; pass the exact child model or the explicit value unknown")
	}
	if !ValidExecutionIdentifier(model, true) {
		return fmt.Errorf("model %q must be a non-secret execution identifier or explicit unknown", model)
	}
	for _, field := range []struct{ name, value string }{{"cli", identity.CLI}, {"provider", identity.Provider}} {
		value := strings.TrimSpace(field.value)
		if value != "" && !ValidExecutionIdentifier(value, false) {
			return fmt.Errorf("%s %q must be a non-secret execution identifier", field.name, value)
		}
	}
	return nil
}
func ValidateCorrectionIdentity(options CorrectionIdentity) error {
	if !worktreelayout.ValidSafeSegment(options.EffortID) || !worktreelayout.ValidSafeSegment(options.RunID) || !ValidClaimID(options.ClaimID) || !worktreelayout.ValidSafeSegment(options.EventID) {
		return fmt.Errorf("effort, run, claim, and correction event ID must be valid exact Work Log identifiers")
	}
	if strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "" {
		return fmt.Errorf("--actor and --reason are required for an execution-identity correction")
	}
	if options.Model == nil && options.CLI == nil && options.Provider == nil {
		return fmt.Errorf("select at least one of --model, --cli, or --provider to correct")
	}
	if options.Model != nil {
		model := strings.TrimSpace(*options.Model)
		if model == "" || !ValidExecutionIdentifier(model, true) {
			return fmt.Errorf("corrected model must be an exact non-secret identifier or explicit unknown")
		}
	}
	for _, field := range []struct {
		name  string
		value *string
	}{{"cli", options.CLI}, {"provider", options.Provider}} {
		if field.value != nil && strings.TrimSpace(*field.value) != "" && !ValidExecutionIdentifier(strings.TrimSpace(*field.value), false) {
			return fmt.Errorf("corrected %s must be a bounded non-secret execution identifier, or an explicit empty value to clear it", field.name)
		}
	}
	return nil
}
func ValidExecutionIdentifier(value string, allowUnknown bool) bool {
	return validExecutionIdentifier(value, allowUnknown)
}
func validExecutionIdentifier(value string, allowUnknown bool) bool {
	if value == "unknown" {
		return allowUnknown
	}
	if !executionIdentifier.MatchString(value) {
		return false
	}
	lower := strings.ToLower(value)
	return !strings.ContainsAny(value, "@=?#") && !strings.Contains(lower, "token") && !strings.Contains(lower, "secret") && !strings.Contains(lower, "password") && !hasCredentialPrefix(lower)
}
func hasCredentialPrefix(lower string) bool {
	for _, prefix := range []string{"sk-", "sk_", "rk_live_", "bearer", "ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "glpat-", "xoxa-", "xoxb-", "xoxp-", "xoxr-", "npm_", "pypi-", "hf_", "ops_", "akia", "aiza", "eyj"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}
func DeclaredBy(options Options) string {
	if value := strings.TrimSpace(options.Initiator); value != "" {
		return value
	}
	if value := strings.TrimSpace(options.AgentID); value != "" {
		return value
	}
	return "unknown"
}
func IdentityFromClaim(claim ClaimIdentity) ExecutionIdentity {
	model := strings.TrimSpace(claim.Model)
	provenance := strings.TrimSpace(claim.ModelProvenance)
	if model == "" {
		model, provenance = "unknown", ModelProvenanceUnknown
	}
	if provenance == "" {
		if model == "unknown" {
			provenance = ModelProvenanceUnknown
		} else {
			provenance = ModelProvenanceRuntimeObserved
		}
	}
	return ExecutionIdentity{Model: model, ModelProvenance: provenance, ModelDeclaredBy: claim.ModelDeclaredBy, CLI: claim.CLI, Provider: claim.Provider}
}
func WorkLogClaimID(effort string, result CreationResult) string {
	return workLogClaimID(effort, result)
}
func workLogClaimID(effort string, result CreationResult) string {
	hash := sha256.New()
	for _, value := range []string{effort, result.Repository, result.Branch, result.Base, result.BaseSHA} {
		_, _ = io.WriteString(hash, fmt.Sprintf("%d:", len(value)))
		_, _ = io.WriteString(hash, value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
func SuccessorWorkLogClaimID(parentClaimID, successor, disposition string) string {
	hash := sha256.New()
	for _, value := range []string{"successor", parentClaimID, successor, disposition} {
		_, _ = io.WriteString(hash, fmt.Sprintf("%d:", len(value)))
		_, _ = io.WriteString(hash, value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
func DeclaredSuccessorWorkLogClaimID(parentClaimID, successor, disposition string, identity ClaimExecutionIdentity) string {
	hash := sha256.New()
	for _, value := range []string{"successor-execution-identity-v2", parentClaimID, successor, disposition, strings.TrimSpace(identity.Model), strings.TrimSpace(identity.CLI), strings.TrimSpace(identity.Provider)} {
		_, _ = io.WriteString(hash, fmt.Sprintf("%d:", len(value)))
		_, _ = io.WriteString(hash, value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
func ExpectedWorkLogClaimID(claim ClaimIdentity, external, parked func() (string, error)) (string, error) {
	if claim.ParentClaimID == "" {
		return WorkLogClaimID(claim.EffortID, CreationResult{Repository: claim.Repository, WorktreeDir: claim.Worktree, Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA}), nil
	}
	switch claim.AcquiredVia {
	case "external_handoff":
		return external()
	case "parked_session_resume":
		return parked()
	case "recycle_failed":
		return SuccessorWorkLogClaimID(claim.ParentClaimID, claim.AgentID, claim.AcquiredVia), nil
	case "handoff", "not_landed":
		if claim.Version == 2 {
			return DeclaredSuccessorWorkLogClaimID(claim.ParentClaimID, claim.AgentID, claim.AcquiredVia, ClaimExecutionIdentity{Model: claim.Model, CLI: claim.CLI, Provider: claim.Provider}), nil
		}
		return SuccessorWorkLogClaimID(claim.ParentClaimID, claim.AgentID, claim.AcquiredVia), nil
	default:
		return "", fmt.Errorf("successor claim acquisition %q is invalid", claim.AcquiredVia)
	}
}
func ValidClaimID(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
