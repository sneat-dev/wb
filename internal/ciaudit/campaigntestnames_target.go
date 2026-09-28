package ciaudit

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/quality"
)

// CompareCampaignTestNamesPendingTotal implements
// spec/plans/coverage-to-100/README.md task-21's cross-PR ratchet on
// internal/quality/testdata/campaign_test_names.pending (AGENTS.md:108-112):
// the list's grand total may not rise against the base branch's committed
// copy, unless the same PR shrinks other entries by at least as much (the
// PR that creates the list is exempt, since there is no prior copy to
// compare against).
//
// It follows CompareUnitTierPendingTotal's and CompareExecSitesPendingTotal's
// own shape exactly -- same technique ("fetch origin/<target> and git show
// its copy"), same git-agnostic core/exported-wrapper split so a unit test
// can substitute a fake instead of a real repository, reusing task-24's and
// task-8's code shape rather than inventing a third style for a naming
// rule's own ratchet. The real git calls are gitOutput, the same helper the
// other two comparators use; compareCampaignTestNamesPendingTotal below is
// the testable core.
func CompareCampaignTestNamesPendingTotal(root, target string) ([]Finding, error) {
	return compareCampaignTestNamesPendingTotal(root, target, gitOutput)
}

// compareCampaignTestNamesPendingTotal is
// CompareCampaignTestNamesPendingTotal's git-agnostic core: every git call
// goes through git rather than the package-level gitOutput, so a unit test
// can substitute a fake that never spawns a process or touches a real
// repository.
func compareCampaignTestNamesPendingTotal(root, target string, git unitTierGitRunner) ([]Finding, error) {
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

	const relativePath = "internal/quality/testdata/campaign_test_names.pending"
	localEntries, err := quality.ParseCampaignTestNamesPending(filepath.Join(root, filepath.FromSlash(relativePath)))
	if err != nil {
		return nil, fmt.Errorf("parse local %s: %w", relativePath, err)
	}
	localTotal := quality.CampaignTestNamesPendingTotal(localEntries)

	targetContent, err := git(root, "show", targetRef+":"+relativePath)
	if err != nil {
		if isGitShowPathMissingOnTarget(err) {
			// The file does not exist on the target -- this PR creates it,
			// and task-21 exempts the creating PR from the ratchet, the same
			// exemption task-24 and task-8 give their own pending lists'
			// creating PR.
			return nil, nil
		}
		return nil, fmt.Errorf("show %s:%s: %w", targetRef, relativePath, err)
	}
	targetEntries, err := quality.ParseCampaignTestNamesPendingBytes([]byte(targetContent), targetRef+":"+relativePath)
	if err != nil {
		return nil, fmt.Errorf("parse %s:%s: %w", targetRef, relativePath, err)
	}
	targetTotal := quality.CampaignTestNamesPendingTotal(targetEntries)

	if localTotal > targetTotal {
		return []Finding{{
			Code: "campaign-test-names-pending-total-rose",
			Message: fmt.Sprintf(
				"campaign_test_names.pending's total match count rose from %d (on %s) to %d; shrink another entry by at least as much in this PR",
				targetTotal, target, localTotal,
			),
			File: relativePath,
		}}, nil
	}
	return nil, nil
}
