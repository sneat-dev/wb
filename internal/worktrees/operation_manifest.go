package worktrees

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

// EnsureOperationManifest preserves legacy bytes while checking that populated
// creation identity agrees with the authenticated or native-recovered operation.
func EnsureOperationManifest(worktree string, expected Manifest) error {
	return ensureOperationManifestWithPorts(worktree, expected, claimJournalPorts())
}

func ensureOperationManifestWithPorts(worktree string, expected Manifest, ports worktreeclaims.Ports) error {
	existing, err := ports.ReadManifest(worktree)
	if errors.Is(err, errManifestNotFound) {
		if err := ports.EnsureManifest(worktree, expected); err != nil {
			return err
		}
		// EnsureManifest accepts an immutable collision. Authenticate the actual
		// winner before the caller can publish any prompt or private claim.
		existing, err = ports.ReadManifest(worktree)
	}
	if err != nil {
		return err
	}
	return validateOperationManifestIdentity(existing, expected)
}

func validateOperationManifestIdentity(existing, expected Manifest) error {
	if existing.EffortID != expected.EffortID || existing.Repository != expected.Repository || existing.Branch != expected.Branch {
		return errors.New("existing immutable manifest does not match the operation checkout identity")
	}
	if existing.Worktree != "" && filepath.Clean(existing.Worktree) != filepath.Clean(expected.Worktree) {
		return errors.New("existing immutable manifest does not match the operation checkout path")
	}
	for _, field := range []struct{ name, existing, expected string }{
		{"base", existing.Base, expected.Base}, {"base SHA", existing.BaseSHA, expected.BaseSHA},
		{"run", existing.RunID, expected.RunID}, {"claim", existing.ClaimID, expected.ClaimID},
	} {
		if field.existing != "" && field.existing != field.expected {
			return fmt.Errorf("existing immutable manifest has a different %s", field.name)
		}
	}
	return nil
}
