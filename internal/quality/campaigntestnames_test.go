package quality

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCampaignFileNameViolationFlagsEveryForbiddenFragment(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		"zz_rwi01_deps_test.go",
		"cov_helpers_test.go",
		"zz_cov_bridge_test.go",
		"api_tailcov_http_test.go",
		"api_dqcov_service_test.go",
		// The rwi/pkp/pk0/cgx/pr9 lineage 997a4201 renamed away
		// (review round 1, B3): these must still be caught even
		// without a "zz_" prefix, since a future brief could reuse
		// the token on its own.
		"agent_remote_rwi01_test.go",
		"session_park_pkp00_test.go",
		"disk_pk0_test.go",
		"branch_cgxc1_test.go",
		"pr9_filewrite_test.go",
	}
	for _, name := range forbidden {
		if !campaignFileNameViolation(name) {
			t.Errorf("campaignFileNameViolation(%q) = false, want true", name)
		}
	}
}

// TestCampaignFileNameViolationLeavesBehaviourNamesAlone pins both the
// "cov"-lineage false-positive boundary and the rwi/pkp/pk0/cgx/pr9 one
// (review round 1, B3: "a word like pkg can't match"), using real file
// names that exist in this repository today wherever one is close enough
// to a token to be worth pinning, not only synthetic probes.
func TestCampaignFileNameViolationLeavesBehaviourNamesAlone(t *testing.T) {
	t.Parallel()
	clean := []string{
		"deps_bump_test.go",
		"coverage_worklist_test.go",
		"coverage_config_hkcov_test.go", // "hkcov" -- no "_cov_", no "cov_" prefix, no "zz_"
		"disk_test.go",
		"pr_create_link_preflight_test.go", // real file (cmd/wb): "pr_" prefix, never "pr9_"
		"pr_test.go",                       // real file (cmd/wb): "pr_" only, no digit
		"daemon_process_darwin_test.go",    // real file (cmd/wb), no lineage token at all
		"a_pkg_test.go",                    // "pkg" is a whole word, not the "pkp"/"pk0" token
		"network_test.go",                  // "network" contains no "rwi" substring
		"cgxray_test.go",                   // "cgx" not underscore-bounded on the right
	}
	for _, name := range clean {
		if campaignFileNameViolation(name) {
			t.Errorf("campaignFileNameViolation(%q) = true, want false", name)
		}
	}
}

func TestCampaignFuncNameViolationFlagsEveryForbiddenToken(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		"TestCwCovServiceHandlesFailure",
		"TestTailCovWorktreeCleansUp",
		"TestDqCovStoreRejectsBadInput",
		"TestZzBridgeStartsSuccessfully",
		"TestCwCov", // the bare token itself, with no descriptor after it
		// The rwi/pkp/pk0/cgx lineage (review round 1, B3), including
		// the all-caps RWI variant main's own violations actually use.
		"TestRwi01DepsBumpWithRegistryPolicy",
		"TestRWI08LocateOneMatch",
		"TestPkp00StatusOutputTextReportsMissingPullRequests",
		"TestPk0FooBarDoesSomething",
		"TestCgxc1BranchListSwitchFormatCoversEveryBranch",
	}
	for _, name := range forbidden {
		if !campaignFuncNameViolation(name) {
			t.Errorf("campaignFuncNameViolation(%q) = false, want true", name)
		}
	}
}

// TestCampaignFuncNameViolationLeavesBehaviourNamesAlone pins the boundary
// both campaignFuncNameToken's and campaignFuncNameLineageToken's own doc
// comments promise: a name that merely starts with the letters "Cov" (or
// "Tailcov"/"Dqcov"/"Zz"/"Rwi"/"Pkp"/"Pk0"/"Cgx") without the campaign
// token being a whole name segment (immediately followed by the boundary
// each pattern requires) must never match -- TestCoverageProfilePathInjected
// is the exact false-positive shape an earlier, looser draft of this
// detector hit during the 2026-09-25 sweep; TestPackageDirForFallsBackToPathWhenNotRelatable
// and TestRewriteAndStampNeverSetPermissionDecision are real, currently
// passing test names in this repository picked for how close their
// prefixes sit to the rwi/pkp/pk0/cgx tokens without ever forming one.
func TestCampaignFuncNameViolationLeavesBehaviourNamesAlone(t *testing.T) {
	t.Parallel()
	clean := []string{
		"TestCoverageProfilePathInjectedKeepsAnExplicitProfile",
		"TestDepsBumpWithRegistryPolicy",
		"TestZazzleNameIsNotZzTokenEither",                 // "Zazzle" does not start with the "Zz" token at all
		"TestPackageDirForFallsBackToPathWhenNotRelatable", // real repo name: "Pack" not "Pkp"/"Pk0"
		"TestRewriteAndStampNeverSetPermissionDecision",    // real repo name: "Rewr" not "Rwi"
		"TestPkgManagerDetectsWorkspaceRoot",               // "Pkg" is not the "Pkp"/"Pk0" token
		"TestCoverageGroupSkipsPackagesOutsideChangedSet",  // "Group" after "Cov" -- neither Cov-family nor cgx-family token
	}
	for _, name := range clean {
		if campaignFuncNameViolation(name) {
			t.Errorf("campaignFuncNameViolation(%q) = true, want false", name)
		}
	}
}

