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
// with when path never existed anywhere in the repository's history -- one
// of the two errors compareUnitTierPendingTotal treats as "this PR creates
// the file", isGitShowPathMissingOnTarget's own doc comment.
func gitShowPathMissingErr(ref, path string) error {
	return fmt.Errorf("git show %s:%s: exit status 128: fatal: path '%s' does not exist in '%s'", ref, path, path, ref)
}

// gitShowPathExistsOnDiskButNotOnTargetErr is the other shape `git show
// <ref>:<path>` fails with: path exists, tracked, on the current branch, but
// was never committed on ref -- confirmed against real git (`git show
// origin/cov/integration:internal/quality/testdata/unit_tier.pending`
// against this very PR, which is exactly this scenario), and the one this
// package's first cut at isGitShowPathMissingOnTarget missed entirely by
// only matching "does not exist in".
func gitShowPathExistsOnDiskButNotOnTargetErr(ref, path string) error {
	return fmt.Errorf("git show %s:%s: exit status 128: fatal: path '%s' exists on disk, but not in '%s'", ref, path, path, ref)
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

	t.Run("the pending list is new on this branch, tracked but uncommitted on target", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, unitTierPendingPath, "a_test.go\t3\ttask-1\n")
		git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showErr: gitShowPathExistsOnDiskButNotOnTargetErr("origin/main", unitTierPendingPath)}

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

// fakeTargetComparator is a targetComparator test double: it returns
// whatever findings/error the test configured and records that it was
// called, without touching root or target at all -- compareAgainstTarget's
// own job is composition, sorting and error propagation, none of which
// needs a real comparison, real findings, or a real Git repository to
// exercise (review note #764 B6: the round-1 B4 fake-port pattern applied to
// this function too).
type fakeTargetComparator struct {
	findings []Finding
	err      error
	called   bool
}

func (f *fakeTargetComparator) run(_, _ string) ([]Finding, error) {
	f.called = true
	return f.findings, f.err
}

// TestCompareAgainstTargetCombinesBothComparisonsSorted pins
// compareAgainstTarget's own contract (review note #764 B5, B6): it runs
// both comparators against the same root and target and returns their
// findings combined into the one sorted slice cmd/wb/ci.go used to build
// itself, from the two calls this function now replaces.
func TestCompareAgainstTargetCombinesBothComparisonsSorted(t *testing.T) {
	t.Parallel()
	floors := &fakeTargetComparator{findings: []Finding{
		{Code: "coverage-floor-lowered", File: ".github/workflows/nightly.yml"},
		{Code: "coverage-floor-lowered", File: ".github/workflows/ci.yml"},
	}}
	pending := &fakeTargetComparator{findings: []Finding{
		{Code: "unit-tier-pending-total-rose", File: unitTierPendingPath},
	}}

	findings, err := compareAgainstTarget("/root", "main", floors.run, pending.run)
	if err != nil {
		t.Fatalf("compareAgainstTarget: %v", err)
	}
	if !floors.called || !pending.called {
		t.Fatalf("both comparators must be called: floors=%t pending=%t", floors.called, pending.called)
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

// TestCompareAgainstTargetPropagatesAFloorsError pins the first error
// branch: compareAgainstTarget must surface the floors comparator's failure
// rather than silently proceeding to the pending comparator.
func TestCompareAgainstTargetPropagatesAFloorsError(t *testing.T) {
	t.Parallel()
	floors := &fakeTargetComparator{err: errors.New("floors comparator failed")}
	pending := &fakeTargetComparator{}

	if _, err := compareAgainstTarget("/root", "main", floors.run, pending.run); err == nil {
		t.Fatal("want an error when the floors comparator fails")
	}
	if pending.called {
		t.Fatal("the pending comparator must not run once the floors comparator has failed")
	}
}

// TestCompareAgainstTargetPropagatesAPendingError pins the second error
// branch: the pending comparator's failure surfaces even when the floors
// comparator itself found nothing to report.
func TestCompareAgainstTargetPropagatesAPendingError(t *testing.T) {
	t.Parallel()
	floors := &fakeTargetComparator{}
	pending := &fakeTargetComparator{err: errors.New("pending comparator failed")}

	if _, err := compareAgainstTarget("/root", "main", floors.run, pending.run); err == nil {
		t.Fatal("want an error when the pending comparator fails")
	}
	if !floors.called {
		t.Fatal("the floors comparator must still run before the pending comparator")
	}
}

// TestCompareAgainstTargetWiresRealComparators pins CompareAgainstTarget's
// own exported wrapper: it must pass CompareCoverageFloors and
// CompareUnitTierPendingTotal, not some other pair, to compareAgainstTarget.
// An empty target short-circuits both real comparators before either
// touches git, so this needs no real repository.
func TestCompareAgainstTargetWiresRealComparators(t *testing.T) {
	t.Parallel()
	findings, err := CompareAgainstTarget(t.TempDir(), "")
	if err != nil {
		t.Fatalf("CompareAgainstTarget: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("an empty target produced findings: %+v", findings)
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
