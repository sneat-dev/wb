package ciaudit

import (
	"fmt"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/quality"
)

// CompareUnitTierPendingTotal implements spec/plans/coverage-to-100/README.md
// task-24's cross-PR ratchet on internal/quality/testdata/unit_tier.pending:
// the list's grand total may not rise against the base branch's committed
// copy of the same file, unless the same PR shrinks other entries by at
// least as much (the PR that creates the list is exempt, since there is no
// prior copy to compare against).
//
// It follows CompareCoverageFloors's own shape exactly (same package, same
// file, same "fetch origin/<target> and git show its copy" technique,
// task-24's own instruction to "reuse the wb ci audit --target pattern"):
// a deliberate no-op when root's current branch already equals target, and
// the one function in this file that is not read-only, because "the fetched
// target" is a comparison this package cannot make from local files alone.
func CompareUnitTierPendingTotal(root, target string) ([]Finding, error) {
	if target == "" {
		return nil, nil
	}
	currentBranch, err := gitOutput(root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("determine the current branch: %w", err)
	}
	if currentBranch == target {
		return nil, nil
	}
	if _, err := gitOutput(root, "fetch", "--quiet", "origin", target); err != nil {
		return nil, fmt.Errorf("fetch origin/%s: %w", target, err)
	}
	targetRef := "origin/" + target

	const relativePath = "internal/quality/testdata/unit_tier.pending"
	localEntries, err := quality.ParseUnitTierPending(filepath.Join(root, filepath.FromSlash(relativePath)))
	if err != nil {
		return nil, fmt.Errorf("parse local %s: %w", relativePath, err)
	}
	localTotal := quality.UnitTierPendingTotal(localEntries)

	targetContent, err := gitOutput(root, "show", targetRef+":"+relativePath)
	if err != nil {
		// The file does not exist on the target -- this PR creates it, and
		// task-24 exempts the creating PR from the ratchet.
		return nil, nil
	}
	targetEntries, err := quality.ParseUnitTierPendingBytes([]byte(targetContent), targetRef+":"+relativePath)
	if err != nil {
		return nil, fmt.Errorf("parse %s:%s: %w", targetRef, relativePath, err)
	}
	targetTotal := quality.UnitTierPendingTotal(targetEntries)

	if localTotal > targetTotal {
		return []Finding{{
			Code: "unit-tier-pending-total-rose",
			Message: fmt.Sprintf(
				"unit_tier.pending's total match count rose from %d (on %s) to %d; shrink another entry by at least as much in this PR",
				targetTotal, target, localTotal,
			),
			File: relativePath,
		}}, nil
	}
	return nil, nil
}
