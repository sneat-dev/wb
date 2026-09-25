package ciaudit

import (
	"errors"
	"testing"
)

// campaignTestNamesPendingPath is campaign_test_names.pending's repo-relative
// path, the task-21 analogue of unittier_target_test.go's
// unitTierPendingPath and execsites_target_test.go's execSitesPendingPath.
const campaignTestNamesPendingPath = "internal/quality/testdata/campaign_test_names.pending"

// These tests mirror unittier_target_test.go's and execsites_target_test.go's
// own coverage of their comparators, applied to
// compareCampaignTestNamesPendingTotal (task-21's cross-PR ratchet on
// campaign_test_names.pending): same fake git double (fakeUnitTierGit,
// already generic over any unitTierGitRunner consumer), same false-positive
// shapes, same error paths. Duplicating the table here, rather than
// parameterising all three target functions over one shared test, keeps
// each ratchet's own file:line failure message pinned independently, the
// same way task-8's own tests were kept separate from task-24's.

// TestCompareCampaignTestNamesPendingTotalReportsARisingTotal pins task-21's
// cross-PR ratchet: campaign_test_names.pending's grand total may not rise
// against the base branch's committed copy.
func TestCompareCampaignTestNamesPendingTotalReportsARisingTotal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, campaignTestNamesPendingPath, "a_test.go\t3\ttask-21\nb_test.go\t1\ttask-21\n")
	git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showContent: "a_test.go\t3\ttask-21\n"}

	findings, err := compareCampaignTestNamesPendingTotal(root, "main", git.run)
	if err != nil {
		t.Fatalf("compareCampaignTestNamesPendingTotal: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected exactly one finding, got %+v", findings)
	}
	if findings[0].Code != "campaign-test-names-pending-total-rose" {
		t.Fatalf("unexpected finding code: %+v", findings[0])
	}
	if findings[0].File != campaignTestNamesPendingPath {
		t.Fatalf("unexpected finding file: %+v", findings[0])
	}
}

// TestCompareCampaignTestNamesPendingTotalFalsePositives pins the shapes
// that must not report a finding.
func TestCompareCampaignTestNamesPendingTotalFalsePositives(t *testing.T) {
	t.Parallel()

	t.Run("an unchanged total", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, campaignTestNamesPendingPath, "a_test.go\t3\ttask-21\n")
		git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showContent: "a_test.go\t3\ttask-21\n"}

		findings, err := compareCampaignTestNamesPendingTotal(root, "main", git.run)
		if err != nil {
			t.Fatalf("compareCampaignTestNamesPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("an unchanged total produced findings: %+v", findings)
		}
	})

	t.Run("a total that shrank", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, campaignTestNamesPendingPath, "a_test.go\t1\ttask-21\n")
		git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showContent: "a_test.go\t3\ttask-21\nb_test.go\t2\ttask-21\n"}

		findings, err := compareCampaignTestNamesPendingTotal(root, "main", git.run)
		if err != nil {
			t.Fatalf("compareCampaignTestNamesPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("a shrinking total produced findings: %+v", findings)
		}
	})

	t.Run("one entry grows while another shrinks by at least as much", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, campaignTestNamesPendingPath, "a_test.go\t5\ttask-21\nb_test.go\t3\ttask-21\n")
		git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showContent: "a_test.go\t3\ttask-21\nb_test.go\t5\ttask-21\n"}

		findings, err := compareCampaignTestNamesPendingTotal(root, "main", git.run)
		if err != nil {
			t.Fatalf("compareCampaignTestNamesPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("an offsetting move produced findings: %+v", findings)
		}
	})

	t.Run("the current branch already is the target", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, campaignTestNamesPendingPath, "a_test.go\t3\ttask-21\n")
		git := &fakeUnitTierGit{t: t, currentBranch: "main"}

		findings, err := compareCampaignTestNamesPendingTotal(root, "main", git.run)
		if err != nil {
			t.Fatalf("compareCampaignTestNamesPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("auditing main against itself produced findings: %+v", findings)
		}
	})

	t.Run("the pending list is new on this branch (the creating PR)", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, campaignTestNamesPendingPath, "a_test.go\t3\ttask-21\n")
		git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showErr: gitShowPathMissingErr("origin/main", campaignTestNamesPendingPath)}

		findings, err := compareCampaignTestNamesPendingTotal(root, "main", git.run)
		if err != nil {
			t.Fatalf("compareCampaignTestNamesPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("the creating PR produced findings: %+v", findings)
		}
	})

	t.Run("the pending list is new on this branch, tracked but uncommitted on target", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, campaignTestNamesPendingPath, "a_test.go\t3\ttask-21\n")
		git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showErr: gitShowPathExistsOnDiskButNotOnTargetErr("origin/main", campaignTestNamesPendingPath)}

		findings, err := compareCampaignTestNamesPendingTotal(root, "main", git.run)
		if err != nil {
			t.Fatalf("compareCampaignTestNamesPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("the creating PR produced findings: %+v", findings)
		}
	})

	t.Run("an empty target is a no-op", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		git := &fakeUnitTierGit{t: t}

		findings, err := compareCampaignTestNamesPendingTotal(root, "", git.run)
		if err != nil {
			t.Fatalf("compareCampaignTestNamesPendingTotal: %v", err)
		}
		if len(findings) != 0 {
			t.Fatalf("an empty target produced findings: %+v", findings)
		}
	})
}