func TestFindCampaignTestNameMatchesFlagsBothTheFileNameAndTheFunctionName(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeQualityFile(t, filepath.Join(root, "pkg", "zz_cov_thing_test.go"), `package pkg

import "testing"

func TestCwCovThingWorks(t *testing.T) {}

func TestOrdinaryBehaviourNameWorks(t *testing.T) {}
`)
	matches, err := FindCampaignTestNameMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %+v, want exactly 2 (the file name and TestCwCovThingWorks)", matches)
	}
	if matches[0].Kind != CampaignTestNameKindFile || matches[0].Name != "zz_cov_thing_test.go" {
		t.Fatalf("matches[0] = %+v, want the file-name match first (sorted by line)", matches[0])
	}
	if matches[1].Kind != CampaignTestNameKindFunc || matches[1].Name != "TestCwCovThingWorks" {
		t.Fatalf("matches[1] = %+v, want the TestCwCovThingWorks func match", matches[1])
	}
	counts := CountCampaignTestNameMatchesByFile(matches)
	if got := counts["pkg/zz_cov_thing_test.go"]; got != 2 {
		t.Fatalf("CountCampaignTestNameMatchesByFile = %d, want 2", got)
	}
}

func TestFindCampaignTestNameMatchesIgnoresANonTestGoFileEvenWithACampaignName(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeQualityFile(t, filepath.Join(root, "pkg", "zz_cov_thing.go"), `package pkg

func zzCovThing() {}
`)
	matches, err := FindCampaignTestNameMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want none for a non-_test.go file", matches)
	}
}

func TestFindCampaignTestNameMatchesIgnoresACleanlyNamedFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `package pkg

import "testing"

func TestThingReportsAnError(t *testing.T) {}
`)
	matches, err := FindCampaignTestNameMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want none", matches)
	}
}

func TestFindCampaignTestNameMatchesIgnoresAMethodNamedLikeAViolation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `package pkg

type suite struct{}

// TestCwCovMethod has a receiver, so it is not a Go test function at all
// (go test never calls it) and must not be flagged as one.
func (s suite) TestCwCovMethod() {}
`)
	matches, err := FindCampaignTestNameMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want none for a method, not a package-level test func", matches)
	}
}

func TestFindCampaignTestNameMatchesSkipsVCSVendorAndHiddenDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeQualityFile(t, filepath.Join(root, ".git", "zz_cov_thing_test.go"), `package pkg
func TestCwCovThingWorks(t *testing.T) {}
`)
	writeQualityFile(t, filepath.Join(root, "vendor", "zz_cov_thing_test.go"), `package pkg
func TestCwCovThingWorks(t *testing.T) {}
`)
	writeQualityFile(t, filepath.Join(root, "node_modules", "zz_cov_thing_test.go"), `package pkg
func TestCwCovThingWorks(t *testing.T) {}
`)
	writeQualityFile(t, filepath.Join(root, ".hidden", "zz_cov_thing_test.go"), `package pkg
func TestCwCovThingWorks(t *testing.T) {}
`)
	matches, err := FindCampaignTestNameMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want none: every fixture sits under a skipped directory", matches)
	}
}

func TestFindCampaignTestNameMatchesReportsAParseError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeQualityFile(t, filepath.Join(root, "pkg", "zz_cov_broken_test.go"), `package pkg

func TestBroken( {
`)
	if _, err := FindCampaignTestNameMatches(root); err == nil {
		t.Fatal("want an error for an unparsable _test.go file")
	}
}

func TestFindCampaignTestNameMatchesRejectsAMissingRoot(t *testing.T) {
	t.Parallel()
	if _, err := FindCampaignTestNameMatches(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("want an error for a root that does not exist")
	}
}

// TestCampaignTestNameMatchStringNamesTheKind pins CampaignTestNameMatch's
// String method, the same "one readable line" contract
// UnitTierMatch.String and ExecSiteMatch.String already promise their own
// callers.
func TestCampaignTestNameMatchStringNamesTheKind(t *testing.T) {
	t.Parallel()
	m := CampaignTestNameMatch{File: "pkg/zz_cov_thing_test.go", Line: 1, Kind: CampaignTestNameKindFile, Name: "zz_cov_thing_test.go"}
	got := m.String()
	if got == "" {
		t.Fatal("String() returned an empty string")
	}
	for _, needle := range []string{"pkg/zz_cov_thing_test.go", "zz_cov_thing_test.go", string(CampaignTestNameKindFile)} {
		if !strings.Contains(got, needle) {
			t.Fatalf("String() = %q, want it to contain %q", got, needle)
		}
	}
}

