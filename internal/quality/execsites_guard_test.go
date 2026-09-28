package quality

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestExecSitesPendingDoesNotRegress is task-8's own static check
// (spec/plans/coverage-to-100 task-8, verifies #4): every non-test .go
// file's live exec-site match count (FindExecSiteMatches) must equal,
// exactly, what testdata/exec_sites.pending committed for it. This mirrors
// TestUnitTierPendingDoesNotRegress's exact-equality discipline (not merely
// "not exceeded"): a live count above its entry is a fresh, unreviewed
// direct-process or direct-git call; a live count below it is a stale entry
// banking unearned slack the next fresh violation could spend unnoticed; a
// pending entry naming a file the detector no longer matches at all is
// stale and must be deleted; and a file with matches but no entry is a
// fresh, unreviewed violation the same way. Unlike unit_tier.pending, there
// is no companion allow-list file here -- the exec-site detector's
// allow-list is the fixed set of fd-inheriting secure-helper function names
// baked into execSiteAllowedFunctionNames and the execSiteAllowedDirs
// package prefixes, both already exercised directly by
// TestFindExecSiteMatchesSkipsAnAllowListedFunctionEntirely and
// TestFindExecSiteMatchesSkipsAllowListedDirectories.
//
// This test is deliberately silent about the cross-PR rule that the pending
// list's grand total must not rise against the base branch's copy: that
// needs a fetched remote branch (ciaudit.CompareExecSitesPendingTotal,
// wired into `wb ci audit --target`), which this hermetic, parallel-safe
// test never touches (no process, no network).
func TestExecSitesPendingDoesNotRegress(t *testing.T) {
	t.Parallel()

	root, err := ParallelGuardModuleRoot()
	if err != nil {
		t.Fatal(err)
	}

	pending, err := ParseExecSitesPending(filepath.Join(root, "internal", "quality", "testdata", "exec_sites.pending"))
	if err != nil {
		t.Fatal(err)
	}

	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	counts := CountExecSiteMatchesByFile(matches)

	seen := map[string]bool{}
	var offenders []string
	for file, count := range counts {
		seen[file] = true
		entry, hasPending := pending[file]
		switch {
		case !hasPending:
			offenders = append(offenders, fmt.Sprintf("%s: %d match(es) found, but no exec_sites.pending entry", file, count))
		case count != entry.Count:
			offenders = append(offenders, fmt.Sprintf("%s: %d match(es) found, pending list commits to exactly %d", file, count, entry.Count))
		}
	}
	// A pending entry this pass never saw live (the file was migrated, no
	// longer exists, or no longer matches at all) is stale and must be
	// removed rather than left to bank unearned headroom.
	for file, entry := range pending {
		if seen[file] {
			continue
		}
		offenders = append(offenders, fmt.Sprintf("%s: exec_sites.pending commits to %d match(es), but the detector finds none; remove the stale entry", file, entry.Count))
	}
	if len(offenders) == 0 {
		return
	}
	sort.Strings(offenders)
	t.Fatalf("%d exec_sites.pending problem(s):\n%s\n"+
		"Either migrate the new call(s) onto internal/runner or internal/gitcli, or -- if "+
		"reviewed and genuinely still needed -- update the matching entry in "+
		"internal/quality/testdata/exec_sites.pending to the exact live count in this same "+
		"PR. See spec/plans/coverage-to-100/README.md task-8.",
		len(offenders), strings.Join(offenders, "\n"))
}
