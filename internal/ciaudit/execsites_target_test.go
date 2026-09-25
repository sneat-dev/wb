package ciaudit

import (
	"errors"
	"testing"
)

// execSitesPendingPath is exec_sites.pending's repo-relative path, the
// task-8 analogue of unittier_target_test.go's unitTierPendingPath. Defined
// in this default-tier file (not the //go:build e2e contract test) so both
// tiers can use it.
const execSitesPendingPath = "internal/quality/testdata/exec_sites.pending"

// These tests mirror unittier_target_test.go's coverage of
// compareUnitTierPendingTotal, applied to compareExecSitesPendingTotal
// (task-8's own cross-PR ratchet on exec_sites.pending): same fake git
// double (fakeUnitTierGit, already generic over any unitTierGitRunner
// consumer), same false-positive shapes, same error paths. Duplicating the
// table here, rather than parameterising both target functions over one
// shared test, keeps each ratchet's own file:line failure message pinned
// independently, the same way task-24's own tests are not shared with any
// other list.

// TestCompareExecSitesPendingTotalReportsARisingTotal pins task-8's cross-PR
// ratchet: exec_sites.pending's grand total may not rise against the base
// branch's committed copy.
func TestCompareExecSitesPendingTotalReportsARisingTotal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, execSitesPendingPath, "a.go\t3\ttask-8\nb.go\t1\ttask-16\n")
	git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showContent: "a.go\t3\ttask-8\n"}

	findings, err := compareExecSitesPendingTotal(root, "main", git.run)
	if err != nil {
		t.Fatalf("compareExecSitesPendingTotal: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected exactly one finding, got %+v", findings)
	}
	if findings[0].Code != "exec-sites-pending-total-rose" {
		t.Fatalf("unexpected finding code: %+v", findings[0])
	}
	if findings[0].File != execSitesPendingPath {
		t.Fatalf("unexpected finding file: %+v", findings[0])
	}
}

// TestCompareExecSitesPendingTotalFalsePositives pins the shapes that must
// not report a finding.
func TestCompareExecSitesPendingTotalFalsePositives(t *testing.T) {
	t.Parallel()

	t.Run("an unchanged total", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, execSitesPendingPath, "a.go\t3\ttask-8\n")
		git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showContent: "a.go\t3\ttask-8\n"}

		findings, err := compareExecSitesPendingTotal(root, "main", git.run)
		if err != nil {
			t.Fatalf("compareExecSitesPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("an unchanged total produced findings: %+v", findings)
		}
	})

	t.Run("a total that shrank", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, execSitesPendingPath, "a.go\t1\ttask-8\n")
		git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showContent: "a.go\t3\ttask-8\nb.go\t2\ttask-16\n"}

		findings, err := compareExecSitesPendingTotal(root, "main", git.run)
		if err != nil {
			t.Fatalf("compareExecSitesPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("a shrinking total produced findings: %+v", findings)
		}
	})

	t.Run("one entry grows while another shrinks by at least as much", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, execSitesPendingPath, "a.go\t5\ttask-8\nb.go\t3\ttask-16\n")
		git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showContent: "a.go\t3\ttask-8\nb.go\t5\ttask-16\n"}

		findings, err := compareExecSitesPendingTotal(root, "main", git.run)
		if err != nil {
			t.Fatalf("compareExecSitesPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("an offsetting move produced findings: %+v", findings)
		}
	})

	t.Run("the current branch already is the target", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, execSitesPendingPath, "a.go\t3\ttask-8\n")
		git := &fakeUnitTierGit{t: t, currentBranch: "main"}

		findings, err := compareExecSitesPendingTotal(root, "main", git.run)
		if err != nil {
			t.Fatalf("compareExecSitesPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("auditing main against itself produced findings: %+v", findings)
		}
	})

	t.Run("the pending list is new on this branch (the creating PR)", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, execSitesPendingPath, "a.go\t3\ttask-8\n")
		git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showErr: gitShowPathMissingErr("origin/main", execSitesPendingPath)}

		findings, err := compareExecSitesPendingTotal(root, "main", git.run)
		if err != nil {
			t.Fatalf("compareExecSitesPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("the creating PR produced findings: %+v", findings)
		}
	})

	t.Run("the pending list is new on this branch, tracked but uncommitted on target", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, execSitesPendingPath, "a.go\t3\ttask-8\n")
		git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showErr: gitShowPathExistsOnDiskButNotOnTargetErr("origin/main", execSitesPendingPath)}

		findings, err := compareExecSitesPendingTotal(root, "main", git.run)
		if err != nil {
			t.Fatalf("compareExecSitesPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("the creating PR produced findings: %+v", findings)
		}
	})

	t.Run("an empty target is a no-op", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		git := &fakeUnitTierGit{t: t}

		findings, err := compareExecSitesPendingTotal(root, "", git.run)
		if err != nil {
			t.Fatalf("compareExecSitesPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("an empty target produced findings: %+v", findings)
		}
	})
}

