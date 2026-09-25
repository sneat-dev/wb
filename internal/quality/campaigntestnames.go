// This file backs task-21's static check (spec/plans/coverage-to-100
// README.md, and this repository's own AGENTS.md:108-112): "Name tests
// after the behaviour they verify and assert an observable outcome, never a
// filler coverage:ignore-style marker or a name that reflects a coverage
// campaign instead of behaviour (zz_cov_*, dqcov, tailcov-style names are
// forbidden)." A 2026-09-25 rename campaign (cov-rename-tests) fixed every
// violation a same-day batch of coordinator briefs had just added and swept
// every _test.go file this detector still matched on main into
// testdata/campaign_test_names.pending -- the same shrink-only discipline
// task-24's unit_tier.pending and task-8's exec_sites.pending already use,
// reusing their exact committed file format and this package's own parser
// for it (execsites.go's own comment names the same convention).
//
// The check flags, in every `_test.go` file anywhere in the module, two
// lineages of campaign token: the long-lived "cov" family several earlier
// coverage campaigns left on main, and the rwi/pkp/pk0/cgx/pr9 family the
// 2026-09-25 coordinator briefs used for one day before the same-day rename
// campaign fixed every occurrence it had just added (997a4201). Both are
// guarded, not just the one still visible on main at seed time: an
// unguarded, already-renamed-away pattern is exactly the gap a future
// coordinator brief could walk straight back through by reusing one of
// these tokens tomorrow.
//
//   - a file name (basename, not the full path) containing "zz_", starting
//     "cov_", or containing "_cov_", "tailcov" or "dqcov" (the "cov"
//     family), or containing "_rwi<digits>_", "_pkp<digits>_",
//     "_pk0_" (no trailing digits -- "pk0" is already a complete token),
//     "_cgx<digits>_"/"_cgxc<digits>_", or starting or containing "_pr9_"
//     (the rwi/pkp/pk0/cgx/pr9 family) -- every fragment
//     token-bounded by a leading/trailing "_" (or the start of the name for
//     a leading token) so an ordinary word that merely contains the letters
//     -- "pkg", "network", "coverage" -- can never match;
//   - a `Test*` function name starting CwCov, TailCov, DqCov or Zz
//     (case-sensitive, immediately after "Test", followed by an uppercase
//     letter, a digit, or the end of the name -- so TestCoverageXxx, whose
//     "Cov" is followed by lowercase "erage", is never mistaken for the
//     campaign token TestCov never actually names on main), or starting
//     Rwi/RWI/Pkp/Pk0/Cgx (case variants main's own violations actually
//     use) immediately followed by a digit -- Cgx's own campaign token is
//     always spelled with a further lowercase "c" before the digit
//     (Cgxc1/2/3), so that form is matched explicitly rather than by a
//     generic uppercase-or-digit boundary the literal "c" would fail.
//
// Both lineages come from what main and this exact rename campaign actually
// contain, not an exhaustive theoretical list of every token a campaign
// could invent; a genuinely new token still needs this detector extended by
// hand, the same way AGENTS.md:108-112's rule itself would need updating.
package quality

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// CampaignTestNameKind names which half of a _test.go file
// FindCampaignTestNameMatches flagged: the file's own name, or one of its
// Test function names.
type CampaignTestNameKind string

const (
	// CampaignTestNameKindFile is the file's own basename.
	CampaignTestNameKindFile CampaignTestNameKind = "file-name"
	// CampaignTestNameKindFunc is one Test* function declared in the file.
	CampaignTestNameKindFunc CampaignTestNameKind = "func-name"
)

// CampaignTestNameMatch is one occurrence FindCampaignTestNameMatches
// reports: either the file itself (Line 1, Name the basename) or one Test
// function whose name carries a campaign token (Line the func's own
// declaration line, Name the function name).
type CampaignTestNameMatch struct {
	File string
	Line int
	Kind CampaignTestNameKind
	Name string
}

func (m CampaignTestNameMatch) String() string {
	return fmt.Sprintf("%s:%d: %s %q carries a campaign-naming token, not a behaviour name (%s)", m.File, m.Line, m.Kind, m.Name, m.Kind)
}

