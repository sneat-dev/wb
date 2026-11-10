package worktrees

import (
	"context"
	"errors"
	"fmt"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

// Preservation does not assert that work landed or restore an unusable claim
// to active service. It backs up both histories and seals the original claim as
// retired. Every identity and custody check still applies. In particular, a
// Git failure (missing object, corrupt repository, etc.) is not divergence.
func corroborateRetirementClaim(home, worktree, head string, projection workLogProjection, claim workLogClaim) error {
	err := corroborateClaimAtPath(home, worktree, head, projection, claim)
	var divergence *claimBaseAncestryError
	if !errors.As(err, &divergence) {
		return err
	}
	ancestor, gitErr := isAncestor(context.Background(), worktree, claim.BaseSHA, head)
	if gitErr != nil || ancestor {
		return err
	}
	return nil
}

// Only the exact ancestry diagnostic that preservation can independently
// corroborate is recoverable. Other malformed candidates remain blocking.
func preservationDiagnostics(home string, diagnostics []ListDiagnostic) []ListDiagnostic {
	kept := diagnostics[:0]
	for _, diagnostic := range diagnostics {
		if !preservableBaseDiagnostic(home, diagnostic) {
			kept = append(kept, diagnostic)
		}
	}
	return kept
}

func corroborateRetirementProjection(home, worktree string, projection workLogProjection) error {
	run, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return err
	}
	defer func() { _ = run.Close() }()
	claim, err := readWorkLogClaimAt(run, projection.ClaimID)
	if err != nil {
		return err
	}
	head, err := git(context.Background(), worktree, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	return corroborateRetirementClaim(home, worktree, head, projection, claim)
}

func sealRetirementWorkLog(home, worktree, head string) error {
	ports := defaultRecycleSealPorts()
	ports.corroborate = corroborateRetirementClaim
	return ports.sealWorkLogForRecycleWithEvidence(home, worktree, head, "retired", worktreeclaims.TerminalEvidence{})
}

func readRetirementTerminal(home, worktree string) (*workLogTerminalRecord, error) {
	record, err := readWorkLogTerminalRecordReadOnly(home, worktree)
	var divergence *claimBaseAncestryError
	if !errors.As(err, &divergence) {
		return record, err
	}
	recovered, recoveryErr := (worktreeclaims.TerminalReadPorts{
		ReadProjectionReadOnly: readWorkLogProjectionForReadOnlyClaim,
		ProjectionMissing:      func(err error) bool { return errors.Is(err, errWorkLogProjectionNotFound) },
		Corroborate:            corroborateRetirementProjection,
		OpenRun:                openWorkLogRun,
		ReadTerminalAt:         readWorkLogTerminalAt,
	}).ReadTerminal(home, worktree, true)
	if recoveryErr != nil || recovered == nil || recovered.Disposition != "retired" {
		return nil, err
	}
	return recovered, nil
}

func preservableBaseDiagnostic(home string, diagnostic ListDiagnostic) bool {
	if diagnostic.DiscoveryScope != "selected-proof" {
		return false
	}
	projection, err := readWorkLogProjectionForReadOnlyClaim(diagnostic.Path)
	if err != nil {
		return false
	}
	proofErr := corroborateProjectionWithPrivateClaim(home, diagnostic.Path, projection)
	var divergence *claimBaseAncestryError
	if !errors.As(proofErr, &divergence) || diagnostic.Message != fmt.Sprintf("corroborate selected current projection: %v", proofErr) {
		return false
	}
	_, _, err = retireReadClaim(home, diagnostic.Path)
	return err == nil
}
