package quality

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestCampaignTestNamesPendingDoesNotRegress is task-21's own static check
// (spec/plans/coverage-to-100 task-21, AGENTS.md:108-112): every _test.go
// file's live campaign-name match count (FindCampaignTestNameMatches) must
// equal, exactly, what testdata/campaign_test_names.pending committed for
// it. This mirrors TestUnitTierPendingDoesNotRegress's and
// TestExecSitesPendingDoesNotRegress's exact-equality discipline: a live
// count above its entry is a fresh, unreviewed campaign-named file or
// function (forbidden outright by AGENTS.md:108-112 -- the fix is always a
// rename, never an entry bump, but the list still records the exact count
// so a rename that only partially fixes a file is visible); a live count
// below it is a stale entry banking unearned slack; a pending entry naming
// a file the detector no longer matches at all is stale and must be
// deleted; and a file with matches but no entry is a fresh, unreviewed
// violation the same way.
//
// This test is deliberately silent about the cross-PR rule that the
// pending list's grand total must not rise against the base branch's copy:
// that needs a fetched remote branch
// (ciaudit.CompareCampaignTestNamesPendingTotal, wired into `wb ci audit
// --target`), which this hermetic, parallel-safe test never touches (no
// process, no network).
func TestCampaignTestNamesPendingDoesNotRegress(t *testing.T) {
	t.Parallel()

	root, err := ParallelGuardModuleRoot()
	if err != nil {
		t.Fatal(err)
	}

	pending, err := ParseCampaignTestNamesPending(filepath.Join(root, "internal", "quality", "testdata", "campaign_test_names.pending"))
	if err != nil {
		t.Fatal(err)
	}

	matches, err := FindCampaignTestNameMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	counts := CountCampaignTestNameMatchesByFile(matches)

	seen := map[string]bool{}
	var offenders []string
	for file, count := range counts {
		seen[file] = true
		entry, hasPending := pending[file]
		switch {
		case !hasPending:
			offenders = append(offenders, fmt.Sprintf("%s: %d match(es) found, but no campaign_test_names.pending entry", file, count))
		case count != entry.Count:
			offenders = append(offenders, fmt.Sprintf("%s: %d match(es) found, pending list commits to exactly %d", file, count, entry.Count))
		}
	}
	// A pending entry this pass never saw live (the file was renamed, no
	// longer exists, or no longer matches at all) is stale and must be
	// removed rather than left to bank unearned headroom.
	for file, entry := range pending {
		if seen[file] {
			continue
		}
		offenders = append(offenders, fmt.Sprintf("%s: campaign_test_names.pending commits to %d match(es), but the detector finds none; remove the stale entry", file, entry.Count))
	}
	if len(offenders) == 0 {
		return
	}
	sort.Strings(offenders)
	t.Fatalf("%d campaign_test_names.pending problem(s):\n%s\n"+
		"Rename the file and/or Test function after the behaviour it verifies "+
		"(AGENTS.md:108-112) -- never merge campaign-named content into an "+
		"existing file this list does not already name. If the file is "+
		"already on this list and you are only partially fixing it in this "+
		"PR, update its entry to the exact live count in the same PR. See "+
		"spec/plans/coverage-to-100/README.md task-21.",
		len(offenders), strings.Join(offenders, "\n"))
}

// TestCampaignTestNamesPendingTotalIsExactlyTheSumOfItsEntries is a light
// sanity pin on CampaignTestNamesPendingTotal/ParseCampaignTestNamesPending
// against the real committed file, independent of the live filesystem scan
// TestCampaignTestNamesPendingDoesNotRegress performs: it only exercises
// that the parser and summer agree with a hand count, catching a corrupted
// or hand-edited pending file's arithmetic even if every individual entry
// still happens to match its file (a scenario the regression test above
// cannot see, since it never sums the list at all).
func TestCampaignTestNamesPendingTotalIsExactlyTheSumOfItsEntries(t *testing.T) {
	t.Parallel()

	root, err := ParallelGuardModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	pending, err := ParseCampaignTestNamesPending(filepath.Join(root, "internal", "quality", "testdata", "campaign_test_names.pending"))
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, entry := range pending {
		want += entry.Count
	}
	if got := CampaignTestNamesPendingTotal(pending); got != want {
		t.Fatalf("CampaignTestNamesPendingTotal = %d, want %d (hand-summed from %d entries)", got, want, len(pending))
	}
}
