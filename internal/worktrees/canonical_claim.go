package worktrees

import (
	"github.com/sneat-dev/wb/internal/worktreeclaims"
	"time"
)

// Canonical-claim admission remains unshipped. Its approved reader and lease
// extension prototype are preserved in canonical_claim_support_test.go with
// their contract tests; this file retains the production claim-format contract.

// Claim modes. The empty mode is the default and means ClaimModeWorktree.
const (
	ClaimModeWorktree  = worktreeclaims.ClaimModeWorktree
	ClaimModeCanonical = worktreeclaims.ClaimModeCanonical
)

// validateClaimModeLease is the mode/lease contract used before a claim is
// written. Reading an immutable claim checks the same contract through
// worktreeclaims.ValidateClaimModeLease.
func validateClaimModeLease(mode string, lease, recordedAt time.Time) error {
	return worktreeclaims.ValidateClaimModeLease(mode, lease, recordedAt)
}

func claimLeaseExpiry(lease time.Time) *time.Time {
	if lease.IsZero() {
		return nil
	}
	utc := lease.UTC()
	return &utc
}
