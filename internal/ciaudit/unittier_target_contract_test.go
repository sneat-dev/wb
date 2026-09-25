//go:build e2e

package ciaudit

import "testing"

// TestContractCompareAgainstTargetRealGit is task-24's contract test for
// CompareAgainstTarget: the plan's "contract tests beside each real adapter"
// shape (spec/plans/coverage-to-100/README.md), run only in the e2e tier
// (`go test -tags e2e -run '^Test(E2E|Contract)' ./...`), never the default
// one. compareAgainstTarget's own composition, sorting and error-propagation
// behaviour is pinned against fakes in unittier_target_test.go (review note
// #764 B6); this test is the one place that still proves the real thing --
// CompareCoverageFloors, CompareUnitTierPendingTotal and (task-8)
// CompareExecSitesPendingTotal running against a real Git repository -- end
// to end through the exported CompareAgainstTarget wrapper.
// exec_sites.pending is seeded identically on both commits (unchanged
// total) so this test keeps pinning exactly the two findings the floor and
// unit-tier-pending changes below produce, without also asserting on
// task-8's ratchet here -- that ratchet gets its own coverage in
// TestContractCompareExecSitesPendingTotalRealGit below.
func TestContractCompareAgainstTargetRealGit(t *testing.T) {
	t.Parallel()
	fixture := newTargetFixture(t, "85")
	write(t, fixture.Root, unitTierPendingPath, "a_test.go\t3\ttask-1\n")
	write(t, fixture.Root, execSitesPendingPath, "pkg/a.go\t1\ttask-8\n")
	targetGit(t, fixture.Root, "add", "-A")
	targetGit(t, fixture.Root, "commit", "-qm", "seed the pending lists")
	targetGit(t, fixture.Root, "push", "-q", "origin", "main")

	targetGit(t, fixture.Root, "checkout", "-qb", "feature/x")
	write(t, fixture.Root, ".github/workflows/ci.yml", `
jobs:
  build:
    with:
      min_test_coverage_percent: 80
`)
	write(t, fixture.Root, unitTierPendingPath, "a_test.go\t3\ttask-1\nb_test.go\t1\ttask-2\n")
	targetGit(t, fixture.Root, "add", "-A")
	targetGit(t, fixture.Root, "commit", "-qm", "lower the floor and grow the pending total")

	findings, err := CompareAgainstTarget(fixture.Root, "main")
	if err != nil {
		t.Fatalf("CompareAgainstTarget: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %+v, want exactly 2 (one floor, one pending)", findings)
	}
	if findings[0].Code != "coverage-floor-lowered" || findings[1].Code != "unit-tier-pending-total-rose" {
		t.Fatalf("findings = %+v, want coverage-floor-lowered before unit-tier-pending-total-rose", findings)
	}
}

// TestContractCompareExecSitesPendingTotalRealGit is task-8's own contract
// test for CompareExecSitesPendingTotal, the exec_sites.pending analogue of
// TestContractCompareAgainstTargetRealGit's unit_tier.pending coverage: a
// real Git repository, a total that rises between the target commit and the
// current branch, reported through the exported wrapper.
func TestContractCompareExecSitesPendingTotalRealGit(t *testing.T) {
	t.Parallel()
	fixture := newTargetFixture(t, "85")
	write(t, fixture.Root, execSitesPendingPath, "pkg/a.go\t1\ttask-8\n")
	targetGit(t, fixture.Root, "add", "-A")
	targetGit(t, fixture.Root, "commit", "-qm", "seed the exec-sites pending list")
	targetGit(t, fixture.Root, "push", "-q", "origin", "main")

	targetGit(t, fixture.Root, "checkout", "-qb", "feature/x")
	write(t, fixture.Root, execSitesPendingPath, "pkg/a.go\t1\ttask-8\npkg/b.go\t2\ttask-16\n")
	targetGit(t, fixture.Root, "add", "-A")
	targetGit(t, fixture.Root, "commit", "-qm", "grow the exec-sites pending total")

	findings, err := CompareExecSitesPendingTotal(fixture.Root, "main")
	if err != nil {
		t.Fatalf("CompareExecSitesPendingTotal: %v", err)
	}
	if len(findings) != 1 || findings[0].Code != "exec-sites-pending-total-rose" {
		t.Fatalf("findings = %+v, want exactly one exec-sites-pending-total-rose finding", findings)
	}
}