// TestFormatCampaignTestNamesPendingRoundTripsThroughParse is the
// campaign_test_names.pending analogue of
// TestFormatUnitTierPendingRoundTripsThroughParse and
// TestParseExecSitesPendingRoundTripsThroughFormat: FormatCampaignTestNamesPending
// has no production caller (the committed testdata/campaign_test_names.pending
// is hand-seeded from a live detector run, not machine-regenerated on every
// run, the same way exec_sites.pending and unit_tier.pending are not), so
// this test is what actually exercises it -- the same shape those two
// siblings already use for their own otherwise-uncalled Format functions.
func TestFormatCampaignTestNamesPendingRoundTripsThroughParse(t *testing.T) {
	t.Parallel()
	entries := map[string]UnitTierPendingEntry{
		"b_zz_test.go": {File: "b_zz_test.go", Count: 2, Owner: "task-21"},
		"a_zz_test.go": {File: "a_zz_test.go", Count: 1, Owner: "task-21"},
	}
	formatted := FormatCampaignTestNamesPending(entries)
	lines := strings.Split(strings.TrimRight(formatted, "\n"), "\n")
	var dataLines []string
	for _, l := range lines {
		if !strings.HasPrefix(l, "#") && l != "" {
			dataLines = append(dataLines, l)
		}
	}
	if len(dataLines) != 2 || dataLines[0] != "a_zz_test.go\t1\ttask-21" || dataLines[1] != "b_zz_test.go\t2\ttask-21" {
		t.Fatalf("formatted data lines = %+v (from %q)", dataLines, formatted)
	}

	root := t.TempDir()
	path := filepath.Join(root, "campaign_test_names.pending")
	writeQualityFile(t, path, formatted)
	roundTripped, err := ParseCampaignTestNamesPending(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(roundTripped) != 2 || roundTripped["a_zz_test.go"] != entries["a_zz_test.go"] || roundTripped["b_zz_test.go"] != entries["b_zz_test.go"] {
		t.Fatalf("round-tripped = %+v, want %+v", roundTripped, entries)
	}
}

// TestFormatCampaignTestNamesPendingUsesItsOwnHeaderNotItsSiblings pins that
// FormatCampaignTestNamesPending writes campaignTestNamesPendingHeader, not
// unitTierPendingHeader or execSitesPendingHeader -- formatPendingList is
// shared across all three Format functions (unittier.go), and a header
// naming the wrong detector or the wrong `wb ci audit --target` ratchet in
// a committed campaign_test_names.pending would point a reviewer at the
// wrong owner and the wrong comparator.
func TestFormatCampaignTestNamesPendingUsesItsOwnHeaderNotItsSiblings(t *testing.T) {
	t.Parallel()
	entries := map[string]UnitTierPendingEntry{"a_zz_test.go": {File: "a_zz_test.go", Count: 1, Owner: "task-21"}}
	formatted := FormatCampaignTestNamesPending(entries)
	if !strings.Contains(formatted, "task-21") || !strings.Contains(formatted, "CompareCampaignTestNamesPendingTotal") {
		t.Fatalf("FormatCampaignTestNamesPending output missing its own header:\n%s", formatted)
	}
	if strings.Contains(formatted, "task-24") || strings.Contains(formatted, "CompareUnitTierPendingTotal") {
		t.Fatalf("FormatCampaignTestNamesPending output uses unit_tier.pending's header:\n%s", formatted)
	}
	if strings.Contains(formatted, "task-8") || strings.Contains(formatted, "CompareExecSitesPendingTotal") {
		t.Fatalf("FormatCampaignTestNamesPending output uses exec_sites.pending's header:\n%s", formatted)
	}
	if formatted == FormatUnitTierPending(entries) || formatted == FormatExecSitesPending(entries) {
		t.Fatal("FormatCampaignTestNamesPending must not render identically to a sibling Format function")
	}
}

// TestParseCampaignTestNamesPendingBytesRejectsInvalidCount is the
// campaign_test_names.pending analogue of
// TestParseExecSitesPendingBytesRejectsInvalidCount, exercising
// ParseCampaignTestNamesPendingBytes directly in this package (it is also
// covered via internal/ciaudit's own tests of
// compareCampaignTestNamesPendingTotal, but pinning it here keeps this
// package's own coverage independent of that caller).
func TestParseCampaignTestNamesPendingBytesRejectsInvalidCount(t *testing.T) {
	t.Parallel()
	if _, err := ParseCampaignTestNamesPendingBytes([]byte("a_test.go\tnotanumber\ttask-21\n"), "source"); err == nil {
		t.Fatal("want an error for a non-numeric count")
	}
}
