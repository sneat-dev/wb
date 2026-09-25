package ciaudit

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/quality"
)

// unitTierGitRunner is CompareUnitTierPendingTotal's own consumer-side port
// onto "run one read-only git command in dir and return trimmed stdout" --
// gitOutput's exact signature. Defining it here, rather than calling
// gitOutput directly, lets compareUnitTierPendingTotal's tests substitute a
// fake instead of building real git repositories (review note #764 B4: "Give
// CompareUnitTierPendingTotal a small git-reading port ... test it against a
// fake, and drop its pending entry" -- the real-git-repo tests it replaces
// added 46 matches of their own to internal/quality/testdata/unit_tier.pending,
// which is the exact debt task-24 exists to stop growing).
type unitTierGitRunner func(dir string, arguments ...string) (string, error)

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
// one of the two functions in this package that are not read-only, because
// "the fetched target" is a comparison this package cannot make from local
// files alone. The real git calls are gitOutput, the same helper
// CompareCoverageFloors uses; compareUnitTierPendingTotal below is the
// testable core.
func CompareUnitTierPendingTotal(root, target string) ([]Finding, error) {
	return compareUnitTierPendingTotal(root, target, gitOutput)
}

// compareUnitTierPendingTotal is CompareUnitTierPendingTotal's git-agnostic
// core: every git call goes through git rather than the package-level
// gitOutput, so a unit test can substitute a fake that never spawns a
// process or touches a real repository.
func compareUnitTierPendingTotal(root, target string, git unitTierGitRunner) ([]Finding, error) {
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

	const relativePath = "internal/quality/testdata/unit_tier.pending"
	localEntries, err := quality.ParseUnitTierPending(filepath.Join(root, filepath.FromSlash(relativePath)))
	if err != nil {
		return nil, fmt.Errorf("parse local %s: %w", relativePath, err)
	}
	localTotal := quality.UnitTierPendingTotal(localEntries)

	targetContent, err := git(root, "show", targetRef+":"+relativePath)
	if err != nil {
		if isGitShowPathMissingOnTarget(err) {
			// The file does not exist on the target -- this PR creates it,
			// and task-24 exempts the creating PR from the ratchet. Any
			// other git error (network, auth, a target branch that does not
			// exist at all) is a real failure and must not be swallowed the
			// same way (review note #764: "only treat `git show` 'path does
			// not exist on target' as the creating-PR exemption; any other
			// git error must fail").
			return nil, nil
		}
		return nil, fmt.Errorf("show %s:%s: %w", targetRef, relativePath, err)
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

// isGitShowPathMissingOnTarget reports whether err is `git show`'s own
// "the path does not exist on this ref" failure, as opposed to any other
// git error (network, auth, an unknown ref). `git show <ref>:<path>` fails
// with one of two messages depending on whether the path exists anywhere in
// the working tree, and this function's own author confirmed both against
// real git rather than assuming one: a path that never existed in the
// repository's history at all ("fatal: path '<path>' does not exist in
// '<ref>'"), and a path that exists on disk on the current branch but was
// never committed on the target ref ("fatal: path '<path>' exists on disk,
// but not in '<ref>'") -- exactly task-24's own creating-PR shape, since a
// freshly authored unit_tier.pending sits on disk, tracked on this branch,
// before the target branch has ever seen it. Both mean the same thing for
// this comparison: the path is simply absent from that commit -- the one
// case task-24 exempts as "this PR creates the file".
func isGitShowPathMissingOnTarget(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "does not exist in") || strings.Contains(message, "exists on disk, but not in")
}

// targetComparator is one `wb ci audit --target` cross-branch comparison's
// own signature -- CompareCoverageFloors's and CompareUnitTierPendingTotal's
// shared shape. compareAgainstTarget takes both as parameters so its own
// tests can substitute fakes: composition, sorting and error propagation are
// the only things it does, and none of that needs a real comparison, let
// alone a real Git repository, to exercise (review note #764 B6).
type targetComparator func(root, target string) ([]Finding, error)

// CompareAgainstTarget runs every `wb ci audit --target` cross-branch
// comparison this package owns -- CompareCoverageFloors,
// CompareUnitTierPendingTotal and CompareExecSitesPendingTotal -- against
// root and target, and returns their findings combined and sorted the same
// way cmd/wb/ci.go already sorts a target comparison's findings (by Code,
// then File). Callers that want all three comparisons call this one
// function instead of calling each separately, so adding a further such
// comparison in the future costs this package a new call here, not a new
// call site in cmd/wb (review note #764 B5: keep cmd/wb's statement count
// over this path unchanged; task-8 reuses the same seam for its own
// exec-sites ratchet rather than adding a parallel call site).
func CompareAgainstTarget(root, target string) ([]Finding, error) {
	return compareAgainstTarget(root, target, CompareCoverageFloors, CompareUnitTierPendingTotal, CompareExecSitesPendingTotal)
}

// compareAgainstTarget is CompareAgainstTarget's comparator-agnostic core.
func compareAgainstTarget(root, target string, comparators ...targetComparator) ([]Finding, error) {
	var findings []Finding
	for _, compare := range comparators {
		comparatorFindings, err := compare(root, target)
		if err != nil {
			return nil, err
		}
		findings = append(findings, comparatorFindings...)
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Code == findings[j].Code {
			return findings[i].File < findings[j].File
		}
		return findings[i].Code < findings[j].Code
	})
	return findings, nil
}