// TestCompareCampaignTestNamesPendingTotalRejectsAMalformedLocalPendingFile
// pins the error path for a local campaign_test_names.pending this
// repository's own ParseCampaignTestNamesPending rejects.
func TestCompareCampaignTestNamesPendingTotalRejectsAMalformedLocalPendingFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, campaignTestNamesPendingPath, "a_test.go\t-1\ttask-21\n")
	git := &fakeUnitTierGit{t: t, currentBranch: "feature/x"}

	if _, err := compareCampaignTestNamesPendingTotal(root, "main", git.run); err == nil {
		t.Fatal("want an error for an invalid local campaign_test_names.pending")
	}
}

// TestCompareCampaignTestNamesPendingTotalRejectsAMalformedTargetPendingFile
// pins the same error path for the fetched target's copy.
func TestCompareCampaignTestNamesPendingTotalRejectsAMalformedTargetPendingFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, campaignTestNamesPendingPath, "a_test.go\t1\ttask-21\n")
	git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showContent: "a_test.go\t-1\ttask-21\n"}

	if _, err := compareCampaignTestNamesPendingTotal(root, "main", git.run); err == nil {
		t.Fatal("want an error for an invalid target campaign_test_names.pending")
	}
}

// TestCompareCampaignTestNamesPendingTotalRejectsADeterminingCurrentBranchFailure
// pins the "determine the current branch" error path.
func TestCompareCampaignTestNamesPendingTotalRejectsADeterminingCurrentBranchFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, campaignTestNamesPendingPath, "a_test.go\t1\ttask-21\n")
	git := &fakeUnitTierGit{t: t, currentBranchErr: errors.New("not a git repository")}

	if _, err := compareCampaignTestNamesPendingTotal(root, "main", git.run); err == nil {
		t.Fatal("want an error when the current branch cannot be determined")
	}
}

// TestCompareCampaignTestNamesPendingTotalRejectsAFetchFailure pins the
// "fetch origin/<target>" error path.
func TestCompareCampaignTestNamesPendingTotalRejectsAFetchFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, campaignTestNamesPendingPath, "a_test.go\t1\ttask-21\n")
	git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", fetchErr: errors.New("no origin remote configured")}

	if _, err := compareCampaignTestNamesPendingTotal(root, "main", git.run); err == nil {
		t.Fatal("want an error when origin/<target> cannot be fetched")
	}
}

// TestCompareCampaignTestNamesPendingTotalRejectsANonMissingShowError pins
// the other half of the creating-PR exemption: a `git show` failure that is
// not the path-missing shape must surface as an error.
func TestCompareCampaignTestNamesPendingTotalRejectsANonMissingShowError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, campaignTestNamesPendingPath, "a_test.go\t1\ttask-21\n")
	git := &fakeUnitTierGit{t: t, currentBranch: "feature/x", showErr: errors.New("fatal: unable to access origin: network error")}

	if _, err := compareCampaignTestNamesPendingTotal(root, "main", git.run); err == nil {
		t.Fatal("want an error for a non-path-missing git show failure")
	}
}

// TestCompareCampaignTestNamesPendingTotalExportedWrapperEmptyTarget covers
// CompareCampaignTestNamesPendingTotal itself (not just its
// compareCampaignTestNamesPendingTotal core): an empty target
// short-circuits before any git call, so this needs no real repository and
// no fake.
func TestCompareCampaignTestNamesPendingTotalExportedWrapperEmptyTarget(t *testing.T) {
	t.Parallel()
	findings, err := CompareCampaignTestNamesPendingTotal(t.TempDir(), "")
	if err != nil {
		t.Fatalf("CompareCampaignTestNamesPendingTotal: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("an empty target produced findings: %+v", findings)
	}
}