// campaignFuncNameToken matches a Test* function name whose name starts
// with one of the "cov"-family campaign tokens AGENTS.md:108-112 and main's
// own pre-existing violations actually use, immediately followed by another
// capitalised word or a digit (so the token itself is a whole name segment)
// or the end of the name. Requiring that boundary is what keeps
// TestCoverageProfilePathInjected -- an entirely legitimate behaviour name
// that happens to start "TestCov" -- out of the match set: "erage" is
// neither uppercase nor a digit, so "Cov" alone never matches on its own,
// only the fuller CwCov/TailCov/DqCov tokens main's actual violations use.
var campaignFuncNameToken = regexp.MustCompile(`^Test(CwCov|TailCov|DqCov|Zz)([A-Z0-9]|$)`)

// campaignFuncNameLineageToken matches a Test* function name starting with
// one of the rwi/pkp/pk0/cgx tokens the 2026-09-25 coordinator briefs used
// (997a4201 renamed every one this exact batch added; this pattern guards
// against a future brief reusing one of them). Rwi, RWI and Pkp are each
// immediately followed by a digit -- the exact shape main's own violations
// use (Rwi01, RWI08, Pkp00/02/03); Cgx is followed by an optional further
// lowercase "c" then a digit (Cgxc1/2/3 -- never a generic
// uppercase-or-digit boundary, which "Cgxc1" would fail, since the
// character right after "Cgx" is the lowercase "c", not a digit); Pk0 is
// already a complete three-character token (the trailing "0" is not a
// separate digit suffix), so it uses the same
// uppercase-letter-or-digit-or-end boundary campaignFuncNameToken's
// Cov-family tokens use, not a required following digit.
var campaignFuncNameLineageToken = regexp.MustCompile(`^Test(Rwi|RWI|Pkp)[0-9]|^TestPk0([A-Z0-9]|$)|^TestCgxc?[0-9]`)

// campaignFileNameLineageFragment matches a _test.go basename carrying one
// of the rwi/pkp/pk0/cgx/pr9 tokens' file-name shape, token-bounded on both
// sides -- a leading "_" or the start of the name, a trailing "_" -- so an
// ordinary word merely containing the letters ("pkg", "network", "cgxray")
// can never match: "rwi<digits>_", "pkp<digits>_", "pk0_" (no trailing
// digits -- "pk0" is already a complete token), "cgx<digits>_"/"cgxc<digits>_"
// or "pr9_", each either at the very start of the basename or immediately
// after an "_" elsewhere in it.
var campaignFileNameLineageFragment = regexp.MustCompile(`(^|_)(rwi|pkp)[0-9]+_|(^|_)pk0_|(^|_)cgxc?[0-9]+_|(^|_)pr9_`)

// campaignFileNameViolation reports whether basename -- a _test.go file's
// own name, never its directory -- carries one of AGENTS.md:108-112's
// forbidden campaign-naming fragments, from either the "cov" lineage or the
// rwi/pkp/pk0/cgx/pr9 lineage.
func campaignFileNameViolation(basename string) bool {
	return strings.Contains(basename, "zz_") ||
		strings.HasPrefix(basename, "cov_") ||
		strings.Contains(basename, "_cov_") ||
		strings.Contains(basename, "tailcov") ||
		strings.Contains(basename, "dqcov") ||
		campaignFileNameLineageFragment.MatchString(basename)
}

// campaignFuncNameViolation reports whether name -- a declared Test*
// function's own identifier -- carries campaignFuncNameToken's (the "cov"
// lineage) or campaignFuncNameLineageToken's (the rwi/pkp/pk0/cgx lineage)
// forbidden prefix.
func campaignFuncNameViolation(name string) bool {
	return campaignFuncNameToken.MatchString(name) || campaignFuncNameLineageToken.MatchString(name)
}

