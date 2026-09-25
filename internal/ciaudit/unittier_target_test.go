package ciaudit

import (
	"errors"
	"fmt"
	"testing"
)

const unitTierPendingPath = "internal/quality/testdata/unit_tier.pending"

// fakeUnitTierGit is a unitTierGitRunner test double: it never spawns a
// process or touches a real repository (review note #764 B4), and answers
// each git subcommand compareUnitTierPendingTotal issues from fields set by
// the test. A subcommand this fixture is not configured for is a test bug,
// not a silent success, so it fails the test immediately via t.
type fakeUnitTierGit struct {
	t *testing.T

	currentBranch    string
	currentBranchErr error
	fetchErr         error
	showContent      string
	showErr          error
}

func (f *fakeUnitTierGit) run(_ string, arguments ...string) (string, error) {
	f.t.Helper()
	if len(arguments) == 0 {
		f.t.Fatalf("fakeUnitTierGit: called with no arguments")
	}
	switch arguments[0] {
	case "rev-parse":
		return f.currentBranch, f.currentBranchErr
	case "fetch":
		return "", f.fetchErr
	case "show":
		return f.showContent, f.showErr
	default:
		f.t.Fatalf("fakeUnitTierGit: unexpected git subcommand %q", arguments[0])
		return "", nil
	}
}

// gitShowPathMissingErr is the exact shape `git show <ref>:<path>` fails
// with when path does not exist on ref -- the one error
// compareUnitTierPendingTotal treats as "this PR creates the file",
// isGitShowPathMissingOnTarget's own doc comment.
func gitShowPathMissingErr(ref, path string) error {
	return fmt.Errorf("git show %s:%s: exit status 128: fatal: path '%s' does not exist in '%s'", ref, path, path, ref)
}

// TestCompareUnitTierPendingTotalReportsARisingTotal pins task-24's cross-PR
// ratchet: internal/quality/testdata/unit_tier.pending's grand total may not
// rise against the base branch's committed copy.
func TestCompareUnitTierPendingTotalReportsARisingTotal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, unitTierPendingPath, "a_test.go\t3\ttask-1\nb_test.go\t1\ttask-2\n")
	git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showContent: "a_test.go\t3\ttask-1\n"}

	findings, err := compareUnitTierPendingTotal(root, "main", git.run)
	if err != nil {
		t.Fatalf("compareUnitTierPendingTotal: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected exactly one finding, got %+v", findings)
	}
	if findings[0].Code != "unit-tier-pending-total-rose" {
		t.Fatalf("unexpected finding code: %+v", findings[0])
	}
	if findings[0].File != unitTierPendingPath {
		t.Fatalf("unexpected finding file: %+v", findings[0])
	}
}

// TestCompareUnitTierPendingTotalFalsePositives pins the shapes that must
// not report a finding: an unchanged total, a total that shrank, one entry
// growing while another shrinks by at least as much, the current branch
// already being the target, a missing target copy (the creating PR), and an
// empty target.
func TestCompareUnitTierPendingTotalFalsePositives(t *testing.T) {
	t.Parallel()

	t.Run("an unchanged total", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, unitTierPendingPath, "a_test.go\t3\ttask-1\n")
		git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showContent: "a_test.go\t3\ttask-1\n"}

		findings, err := compareUnitTierPendingTotal(root, "main", git.run)
		if err != nil {
			t.Fatalf("compareUnitTierPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("an unchanged total produced findings: %+v", findings)
		}
	})

	t.Run("a total that shrank", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, unitTierPendingPath, "a_test.go\t1\ttask-1\n")
		git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showContent: "a_test.go\t3\ttask-1\nb_test.go\t2\ttask-2\n"}

		findings, err := compareUnitTierPendingTotal(root, "main", git.run)
		if err != nil {
			t.Fatalf("compareUnitTierPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("a shrinking total produced findings: %+v", findings)
		}
	})

	t.Run("one entry grows while another shrinks by at least as much", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, unitTierPendingPath, "a_test.go\t5\ttask-1\nb_test.go\t3\ttask-2\n")
		git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showContent: "a_test.go\t3\ttask-1\nb_test.go\t5\ttask-2\n"}

		findings, err := compareUnitTierPendingTotal(root, "main", git.run)
		if err != nil {
			t.Fatalf("compareUnitTierPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("an offsetting move produced findings: %+v", findings)
		}
	})

	t.Run("the current branch already is the target", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, unitTierPendingPath, "a_test.go\t3\ttask-1\n")
		git := &fakeUnitTierGit{t: t, currentBranch: "main"}

		findings, err := compareUnitTierPendingTotal(root, "main", git.run)
		if err != nil {
			t.Fatalf("compareUnitTierPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("auditing main against itself produced findings: %+v", findings)
		}
	})

	t.Run("the pending list is new on this branch (the creating PR)", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, unitTierPendingPath, "a_test.go\t3\ttask-1\n")
		git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showErr: gitShowPathMissingErr("origin/main", unitTierPendingPath)}

		findings, err := compareUnitTierPendingTotal(root, "main", git.run)
		if err != nil {
			t.Fatalf("compareUnitTierPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("the creating PR produced findings: %+v", findings)
		}
	})

	t.Run("an empty target is a no-op", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		git := &fakeUnitTierGit{t: t}

		findings, err := compareUnitTierPendingTotal(root, "", git.run)
		if err != nil {
			t.Fatalf("compareUnitTierPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("an empty target produced findings: %+v", findings)
		}
	})
}

