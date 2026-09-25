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
// not exceed what testdata/unit_tier.pending committed for it, unless the
// file is instead named on testdata/unit_tier.allow, which exempts it
// entirely. A file with matches but no entry on either list -- including one
// that first starts using the helper-process re-exec marker
// (UnitTierPatternHelperProcessEnv, review note #764 N1: "only allow-listed
// files may use it") -- is a fresh, unreviewed violation and fails here,
// exactly like a file whose count grew past its committed entry.
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

	var offenders []string
	for file, count := range counts {
		if _, allowed := allow[file]; allowed {
			continue
		}
		entry, hasPending := pending[file]
		if !hasPending || count > entry.Count {
			offenders = append(offenders, fmt.Sprintf("%s: %d match(es) found, pending list allows %d", file, count, entry.Count))
		}
	}
	if len(offenders) == 0 {
		return
	}
	sort.Strings(offenders)
	t.Fatalf("%d default-tier test file(s) exceed the unit tier's committed headroom:\n%s\n"+
		"Either rewrite the new match(es) as a unit test against a fake (task-8), or -- if "+
		"reviewed and genuinely still needed -- update the matching entry in "+
		"internal/quality/testdata/unit_tier.pending (or add the file to "+
		"internal/quality/testdata/unit_tier.allow if it only re-runs the test binary "+
		"itself) in this same PR. See spec/plans/coverage-to-100/README.md task-24.",
		len(offenders), strings.Join(offenders, "\n"))
}

// TestUnitTierAllowListEntriesAreGenuineHelperProcessReexec asserts every
// entry on testdata/unit_tier.allow is still a real, currently-detected
// unit-tier match (so a stale entry -- naming a file the detector no longer
// flags at all -- is caught immediately, the same shrink-only discipline
// TestEveryFilewriteBoundaryExemptionMatchesALiveViolation applies to
// internal/filewrite's own allow-lists) and that every one of its matches is
// UnitTierPatternExecStart: the allow list is seeded only with files that
// genuinely re-run the test binary itself (task-24 PR-1's own scope note),
// never a file that also calls real git/gh or writes a fake PATH executable.
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
			if m.Pattern != UnitTierPatternExecStart {
				problems = append(problems, fmt.Sprintf("%s:%d: allow-listed, but this match is %s, not a plain exec-start re-exec -- move it to unit_tier.pending instead", file, m.Line, m.Pattern))
			}
		}
	}
	if len(problems) == 0 {
		return
	}
	sort.Strings(problems)
	t.Fatalf("%d unit_tier.allow problem(s):\n%s", len(problems), strings.Join(problems, "\n"))
}
