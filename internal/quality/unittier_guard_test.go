package quality

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestUnitTierPendingDoesNotRegress is task-24's static check: every
// default-tier `_test.go` file's live match count (FindUnitTierMatches) must
// equal, exactly, what testdata/unit_tier.pending committed for it, unless
// the file is instead named on testdata/unit_tier.allow, which exempts it
// entirely. Equality, not merely a not-exceeded ceiling (review note #764
// B3): a live count above its entry is a fresh, unreviewed violation, a live
// count below it is a stale entry silently banking slack the next fresh
// violation could spend unnoticed, a pending entry naming a file the
// detector no longer matches at all is a stale, zero-count entry that must
// be deleted, and a file with matches but no entry on either list --
// including one that first starts using the helper-process re-exec marker
// (UnitTierPatternHelperProcessEnv, review note #764 N1: "only allow-listed
// files may use it") -- is a fresh, unreviewed violation the same way.
//
// This test is deliberately silent about the *cross-PR* rule that the
// pending list's grand total must not rise against the base branch's copy:
// that needs a fetched remote branch (ciaudit.CompareUnitTierPendingTotal,
// wired into `wb ci audit --target`), which this hermetic, parallel-safe
// unit-tier test never touches (task-24's own tier rules: no process, no
// network).
func TestUnitTierPendingDoesNotRegress(t *testing.T) {
	t.Parallel()

	root, err := ParallelGuardModuleRoot()
	if err != nil {
		t.Fatal(err)
	}

	pending, err := ParseUnitTierPending(filepath.Join(root, "internal", "quality", "testdata", "unit_tier.pending"))
	if err != nil {
		t.Fatal(err)
	}
	allow, err := ParseUnitTierAllow(filepath.Join(root, "internal", "quality", "testdata", "unit_tier.allow"))
	if err != nil {
		t.Fatal(err)
	}

	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	counts := CountUnitTierMatchesByFile(matches)

	seen := map[string]bool{}
	var offenders []string
	for file, count := range counts {
		if _, allowed := allow[file]; allowed {
			continue
		}
		seen[file] = true
		entry, hasPending := pending[file]
		switch {
		case !hasPending:
			offenders = append(offenders, fmt.Sprintf("%s: %d match(es) found, but no unit_tier.pending entry", file, count))
		case count != entry.Count:
			offenders = append(offenders, fmt.Sprintf("%s: %d match(es) found, pending list commits to exactly %d", file, count, entry.Count))
		}
	}
	// A pending entry this pass never saw live (because the file no longer
	// exists, no longer matches at all, or was moved to unit_tier.allow) is
	// stale and must be removed rather than left to bank unearned headroom.
	for file, entry := range pending {
		if seen[file] {
			continue
		}
		if _, allowed := allow[file]; allowed {
			offenders = append(offenders, fmt.Sprintf("%s: on both unit_tier.pending (%d) and unit_tier.allow; remove the stale pending entry", file, entry.Count))
			continue
		}
		offenders = append(offenders, fmt.Sprintf("%s: unit_tier.pending commits to %d match(es), but the detector finds none; remove the stale entry", file, entry.Count))
	}
	if len(offenders) == 0 {
		return
	}
	sort.Strings(offenders)
	t.Fatalf("%d unit_tier.pending problem(s):\n%s\n"+
		"Either rewrite the new match(es) as a unit test against a fake (task-8), or -- if "+
		"reviewed and genuinely still needed -- update the matching entry in "+
		"internal/quality/testdata/unit_tier.pending to the exact live count (or add the "+
		"file to internal/quality/testdata/unit_tier.allow if it only re-runs the test "+
		"binary itself) in this same PR. See spec/plans/coverage-to-100/README.md task-24.",
		len(offenders), strings.Join(offenders, "\n"))
}

// TestUnitTierAllowListEntriesAreGenuineHelperProcessReexec asserts every
// entry on testdata/unit_tier.allow is still a real, currently-detected
// unit-tier match (so a stale entry -- naming a file the detector no longer
// flags at all -- is caught immediately, the same shrink-only discipline
// TestEveryFilewriteBoundaryExemptionMatchesALiveViolation applies to
// internal/filewrite's own allow-lists) and that every one of its matches is
// either a genuine self-reexec (UnitTierPatternExecStart with SelfReexec
// true -- the program argument is exactly os.Args[0], review note #764 B1:
// "must require the exec program argument to be os.Args[0], not just any
// exec.Command-type call") or the expected companion
// UnitTierPatternHelperProcessEnv marker match that pattern's own re-exec
// dispatch reads to tell the helper-process child from the ordinary test run
// (see UnitTierPatternHelperProcessEnv's doc comment). The allow list is
// seeded only with files that genuinely re-run the test binary itself
// (task-24 PR-1's own scope note): an exec.Command call whose first argument
// is anything else -- "git", a shell path, a fake PATH executable -- must
// fail here even if it sits in an otherwise allow-listed file, so appending
// one to an allow-listed file (the review's own mutation) cannot pass
// silently.
func TestUnitTierAllowListEntriesAreGenuineHelperProcessReexec(t *testing.T) {
	t.Parallel()

	root, err := ParallelGuardModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	allow, err := ParseUnitTierAllow(filepath.Join(root, "internal", "quality", "testdata", "unit_tier.allow"))
	if err != nil {
		t.Fatal(err)
	}
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	byFile := map[string][]UnitTierMatch{}
	for _, m := range matches {
		byFile[m.File] = append(byFile[m.File], m)
	}

	var problems []string
	for file := range allow {
		fileMatches := byFile[file]
		if len(fileMatches) == 0 {
			problems = append(problems, fmt.Sprintf("%s: allow-listed but the detector finds no match at all; remove the stale entry", file))
			continue
		}
		for _, m := range fileMatches {
			genuine := (m.Pattern == UnitTierPatternExecStart && m.SelfReexec) || m.Pattern == UnitTierPatternHelperProcessEnv
			if !genuine {
				problems = append(problems, fmt.Sprintf("%s:%d: allow-listed, but this match is %s (self-reexec=%t), not a genuine os.Args[0] re-exec -- move it to unit_tier.pending instead", file, m.Line, m.Pattern, m.SelfReexec))
			}
		}
	}
	if len(problems) == 0 {
		return
	}
	sort.Strings(problems)
	t.Fatalf("%d unit_tier.allow problem(s):\n%s", len(problems), strings.Join(problems, "\n"))
}