// TestCompareExecSitesPendingTotalRejectsAMalformedLocalPendingFile pins the
// error path for a local exec_sites.pending this repository's own
// ParseExecSitesPending rejects.
func TestCompareExecSitesPendingTotalRejectsAMalformedLocalPendingFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, execSitesPendingPath, "a.go\t-1\ttask-8\n")
	git := &fakeUnitTierGit{t: t, currentBranch: "feature/x"}

	if _, err := compareExecSitesPendingTotal(root, "main", git.run); err == nil {
		t.Fatal("want an error for an invalid local exec_sites.pending")
	}
}

// TestCompareExecSitesPendingTotalRejectsAMalformedTargetPendingFile pins
// the same error path for the fetched target's copy.
func TestCompareExecSitesPendingTotalRejectsAMalformedTargetPendingFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, execSitesPendingPath, "a.go\t1\ttask-8\n")
	git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showContent: "a.go\t-1\ttask-8\n"}

	if _, err := compareExecSitesPendingTotal(root, "main", git.run); err == nil {
		t.Fatal("want an error for an invalid target exec_sites.pending")
	}
}

// TestCompareExecSitesPendingTotalRejectsADeterminingCurrentBranchFailure
// pins the "determine the current branch" error path.
func TestCompareExecSitesPendingTotalRejectsADeterminingCurrentBranchFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, execSitesPendingPath, "a.go\t1\ttask-8\n")
	git := &fakeUnitTierGit{t: t, currentBranchErr: errors.New("not a git repository")}

	if _, err := compareExecSitesPendingTotal(root, "main", git.run); err == nil {
		t.Fatal("want an error when the current branch cannot be determined")
	}
}

// TestCompareExecSitesPendingTotalRejectsAFetchFailure pins the "fetch
// origin/<target>" error path.
func TestCompareExecSitesPendingTotalRejectsAFetchFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, execSitesPendingPath, "a.go\t1\ttask-8\n")
	git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", fetchErr: errors.New("no origin remote configured")}

	if _, err := compareExecSitesPendingTotal(root, "main", git.run); err == nil {
		t.Fatal("want an error when origin/<target> cannot be fetched")
	}
}

// TestCompareExecSitesPendingTotalRejectsANonMissingShowError pins the other
// half of the creating-PR exemption: a `git show` failure that is not the
// path-missing shape must surface as an error.
func TestCompareExecSitesPendingTotalRejectsANonMissingShowError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, execSitesPendingPath, "a.go\t1\ttask-8\n")
	git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showErr: errors.New("fatal: unable to access origin: network error")}

	if _, err := compareExecSitesPendingTotal(root, "main", git.run); err == nil {
		t.Fatal("want an error for a non-path-missing git show failure")
	}
}

