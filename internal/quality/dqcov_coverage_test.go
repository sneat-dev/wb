package quality

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDqCovCoverDelegatesToDefaultOptions pins the exported wrapper: a tree
// with no Go module is reported skipped rather than failed, and the caller's
// repository and path survive into the report.
func TestDqCovCoverDelegatesToDefaultOptions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	report := Cover(context.Background(), "example/delegated", root)
	if report.Status != StatusSkipped || report.Repository != "example/delegated" || report.Path != root {
		t.Fatalf("Cover report = %+v, want a skipped report carrying caller identity", report)
	}
	if report.Error != "" || report.Statements != 0 || report.Percentage != 0 {
		t.Fatalf("Cover report = %+v, want no partial totals", report)
	}
}

// TestDqCovCoverWithOptionsFailsClosedBeforeRunningCommands covers the
// discovery-time rejections: an unparsable go.work, a tree with no module, and
// a retained profile requested for more than one module.
func TestDqCovCoverWithOptionsFailsClosedBeforeRunningCommands(t *testing.T) {
	t.Parallel()
	t.Run("unparsable go.work", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeQualityFile(t, filepath.Join(root, "go.work"), "this is not a go.work file\n")
		report := CoverWithOptions(context.Background(), "example/broken", root, RunOptions{})
		if report.Status != StatusFailed || !strings.Contains(report.Error, "parse go.work") {
			t.Fatalf("report = %+v, want a parse failure", report)
		}
	})

	t.Run("no go module", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		report := CoverWithOptions(context.Background(), "example/empty", root, RunOptions{CoverageDiagnosticsRepository: "example/empty"})
		if report.Status != StatusSkipped || len(report.Modules) != 0 {
			t.Fatalf("report = %+v, want an empty tree skipped", report)
		}
	})

	t.Run("retained profile with two modules", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeQualityFile(t, filepath.Join(root, "one", "go.mod"), "module example.test/one\n\ngo 1.24\n")
		writeQualityFile(t, filepath.Join(root, "two", "go.mod"), "module example.test/two\n\ngo 1.24\n")
		report := CoverWithOptions(context.Background(), "example/two-modules", root, RunOptions{CoverageProfile: filepath.Join(root, "retained.out")})
		if report.Status != StatusFailed || !strings.Contains(report.Error, "requires exactly one Go module") {
			t.Fatalf("report = %+v, want the single-module requirement enforced", report)
		}
	})
}

// TestDqCovCoverWithOptionsRetainsProfileAndRejectsInvalidProfile uses the
// injected `go` shim to separate two behaviours a real toolchain cannot produce
// cheaply: a retained profile is reused in place, and a profile the shim did not
// rewrite is rejected as unparsable.
func TestDqCovCoverWithOptionsRetainsProfileAndRejectsInvalidProfile(t *testing.T) {
	root := t.TempDir()
	writeQualityFile(t, filepath.Join(root, "go.mod"), "module example.test/retained\n\ngo 1.24\n")
	dqCovFakeGo(t, root)
	retained := filepath.Join(root, "retained.out")

	t.Run("valid profile retained in place", func(t *testing.T) {
		t.Parallel()
		writeQualityFile(t, retained, "mode: set\nexample/a.go:1.1,2.2 4 3\nexample/b.go:1.1,2.2 1 0\n")
		report := CoverWithOptions(context.Background(), "example/retained", root, RunOptions{CoverageProfile: retained})
		if report.Status != StatusPassed || report.Statements != 5 || report.Covered != 4 {
			t.Fatalf("report = %+v, want the retained profile totals", report)
		}
		if _, err := os.Stat(retained); err != nil {
			t.Fatalf("retained profile was removed: %v", err)
		}
	})

	t.Run("unparsable profile fails", func(t *testing.T) {
		t.Parallel()
		writeQualityFile(t, retained, "mode: set\nthis line has too many fields here\n")
		report := CoverWithOptions(context.Background(), "example/retained", root, RunOptions{CoverageProfile: retained})
		if report.Status != StatusFailed || !strings.Contains(report.Error, "invalid coverage profile") {
			t.Fatalf("report = %+v, want an invalid-profile failure", report)
		}
		if _, err := os.Stat(retained); err != nil {
			t.Fatalf("caller-supplied profile was removed: %v", err)
		}
	})
}

// TestDqCovCoverageProfilePathRetainsOrCreatesTemp pins both productions: a
// caller-supplied path is absolutised and never removed, and an unusable
// temporary root is surfaced instead of silently ignored.
func TestDqCovCoverageProfilePathRetainsOrCreatesTemp(t *testing.T) {
	absolute, remove, err := coverageProfilePath(filepath.Join("nested", "profile.out"))
	if err != nil {
		t.Fatal(err)
	}
	want, absErr := filepath.Abs(filepath.Join("nested", "profile.out"))
	if absErr != nil {
		t.Fatal(absErr)
	}
	if absolute != want || remove {
		t.Fatalf("retained path = %q remove=%v, want %q remove=false", absolute, remove, want)
	}

	generated, remove, err := coverageProfilePath("")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(generated) }()
	if !remove || !strings.HasSuffix(generated, ".out") {
		t.Fatalf("generated path = %q remove=%v, want a removable *.out file", generated, remove)
	}
	if _, err := os.Stat(generated); err != nil {
		t.Fatalf("generated profile does not exist: %v", err)
	}

	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	if _, _, err := coverageProfilePath(""); err == nil {
		t.Fatal("unusable temporary root was accepted")
	}
}

