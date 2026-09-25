package ciaudit

import "testing"

const unitTierPendingPath = "internal/quality/testdata/unit_tier.pending"

// TestCompareUnitTierPendingTotalReportsARisingTotal pins task-24's cross-PR
// ratchet: internal/quality/testdata/unit_tier.pending's grand total may not
// rise against the base branch's committed copy.
func TestCompareUnitTierPendingTotalReportsARisingTotal(t *testing.T) {
	t.Parallel()
	fixture := newTargetFixture(t, "85")
	write(t, fixture.Root, unitTierPendingPath, "a_test.go\t3\ttask-1\n")
	targetGit(t, fixture.Root, "add", "-A")
	targetGit(t, fixture.Root, "commit", "-qm", "seed the pending list")
	targetGit(t, fixture.Root, "push", "-q", "origin", "main")

	targetGit(t, fixture.Root, "checkout", "-qb", "feature/x")
	write(t, fixture.Root, unitTierPendingPath, "a_test.go\t3\ttask-1\nb_test.go\t1\ttask-2\n")
	targetGit(t, fixture.Root, "add", "-A")
	targetGit(t, fixture.Root, "commit", "-qm", "add a new pending entry without shrinking another")

	findings, err := CompareUnitTierPendingTotal(fixture.Root, "main")
	if err != nil {
		t.Fatalf("CompareUnitTierPendingTotal: %v", err)
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
		fixture := newTargetFixture(t, "85")
		write(t, fixture.Root, unitTierPendingPath, "a_test.go\t3\ttask-1\n")
		targetGit(t, fixture.Root, "add", "-A")
		targetGit(t, fixture.Root, "commit", "-qm", "seed the pending list")
		targetGit(t, fixture.Root, "push", "-q", "origin", "main")

		targetGit(t, fixture.Root, "checkout", "-qb", "feature/x")
		write(t, fixture.Root, "README.md", "unrelated change\n")
		targetGit(t, fixture.Root, "add", "-A")
		targetGit(t, fixture.Root, "commit", "-qm", "unrelated")

		findings, err := CompareUnitTierPendingTotal(fixture.Root, "main")
		if err != nil {
			t.Fatalf("CompareUnitTierPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("an unchanged total produced findings: %+v", findings)
		}
	})

	t.Run("a total that shrank", func(t *testing.T) {
		t.Parallel()
		fixture := newTargetFixture(t, "85")
		write(t, fixture.Root, unitTierPendingPath, "a_test.go\t3\ttask-1\nb_test.go\t2\ttask-2\n")
		targetGit(t, fixture.Root, "add", "-A")
		targetGit(t, fixture.Root, "commit", "-qm", "seed the pending list")
		targetGit(t, fixture.Root, "push", "-q", "origin", "main")

		targetGit(t, fixture.Root, "checkout", "-qb", "feature/x")
		write(t, fixture.Root, unitTierPendingPath, "a_test.go\t1\ttask-1\n")
		targetGit(t, fixture.Root, "add", "-A")
		targetGit(t, fixture.Root, "commit", "-qm", "convert b_test.go and shrink a_test.go")

		findings, err := CompareUnitTierPendingTotal(fixture.Root, "main")
		if err != nil {
			t.Fatalf("CompareUnitTierPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("a shrinking total produced findings: %+v", findings)
		}
	})

	t.Run("one entry grows while another shrinks by at least as much", func(t *testing.T) {
		t.Parallel()
		fixture := newTargetFixture(t, "85")
		write(t, fixture.Root, unitTierPendingPath, "a_test.go\t3\ttask-1\nb_test.go\t5\ttask-2\n")
		targetGit(t, fixture.Root, "add", "-A")
		targetGit(t, fixture.Root, "commit", "-qm", "seed the pending list")
		targetGit(t, fixture.Root, "push", "-q", "origin", "main")

		targetGit(t, fixture.Root, "checkout", "-qb", "feature/x")
		write(t, fixture.Root, unitTierPendingPath, "a_test.go\t5\ttask-1\nb_test.go\t3\ttask-2\n")
		targetGit(t, fixture.Root, "add", "-A")
		targetGit(t, fixture.Root, "commit", "-qm", "move matches from b_test.go to a_test.go")

		findings, err := CompareUnitTierPendingTotal(fixture.Root, "main")
		if err != nil {
			t.Fatalf("CompareUnitTierPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("an offsetting move produced findings: %+v", findings)
		}
	})

	t.Run("the current branch already is the target", func(t *testing.T) {
		t.Parallel()
		fixture := newTargetFixture(t, "85")
		write(t, fixture.Root, unitTierPendingPath, "a_test.go\t3\ttask-1\n")
		targetGit(t, fixture.Root, "add", "-A")
		targetGit(t, fixture.Root, "commit", "-qm", "seed the pending list")

		findings, err := CompareUnitTierPendingTotal(fixture.Root, "main")
		if err != nil {
			t.Fatalf("CompareUnitTierPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("auditing main against itself produced findings: %+v", findings)
		}
	})

	t.Run("the pending list is new on this branch (the creating PR)", func(t *testing.T) {
		t.Parallel()
		fixture := newTargetFixture(t, "85")
		targetGit(t, fixture.Root, "checkout", "-qb", "feature/x")
		write(t, fixture.Root, unitTierPendingPath, "a_test.go\t3\ttask-1\n")
		targetGit(t, fixture.Root, "add", "-A")
		targetGit(t, fixture.Root, "commit", "-qm", "create the pending list")

		findings, err := CompareUnitTierPendingTotal(fixture.Root, "main")
		if err != nil {
			t.Fatalf("CompareUnitTierPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("the creating PR produced findings: %+v", findings)
		}
	})

	t.Run("an empty target is a no-op", func(t *testing.T) {
		t.Parallel()
		fixture := newTargetFixture(t, "85")
		findings, err := CompareUnitTierPendingTotal(fixture.Root, "")
		if err != nil {
			t.Fatalf("CompareUnitTierPendingTotal: %v", err)
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
	fixture := newTargetFixture(t, "85")
	write(t, fixture.Root, unitTierPendingPath, "a_test.go\t3\ttask-1\n")
	targetGit(t, fixture.Root, "add", "-A")
	targetGit(t, fixture.Root, "commit", "-qm", "seed the pending list")
	targetGit(t, fixture.Root, "push", "-q", "origin", "main")

	targetGit(t, fixture.Root, "checkout", "-qb", "feature/x")
	write(t, fixture.Root, unitTierPendingPath, "a_test.go\t-1\ttask-1\n")
	targetGit(t, fixture.Root, "add", "-A")
	targetGit(t, fixture.Root, "commit", "-qm", "corrupt the local pending file")

	if _, err := CompareUnitTierPendingTotal(fixture.Root, "main"); err == nil {
		t.Fatal("want an error for an invalid local unit_tier.pending")
	}
}

// TestCompareUnitTierPendingTotalRejectsAMalformedTargetPendingFile pins the
// same error path for the fetched target's copy.
func TestCompareUnitTierPendingTotalRejectsAMalformedTargetPendingFile(t *testing.T) {
	t.Parallel()
	fixture := newTargetFixture(t, "85")
	write(t, fixture.Root, unitTierPendingPath, "a_test.go\t-1\ttask-1\n")
	targetGit(t, fixture.Root, "add", "-A")
	targetGit(t, fixture.Root, "commit", "-qm", "seed a corrupt pending list")
	targetGit(t, fixture.Root, "push", "-q", "origin", "main")

	targetGit(t, fixture.Root, "checkout", "-qb", "feature/x")
	write(t, fixture.Root, unitTierPendingPath, "a_test.go\t1\ttask-1\n")
	targetGit(t, fixture.Root, "add", "-A")
	targetGit(t, fixture.Root, "commit", "-qm", "fix the local pending list")

	if _, err := CompareUnitTierPendingTotal(fixture.Root, "main"); err == nil {
		t.Fatal("want an error for an invalid target unit_tier.pending")
	}
}

// TestCompareUnitTierPendingTotalRejectsAPathThatIsNotAGitRepository pins
// the "determine the current branch" error path.
func TestCompareUnitTierPendingTotalRejectsAPathThatIsNotAGitRepository(t *testing.T) {
	t.Parallel()
	if _, err := CompareUnitTierPendingTotal(t.TempDir(), "main"); err == nil {
		t.Fatal("want an error for a directory that is not a Git repository")
	}
}

// TestCompareUnitTierPendingTotalRejectsAFetchFailure pins the "fetch
// origin/<target>" error path: a repository with no "origin" remote at all.
func TestCompareUnitTierPendingTotalRejectsAFetchFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	targetGit(t, root, "init", "-q", "-b", "feature/x")
	targetGit(t, root, "config", "user.email", "audit@example.test")
	targetGit(t, root, "config", "user.name", "audit")
	write(t, root, "README.md", "no origin remote configured\n")
	targetGit(t, root, "add", "-A")
	targetGit(t, root, "commit", "-qm", "init")

	if _, err := CompareUnitTierPendingTotal(root, "main"); err == nil {
		t.Fatal("want an error when origin/<target> cannot be fetched")
	}
}