// FindCampaignTestNameMatches walks root and reports, in every `_test.go`
// file anywhere in the module (unlike task-24's unit-tier detector, this
// naming rule is not tier-specific: an e2e-tagged file is named after its
// behaviour exactly the same as a default-tier one), every file name and
// every Test function name that carries a forbidden campaign-naming token.
// Results are sorted by file, then line, then name.
func FindCampaignTestNameMatches(root string) ([]CampaignTestNameMatch, error) {
	fset := token.NewFileSet()
	var matches []CampaignTestNameMatch
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if unitTierSkipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := relSlashFor(root, path)
		basename := filepath.Base(path)
		if campaignFileNameViolation(basename) {
			matches = append(matches, CampaignTestNameMatch{File: rel, Line: 1, Kind: CampaignTestNameKindFile, Name: basename})
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			name := fn.Name.Name
			if !strings.HasPrefix(name, "Test") || !campaignFuncNameViolation(name) {
				continue
			}
			matches = append(matches, CampaignTestNameMatch{File: rel, Line: fset.Position(fn.Pos()).Line, Kind: CampaignTestNameKindFunc, Name: name})
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk %s: %w", root, walkErr)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].File != matches[j].File {
			return matches[i].File < matches[j].File
		}
		if matches[i].Line != matches[j].Line {
			return matches[i].Line < matches[j].Line
		}
		return matches[i].Name < matches[j].Name
	})
	return matches, nil
}

// CountCampaignTestNameMatchesByFile aggregates matches per file: the file
// name itself (if it violates) counts as one, and every violating Test
// function inside it adds one more -- the same per-file "how many things
// would a reviewer have to rename" count task-24's and task-8's own
// detectors report.
func CountCampaignTestNameMatchesByFile(matches []CampaignTestNameMatch) map[string]int {
	counts := map[string]int{}
	for _, m := range matches {
		counts[m.File]++
	}
	return counts
}

// campaign_test_names.pending shares unit_tier.pending's exact file format
// (path\tcount\towner) and this package's own parser for it -- the same
// thin, discoverable aliasing execsites.go already uses for exec_sites.pending,
// not a third reimplementation of the same three-field parser.

// campaignTestNamesPendingHeader is written by FormatCampaignTestNamesPending
// and recognised (as ordinary "#"-prefixed comment lines) by
// ParseCampaignTestNamesPendingBytes (ParseUnitTierPendingBytes underneath).
const campaignTestNamesPendingHeader = "" +
	"# spec/plans/coverage-to-100 task-21 (AGENTS.md:108-112): one line per\n" +
	"# _test.go file where the campaign-test-name detector\n" +
	"# (internal/quality/campaigntestnames.go) still matches a forbidden\n" +
	"# zz_/cov_/_cov_/tailcov/dqcov file name or a TestCwCov*/TestTailCov*/\n" +
	"# TestDqCov*/TestZz* function name, beyond zero. Format:\n" +
	"# <path>\\t<count>\\t<owning task>. Seeded 2026-09-25 from main's own\n" +
	"# pre-existing violations (cov-rename-tests); the grand total may not\n" +
	"# rise against the base branch's copy of this file\n" +
	"# (ciaudit.CompareCampaignTestNamesPendingTotal, wired into `wb ci audit\n" +
	"# --target`) unless the same PR shrinks other entries by at least as\n" +
	"# much. Fix a violation by renaming the file and/or function after its\n" +
	"# actual behaviour (never merge campaign-named content into an existing\n" +
	"# file this list does not already name), then remove or shrink its\n" +
	"# entry in the same PR. Nothing this detector adds after the sweep may\n" +
	"# be added to this list -- rename it instead.\n"

// ParseCampaignTestNamesPending parses testdata/campaign_test_names.pending.
func ParseCampaignTestNamesPending(path string) (map[string]UnitTierPendingEntry, error) {
	return ParseUnitTierPending(path)
}

// ParseCampaignTestNamesPendingBytes parses campaign_test_names.pending
// content already read into memory (for example, a fetched target branch's
// copy).
func ParseCampaignTestNamesPendingBytes(data []byte, sourceName string) (map[string]UnitTierPendingEntry, error) {
	return ParseUnitTierPendingBytes(data, sourceName)
}

// FormatCampaignTestNamesPending renders entries back into
// campaign_test_names.pending's file format, sorted by path, under
// campaignTestNamesPendingHeader.
func FormatCampaignTestNamesPending(entries map[string]UnitTierPendingEntry) string {
	return formatPendingList(entries, campaignTestNamesPendingHeader)
}

// CampaignTestNamesPendingTotal sums every entry's count.
func CampaignTestNamesPendingTotal(entries map[string]UnitTierPendingEntry) int {
	return UnitTierPendingTotal(entries)
}