// TestCompareExecSitesPendingTotalExportedWrapperEmptyTarget covers
// CompareExecSitesPendingTotal itself (not just its
// compareExecSitesPendingTotal core): an empty target short-circuits before
// any git call, so this needs no real repository and no fake.
func TestCompareExecSitesPendingTotalExportedWrapperEmptyTarget(t *testing.T) {
	t.Parallel()
	findings, err := CompareExecSitesPendingTotal(t.TempDir(), "")
	if err != nil {
		t.Fatalf("CompareExecSitesPendingTotal: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("an empty target produced findings: %+v", findings)
	}
}

// TestCompareAgainstTargetWiresAllThreeRealComparators pins
// CompareAgainstTarget's own exported wrapper after task-8's third
// comparator was wired in: it must pass CompareCoverageFloors,
// CompareUnitTierPendingTotal and CompareExecSitesPendingTotal, not just
// the first two. An empty target short-circuits all three real comparators
// before any of them touches git, so this needs no real repository.
func TestCompareAgainstTargetWiresAllThreeRealComparators(t *testing.T) {
	t.Parallel()
	findings, err := CompareAgainstTarget(t.TempDir(), "")
	if err != nil {
		t.Fatalf("CompareAgainstTarget: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("an empty target produced findings: %+v", findings)
	}
}

// TestCompareAgainstTargetCombinesThreeComparatorsSorted extends
// TestCompareAgainstTargetCombinesBothComparisonsSorted to three
// comparators, pinning that compareAgainstTarget's variadic signature
// still calls every comparator it is given and still sorts their combined
// findings by Code then File.
func TestCompareAgainstTargetCombinesThreeComparatorsSorted(t *testing.T) {
	t.Parallel()
	floors := &fakeTargetComparator{findings: []Finding{
		{Code: "coverage-floor-lowered", File: ".github/workflows/ci.yml"},
	}}
	unitTier := &fakeTargetComparator{findings: []Finding{
		{Code: "unit-tier-pending-total-rose", File: unitTierPendingPath},
	}}
	execSites := &fakeTargetComparator{findings: []Finding{
		{Code: "exec-sites-pending-total-rose", File: execSitesPendingPath},
	}}

	findings, err := compareAgainstTarget("/root", "main", floors.run, unitTier.run, execSites.run)
	if err != nil {
		t.Fatalf("compareAgainstTarget: %v", err)
	}
	if !floors.called || !unitTier.called || !execSites.called {
		t.Fatalf("all three comparators must be called: floors=%t unitTier=%t execSites=%t", floors.called, unitTier.called, execSites.called)
	}
	if len(findings) != 3 {
		t.Fatalf("findings = %+v, want exactly 3", findings)
	}
	if findings[0].Code != "coverage-floor-lowered" || findings[1].Code != "exec-sites-pending-total-rose" || findings[2].Code != "unit-tier-pending-total-rose" {
		t.Fatalf("findings = %+v, want coverage-floor-lowered, exec-sites-pending-total-rose, unit-tier-pending-total-rose in Code order", findings)
	}
}

// TestCompareAgainstTargetStopsAtTheFirstFailingComparator pins that a
// later comparator in the variadic list never runs once an earlier one has
// failed, the N-comparator generalisation of
// TestCompareAgainstTargetPropagatesAPendingError.
func TestCompareAgainstTargetStopsAtTheFirstFailingComparator(t *testing.T) {
	t.Parallel()
	floors := &fakeTargetComparator{}
	unitTier := &fakeTargetComparator{err: errors.New("unit-tier comparator failed")}
	execSites := &fakeTargetComparator{}

	if _, err := compareAgainstTarget("/root", "main", floors.run, unitTier.run, execSites.run); err == nil {
		t.Fatal("want an error when the unit-tier comparator fails")
	}
	if !floors.called || !unitTier.called {
		t.Fatalf("floors and unit-tier comparators must run: floors=%t unitTier=%t", floors.called, unitTier.called)
	}
	if execSites.called {
		t.Fatal("the exec-sites comparator must not run once an earlier comparator has failed")
	}
}