// TestCompareUnitTierPendingTotalRejectsAMalformedLocalPendingFile pins the
// error path: a local unit_tier.pending this repository's own
// ParseUnitTierPending rejects (here, a negative count) surfaces as an
// error, not a silently-zero total.
func TestCompareUnitTierPendingTotalRejectsAMalformedLocalPendingFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, unitTierPendingPath, "a_test.go\t-1\ttask-1\n")
	git := &fakeUnitTierGit{t: t, currentBranch: "feature/x"}

	if _, err := compareUnitTierPendingTotal(root, "main", git.run); err == nil {
		t.Fatal("want an error for an invalid local unit_tier.pending")
	}
}

// TestCompareUnitTierPendingTotalRejectsAMalformedTargetPendingFile pins the
// same error path for the fetched target's copy.
func TestCompareUnitTierPendingTotalRejectsAMalformedTargetPendingFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, unitTierPendingPath, "a_test.go\t1\ttask-1\n")
	git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showContent: "a_test.go\t-1\ttask-1\n"}

	if _, err := compareUnitTierPendingTotal(root, "main", git.run); err == nil {
		t.Fatal("want an error for an invalid target unit_tier.pending")
	}
}

// TestCompareUnitTierPendingTotalRejectsADeterminingCurrentBranchFailure
// pins the "determine the current branch" error path.
func TestCompareUnitTierPendingTotalRejectsADeterminingCurrentBranchFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, unitTierPendingPath, "a_test.go\t1\ttask-1\n")
	git := &fakeUnitTierGit{t: t, currentBranchErr: errors.New("not a git repository")}

	if _, err := compareUnitTierPendingTotal(root, "main", git.run); err == nil {
		t.Fatal("want an error when the current branch cannot be determined")
	}
}

// TestCompareUnitTierPendingTotalRejectsAFetchFailure pins the "fetch
// origin/<target>" error path.
func TestCompareUnitTierPendingTotalRejectsAFetchFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, unitTierPendingPath, "a_test.go\t1\ttask-1\n")
	git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", fetchErr: errors.New("no origin remote configured")}

	if _, err := compareUnitTierPendingTotal(root, "main", git.run); err == nil {
		t.Fatal("want an error when origin/<target> cannot be fetched")
	}
}

// TestCompareUnitTierPendingTotalRejectsANonMissingShowError pins the other
// half of review note #764's "only treat 'path does not exist on target' as
// the creating-PR exemption; any other git error must fail": a `git show`
// failure that is not the path-missing shape (here, a bare "network error"
// message with no "does not exist in" substring) must surface as an error,
// never be swallowed the way a missing path is.
func TestCompareUnitTierPendingTotalRejectsANonMissingShowError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, unitTierPendingPath, "a_test.go\t1\ttask-1\n")
	git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showErr: errors.New("fatal: unable to access origin: network error")}

	if _, err := compareUnitTierPendingTotal(root, "main", git.run); err == nil {
		t.Fatal("want an error for a non-path-missing git show failure")
	}
}

// TestIsGitShowPathMissingOnTargetRejectsNilError pins the nil-error branch.
func TestIsGitShowPathMissingOnTargetRejectsNilError(t *testing.T) {
	t.Parallel()
	if isGitShowPathMissingOnTarget(nil) {
		t.Fatal("want false for a nil error")
	}
}