// TestDqCovNewCoverageReportSortsAndAggregates pins deterministic ordering and
// percentage aggregation across repositories supplied in arbitrary order.
func TestDqCovNewCoverageReportSortsAndAggregates(t *testing.T) {
	t.Parallel()
	report := NewCoverageReport([]RepositoryCoverage{
		{Repository: "zulu/repo", Statements: 10, Covered: 5},
		{Repository: "alpha/repo", Statements: 30, Covered: 30},
	})
	if report.SchemaVersion != 1 || len(report.Repositories) != 2 {
		t.Fatalf("report = %+v, want schema 1 with both repositories", report)
	}
	if report.Repositories[0].Repository != "alpha/repo" || report.Repositories[1].Repository != "zulu/repo" {
		t.Fatalf("repositories = %+v, want deterministic name order", report.Repositories)
	}
	if report.Statements != 40 || report.Covered != 35 || report.Percentage != 87.5 {
		t.Fatalf("totals = %d/%d %.2f%%, want 35/40 87.50%%", report.Covered, report.Statements, report.Percentage)
	}
}

// TestDqCovGoModulesDiscoversWorkAndWalkTrees covers every discovery failure
// mode and the directory pruning that keeps generated trees out of module
// discovery.
func TestDqCovGoModulesDiscoversWorkAndWalkTrees(t *testing.T) {
	t.Parallel()
	t.Run("unparsable go.work", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeQualityFile(t, filepath.Join(root, "go.work"), "not a workspace file\n")
		if _, err := goModules(root); err == nil || !strings.Contains(err.Error(), "parse go.work") {
			t.Fatalf("error = %v, want a parse failure", err)
		}
	})

	t.Run("go.work use without go.mod", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeQualityFile(t, filepath.Join(root, "go.work"), "go 1.24\n\nuse ./missing\n")
		if _, err := goModules(root); err == nil || !strings.Contains(err.Error(), "has no readable go.mod") {
			t.Fatalf("error = %v, want a missing go.mod failure", err)
		}
	})

	t.Run("unreadable go.work", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "go.work"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := goModules(root); err == nil || !strings.Contains(err.Error(), "read go.work") {
			t.Fatalf("error = %v, want a read failure", err)
		}
	})

	t.Run("walk prunes generated trees", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeQualityFile(t, filepath.Join(root, ".git", "go.mod"), "module example.test/git\n")
		writeQualityFile(t, filepath.Join(root, "vendor", "go.mod"), "module example.test/vendor\n")
		writeQualityFile(t, filepath.Join(root, "node_modules", "go.mod"), "module example.test/node\n")
		writeQualityFile(t, filepath.Join(root, "service", "go.mod"), "module example.test/service\n")
		modules, err := goModules(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(modules) != 1 || filepath.Base(modules[0]) != "service" {
			t.Fatalf("modules = %v, want only the real module", modules)
		}
	})

	t.Run("walk failure surfaces", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeQualityFile(t, filepath.Join(root, "service", "go.mod"), "module example.test/service\n")
		denied := filepath.Join(root, "denied")
		if err := os.Mkdir(denied, 0o755); err != nil {
			t.Fatal(err)
		}
		dqCovChmod(t, denied, 0)
		if _, err := goModules(root); err == nil {
			t.Fatal("an unreadable subtree was silently accepted")
		}
	})
}

// TestDqCovProfileTotalsRejectsInvalidProfiles asserts each malformed profile
// shape fails with a diagnostic naming the file, rather than reporting a
// misleading total.
func TestDqCovProfileTotalsRejectsInvalidProfiles(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	for _, tc := range []struct {
		name     string
		contents string
		want     string
	}{
		{name: "wrong field count", contents: "mode: set\nexample.go:1.1,1.2 3\n", want: "invalid coverage profile"},
		{name: "non-numeric fields", contents: "mode: set\nexample.go:1.1,1.2 three one\n", want: "invalid coverage profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(directory, strings.ReplaceAll(tc.name, " ", "-")+".out")
			writeQualityFile(t, path, tc.contents)
			if _, _, err := profileTotals(path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}

	if _, _, err := profileTotals(filepath.Join(directory, "absent.out")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing profile error = %v, want a not-exist error", err)
	}
}

func TestDqCovPercentHandlesZeroAndPartialStatements(t *testing.T) {
	t.Parallel()
	if got := percent(0, 0); got != 0 {
		t.Fatalf("percent(0, 0) = %v, want 0", got)
	}
	if got := percent(1, 4); got != 25 {
		t.Fatalf("percent(1, 4) = %v, want 25", got)
	}
}

// TestDqCovCoverWithOptionsReportsUnusableTemporaryRoot pins the fail-closed
// path taken when the coverage profile scratch file cannot be created: the
// repository is reported failed before any test command runs and no module
// evidence is recorded.
func TestDqCovCoverWithOptionsReportsUnusableTemporaryRoot(t *testing.T) {
	root := t.TempDir()
	writeQualityFile(t, filepath.Join(root, "go.mod"), "module example.test/single\n\ngo 1.24\n")
	absent := filepath.Join(root, "missing-tmp")
	t.Setenv("TMPDIR", absent)
	t.Setenv("TMP", absent)
	t.Setenv("TEMP", absent)
	report := CoverWithOptions(context.Background(), "example/unusable-tmp", root, RunOptions{})
	if report.Status != StatusFailed {
		t.Fatalf("report = %+v, want a failed report", report)
	}
	if !strings.Contains(report.Error, "missing-tmp") {
		t.Fatalf("report error = %q, want the temporary-root failure", report.Error)
	}
	if len(report.Modules) != 0 || report.Covered != 0 || report.Statements != 0 {
		t.Fatalf("report = %+v, want no module evidence after a scratch failure", report)
	}
}
