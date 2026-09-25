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
	}
	for _, name := range forbidden {
		if !campaignFileNameViolation(name) {
			t.Errorf("campaignFileNameViolation(%q) = false, want true", name)
		}
	}
}

func TestCampaignFileNameViolationLeavesBehaviourNamesAlone(t *testing.T) {
	t.Parallel()
	clean := []string{
		"deps_bump_test.go",
		"coverage_worklist_test.go",
		"coverage_config_hkcov_test.go", // "hkcov" -- no "_cov_", no "cov_" prefix, no "zz_"
		"disk_test.go",
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
	}
	for _, name := range forbidden {
		if !campaignFuncNameViolation(name) {
			t.Errorf("campaignFuncNameViolation(%q) = false, want true", name)
		}
	}
}

// TestCampaignFuncNameViolationLeavesBehaviourNamesAlone pins the boundary
// campaignFuncNameToken's own doc comment promises: a name that merely
// starts with the letters "Cov" (or "Tailcov"/"Dqcov"/"Zz") without the
// campaign token being a whole name segment must never match -- this is
// the exact false-positive shape an earlier, looser draft of this detector
// hit against TestCoverageProfilePathInjected during the 2026-09-25 sweep.
func TestCampaignFuncNameViolationLeavesBehaviourNamesAlone(t *testing.T) {
	t.Parallel()
	clean := []string{
		"TestCoverageProfilePathInjectedKeepsAnExplicitProfile",
		"TestDepsBumpWithRegistryPolicy",
		"TestZazzleNameIsNotZzTokenEither", // "Zazzle" does not start with the "Zz" token at all
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
