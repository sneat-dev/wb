package ciaudit

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/quality"
)

// CompareExecSitesPendingTotal implements spec/plans/coverage-to-100/README.md
// task-8's cross-PR ratchet on internal/quality/testdata/exec_sites.pending:
// the list's grand total may not rise against the base branch's committed
// copy, unless the same PR shrinks other entries by at least as much (the PR
// that creates the list is exempt, since there is no prior copy to compare
// against).
//
// It follows CompareUnitTierPendingTotal's own shape exactly -- same
// technique ("fetch origin/<target> and git show its copy"), same
// git-agnostic core/exported-wrapper split so a unit test can substitute a
// fake instead of a real repository, reusing task-24's code shape per the
// task-8 brief rather than inventing a second style. The real git calls are
// gitOutput, the same helper CompareCoverageFloors and
// CompareUnitTierPendingTotal use; compareExecSitesPendingTotal below is the
// testable core.
func CompareExecSitesPendingTotal(root, target string) ([]Finding, error) {
	return compareExecSitesPendingTotal(root, target, gitOutput)
}

// compareExecSitesPendingTotal is CompareExecSitesPendingTotal's git-agnostic
// core: every git call goes through git rather than the package-level
// gitOutput, so a unit test can substitute a fake that never spawns a
// process or touches a real repository.
func compareExecSitesPendingTotal(root, target string, git unitTierGitRunner) ([]Finding, error) {
	if strings.TrimSpace(target) == "" {
		return nil, nil
	}
	currentBranch, err := git(root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("determine the current branch: %w", err)
	}
	if currentBranch == target {
		return nil, nil
	}
	if _, err := git(root, "fetch", "--quiet", "origin", target); err != nil {
		return nil, fmt.Errorf("fetch origin/%s: %w", target, err)
	}
	targetRef := "origin/" + target

	const relativePath = "internal/quality/testdata/exec_sites.pending"
	localEntries, err := quality.ParseExecSitesPending(filepath.Join(root, filepath.FromSlash(relativePath)))
	if err != nil {
		return nil, fmt.Errorf("parse local %s: %w", relativePath, err)
	}
	localTotal := quality.ExecSitesPendingTotal(localEntries)

	targetContent, err := git(root, "show", targetRef+":"+relativePath)
	if err != nil {
		if isGitShowPathMissingOnTarget(err) {
			// The file does not exist on the target -- this PR creates it,
			// and task-8 exempts the creating PR from the ratchet, the same
			// exemption task-24 gives unit_tier.pending's creating PR.
			return nil, nil
		}
		return nil, fmt.Errorf("show %s:%s: %w", targetRef, relativePath, err)
	}
	targetEntries, err := quality.ParseExecSitesPendingBytes([]byte(targetContent), targetRef+":"+relativePath)
	if err != nil {
		return nil, fmt.Errorf("parse %s:%s: %w", targetRef, relativePath, err)
	}
	targetTotal := quality.ExecSitesPendingTotal(targetEntries)

	if localTotal > targetTotal {
		return []Finding{{
			Code: "exec-sites-pending-total-rose",
			Message: fmt.Sprintf(
				"exec_sites.pending's total match count rose from %d (on %s) to %d; shrink another entry by at least as much in this PR",
				targetTotal, target, localTotal,
			),
			File: relativePath,
		}}, nil
	}
	return nil, nil
}