// TestCompareAgainstTargetCombinesBothComparisonsSorted pins
// CompareAgainstTarget's own contract (review note #764 B5): it runs both
// CompareCoverageFloors and CompareUnitTierPendingTotal against the same
// root and target and returns their findings combined into the one sorted
// slice cmd/wb/ci.go used to build itself, from the two calls this function
// now replaces. A real Git fixture is unavoidable here (unlike
// compareUnitTierPendingTotal's own tests above): CompareCoverageFloors has
// no git-reading port of its own to fake, and CompareAgainstTarget's whole
// job is running both real, exported comparisons together.
func TestCompareAgainstTargetCombinesBothComparisonsSorted(t *testing.T) {
	t.Parallel()
	fixture := newTargetFixture(t, "85")
	write(t, fixture.Root, ".github/workflows/nightly.yml", `
jobs:
  build:
    with:
      min_test_coverage_percent: 85
`)
	write(t, fixture.Root, unitTierPendingPath, "a_test.go\t3\ttask-1\n")
	targetGit(t, fixture.Root, "add", "-A")
	targetGit(t, fixture.Root, "commit", "-qm", "seed the pending list")
	targetGit(t, fixture.Root, "push", "-q", "origin", "main")

	targetGit(t, fixture.Root, "checkout", "-qb", "feature/x")
	write(t, fixture.Root, ".github/workflows/ci.yml", `
jobs:
  build:
    with:
      min_test_coverage_percent: 80
`)
	write(t, fixture.Root, ".github/workflows/nightly.yml", `
jobs:
  build:
    with:
      min_test_coverage_percent: 80
`)
	write(t, fixture.Root, unitTierPendingPath, "a_test.go\t3\ttask-1\nb_test.go\t1\ttask-2\n")
	targetGit(t, fixture.Root, "add", "-A")
	targetGit(t, fixture.Root, "commit", "-qm", "lower both floors and grow the pending total")

	findings, err := CompareAgainstTarget(fixture.Root, "main")
	if err != nil {
		t.Fatalf("CompareAgainstTarget: %v", err)
	}
	if len(findings) != 3 {
		t.Fatalf("findings = %+v, want exactly 3 (two floors, one pending)", findings)
	}
	if findings[0].Code != "coverage-floor-lowered" || findings[1].Code != "coverage-floor-lowered" || findings[2].Code != "unit-tier-pending-total-rose" {
		t.Fatalf("findings = %+v, want both coverage-floor-lowered findings before unit-tier-pending-total-rose", findings)
	}
	if findings[0].File >= findings[1].File {
		t.Fatalf("findings = %+v, want the two same-code findings sorted by File", findings)
	}
}

// TestCompareAgainstTargetPropagatesACoverageFloorsError pins the first
// error branch: CompareAgainstTarget must surface a CompareCoverageFloors
// failure rather than silently proceeding to the pending comparison.
func TestCompareAgainstTargetPropagatesACoverageFloorsError(t *testing.T) {
	t.Parallel()
	if _, err := CompareAgainstTarget(t.TempDir(), "main"); err == nil {
		t.Fatal("want an error for a directory that is not a Git repository")
	}
}

// TestCompareAgainstTargetPropagatesAPendingTotalError pins the second error
// branch: a CompareUnitTierPendingTotal failure (here, a malformed local
// unit_tier.pending) surfaces even when CompareCoverageFloors itself found
// nothing to report.
func TestCompareAgainstTargetPropagatesAPendingTotalError(t *testing.T) {
	t.Parallel()
	fixture := newTargetFixture(t, "85")
	write(t, fixture.Root, unitTierPendingPath, "a_test.go\t3\ttask-1\n")
	targetGit(t, fixture.Root, "add", "-A")
	targetGit(t, fixture.Root, "commit", "-qm", "seed the pending list")
	targetGit(t, fixture.Root, "push", "-q", "origin", "main")

	targetGit(t, fixture.Root, "checkout", "-qb", "feature/x")
	write(t, fixture.Root, unitTierPendingPath, "a_test.go\t-1\ttask-1\n")
	targetGit(t, fixture.Root, "add", "-A")
	targetGit(t, fixture.Root, "commit", "-qm", "corrupt the local pending file")

	if _, err := CompareAgainstTarget(fixture.Root, "main"); err == nil {
		t.Fatal("want an error for an invalid local unit_tier.pending")
	}
}

// TestCompareUnitTierPendingTotalExportedWrapperEmptyTarget covers
// CompareUnitTierPendingTotal itself (not just its compareUnitTierPendingTotal
// core): an empty target short-circuits before any git call, so this needs
// no real repository and no fake.
func TestCompareUnitTierPendingTotalExportedWrapperEmptyTarget(t *testing.T) {
	t.Parallel()
	findings, err := CompareUnitTierPendingTotal(t.TempDir(), "")
	if err != nil {
		t.Fatalf("CompareUnitTierPendingTotal: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("an empty target produced findings: %+v", findings)
	}
}
