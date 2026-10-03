package qualityrun

import (
	"bytes"
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/quality"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func changedFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range map[string]string{"go.mod": "module example.test/app\n\ngo 1.27\n", "app.go": "package app\nfunc Covered() int { return 1 }\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
func fakeChanged(t *testing.T, root string) changedOperations {
	t.Helper()
	return changedOperations{Abs: func(string) (string, error) { return root, nil }, MergeBase: func(context.Context, string, string) (string, error) { return "base", nil }, ChangedLines: func(context.Context, string, string) (quality.ChangedLines, error) {
		return quality.ChangedLines{}, nil
	}, TouchedFiles: func(context.Context, string, string) (map[string]bool, error) { return map[string]bool{}, nil }, LineOffsets: func(context.Context, string, string) (map[string]quality.FileLineOffsets, error) { return nil, nil }, Scope: func(context.Context, string, string, map[string]bool, bool) (quality.CoverageSelection, error) {
		return quality.CoverageSelection{Packages: []string{"."}, ChangedPackages: []string{"."}, Reason: "scope fixture"}, nil
	}, ProfilePath: func(path string) (string, bool, error) { return path, false, nil }, Cover: func(_ context.Context, _ string, _ string, o quality.RunOptions) quality.RepositoryCoverage {
		if err := os.WriteFile(o.CoverageProfile, []byte("mode: set\nexample.test/app/app.go:2.1,2.30 1 1\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return quality.RepositoryCoverage{Status: quality.StatusPassed}
	}, Baseline: func(context.Context, io.Writer, string, string, changedOptions) (quality.PackageBaseline, error) {
		return quality.PackageBaseline{SchemaVersion: 2, SHA: "base", Packages: map[string]int{"example.test/app": 0}}, nil
	}}
}
func changedRequest(t *testing.T) ChangedRequest {
	t.Helper()
	return ChangedRequest{Path: "input", Target: "main", Minimum: -1, Diagnostics: io.Discard, Run: quality.RunOptions{CoverageProfile: filepath.Join(t.TempDir(), "profile.cov")}}
}
func TestChangedWorkflowEffectErrorsAndPreflightOrder(t *testing.T) {
	root := changedFixture(t)
	boom := errors.New("effect sentinel")
	for _, stage := range []string{"abs", "module", "policy-tolerance", "merge-base", "diff", "touched", "scope", "offsets", "profile", "run-policy", "baseline", "profile-parse"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			fixture := root
			req := changedRequest(t)
			ops := fakeChanged(t, root)
			switch stage {
			case "abs":
				ops.Abs = func(string) (string, error) { return "", boom }
			case "module":
				fixture = t.TempDir()
				ops.Abs = func(string) (string, error) { return fixture, nil }
			case "policy-tolerance":
				fixture = changedFixture(t)
				if err := os.Mkdir(filepath.Join(fixture, ".wb"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fixture, ".wb", "coverage-ratchet.yaml"), []byte("[broken"), 0600); err != nil {
					t.Fatal(err)
				}
				ops.Abs = func(string) (string, error) { return fixture, nil }
				ops.MergeBase = func(context.Context, string, string) (string, error) {
					t.Fatal("Git called before tolerance validation")
					return "", nil
				}
			case "merge-base":
				ops.MergeBase = func(context.Context, string, string) (string, error) { return "", boom }
			case "diff":
				ops.ChangedLines = func(context.Context, string, string) (quality.ChangedLines, error) { return nil, boom }
			case "touched":
				ops.TouchedFiles = func(context.Context, string, string) (map[string]bool, error) { return nil, boom }
			case "scope":
				req.AffectedPackages = true
				ops.Scope = func(context.Context, string, string, map[string]bool, bool) (quality.CoverageSelection, error) {
					return quality.CoverageSelection{}, boom
				}
			case "offsets":
				ops.LineOffsets = func(context.Context, string, string) (map[string]quality.FileLineOffsets, error) { return nil, boom }
			case "profile":
				ops.ProfilePath = func(string) (string, bool, error) { return "", false, boom }
			case "run-policy":
				fixture = changedFixture(t)
				if err := os.Mkdir(filepath.Join(fixture, ".wb"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fixture, ".wb", "quality.yaml"), []byte("go_test:\n  shards: -1\n"), 0600); err != nil {
					t.Fatal(err)
				}
				ops.Abs = func(string) (string, error) { return fixture, nil }
			case "baseline":
				ops.Baseline = func(context.Context, io.Writer, string, string, changedOptions) (quality.PackageBaseline, error) {
					return quality.PackageBaseline{}, boom
				}
			case "profile-parse":
				ops.Cover = func(context.Context, string, string, quality.RunOptions) quality.RepositoryCoverage {
					return quality.RepositoryCoverage{Status: quality.StatusPassed}
				}
			}
			_, err := changedWith(t.Context(), req, ops)
			if err == nil {
				t.Fatal("accepted effect failure")
			}
			if stage != "module" && stage != "policy-tolerance" && stage != "run-policy" && stage != "profile-parse" && !errors.Is(err, boom) {
				t.Fatal(err)
			}
		})
	}
}
func TestChangedWorkflowScopeEmptyProfileAndPersistence(t *testing.T) {
	root := changedFixture(t)
	for _, mode := range []string{"empty", "write-error", "persist-error", "selection-read-error", "affected"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			req := changedRequest(t)
			req.Minimum = 100
			req.Run.GoTestPackages = []string{"./deleted"}
			ops := fakeChanged(t, root)
			ops.Cover = func(context.Context, string, string, quality.RunOptions) quality.RepositoryCoverage {
				t.Fatal("empty selection measured")
				return quality.RepositoryCoverage{}
			}
			switch mode {
			case "write-error":
				req.Run.CoverageProfile = t.TempDir()
			case "persist-error":
				req.ReportDir = filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(req.ReportDir, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "selection-read-error":
				req.Run.GoTestPackages = []string{"./go.mod"}
			case "affected":
				req.AffectedPackages = true
				ops.Scope = func(context.Context, string, string, map[string]bool, bool) (quality.CoverageSelection, error) {
					return quality.CoverageSelection{Packages: []string{"./deleted"}, ChangedPackages: []string{"./deleted"}, Reason: "deleted"}, nil
				}
			}
			got, err := changedWith(t.Context(), req, ops)
			if mode == "write-error" || mode == "persist-error" || mode == "selection-read-error" {
				if err == nil {
					t.Fatalf("%s accepted", mode)
				}
				return
			}
			if err != nil || !got.HasReport || got.Findings != "" || len(got.Report.Scope) != 1 {
				t.Fatalf("result=%+v err=%v", got, err)
			}
			raw, err := os.ReadFile(req.Run.CoverageProfile)
			if err != nil || string(raw) != "mode: set\n" {
				t.Fatalf("profile=%s err=%v", raw, err)
			}
			if mode == "affected" && got.Report.ScopeReason != "deleted" {
				t.Fatal(got.Report)
			}
		})
	}
}
func TestChangedWorkflowMeasurementFailureAndDiagnostics(t *testing.T) {
	t.Parallel()
	root := changedFixture(t)
	for _, diagnostic := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "manifest"}[diagnostic], func(t *testing.T) {
			t.Parallel()
			req := changedRequest(t)
			ops := fakeChanged(t, root)
			ops.Cover = func(ctx context.Context, name, path string, o quality.RunOptions) quality.RepositoryCoverage {
				if ctx != t.Context() || path != root || name != filepath.Base(root) {
					t.Error("context/path identity lost")
				}
				report := quality.RepositoryCoverage{Status: quality.StatusFailed, Error: "compile failed"}
				if diagnostic {
					report.Diagnostic = &quality.CoverageDiagnostic{Manifest: "manifest.json"}
				}
				return report
			}
			got, err := changedWith(t.Context(), req, ops)
			if err != nil || got.HasReport || !strings.Contains(got.Findings, "compile failed") {
				t.Fatalf("result=%+v err=%v", got, err)
			}
			if diagnostic && !strings.Contains(got.Findings, "manifest.json") {
				t.Fatal(got.Findings)
			}
		})
	}
}
func TestChangedWorkflowCallbackBeforeWriteAndSelectedOptions(t *testing.T) {
	root := changedFixture(t)
	for _, mode := range []string{"success", "persistence-error", "temporary-profile", "minimum", "ratchet"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			req := changedRequest(t)
			req.AffectedPackages = true
			req.ReportDir = t.TempDir()
			ops := fakeChanged(t, root)
			called := false
			req.BeforePersist = func(report ChangedReport) {
				called = true
				if report.Target != "main" || report.ScopeReason != "scope fixture" {
					t.Error(report)
				}
				if _, err := os.Stat(filepath.Join(req.ReportDir, "coverage-ratchet.json")); mode != "persistence-error" && !os.IsNotExist(err) {
					t.Errorf("callback after persistence: %v", err)
				}
			}
			ops.Baseline = func(_ context.Context, _ io.Writer, _ string, _ string, o changedOptions) (quality.PackageBaseline, error) {
				if !o.explicitGoTestPackages || len(o.packagePatterns) != 1 || o.packagePatterns[0] != "." || len(o.runOptions().GoTestPackages) != 1 || o.runOptions().GoTestPackages[0] != "." {
					t.Errorf("baseline options=%+v", o)
				}
				return quality.PackageBaseline{Packages: map[string]int{"example.test/app": 0}}, nil
			}
			cover := ops.Cover
			ops.Cover = func(ctx context.Context, name, path string, o quality.RunOptions) quality.RepositoryCoverage {
				if len(o.GoTestPackages) != 1 || o.GoTestPackages[0] != "." {
					t.Error(o.GoTestPackages)
				}
				result := cover(ctx, name, path, o)
				if mode == "minimum" || mode == "ratchet" {
					if err := os.WriteFile(o.CoverageProfile, []byte("mode: set\nexample.test/app/app.go:2.1,2.30 1 0\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return result
			}
			if mode == "persistence-error" {
				if err := os.Mkdir(filepath.Join(req.ReportDir, "coverage-ratchet.json"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "temporary-profile" {
				ops.ProfilePath = func(path string) (string, bool, error) { return path, true, nil }
			}
			if mode == "minimum" {
				req.Minimum = 100
			}
			if mode == "ratchet" {
				ops.TouchedFiles = func(context.Context, string, string) (map[string]bool, error) {
					return map[string]bool{"app.go": true}, nil
				}
			}
			got, err := changedWith(t.Context(), req, ops)
			if !called {
				t.Fatal("missing pre-persistence callback")
			}
			if mode == "persistence-error" {
				if err == nil {
					t.Fatal("write succeeded")
				}
				return
			}
			if err != nil || !got.HasReport {
				t.Fatalf("result=%+v err=%v", got, err)
			}
			if mode == "temporary-profile" {
				if _, err := os.Stat(req.Run.CoverageProfile); !os.IsNotExist(err) {
					t.Errorf("temporary profile remains: %v", err)
				}
			}
			if mode == "minimum" && !strings.Contains(got.Findings, "below required") {
				t.Fatal(got.Findings)
			}
			if mode == "ratchet" && !strings.Contains(got.Findings, "coverage ratchet failed") {
				t.Fatal(got.Findings)
			}
		})
	}
}
func TestChangedFindingsSortAndMinimum(t *testing.T) {
	t.Parallel()
	got := changedCoverageRatchetFindings([]quality.PackageRatchet{{Package: "z", Rose: true, Uncovered: 2, BaselineUncovered: 1, NewlyUncoveredChanged: []quality.RatchetFinding{{File: "a.go", Line: 2, Reason: "new"}}}, {Pass: true}})
	if !strings.Contains(got, "a.go:2: new\n  z: uncovered count 2 rose above baseline 1") {
		t.Fatal(got)
	}
	if got := changedCoverageMinimumFindings([]quality.CoverageBlock{{Statements: 2, Count: 1}}, 101); !strings.Contains(got, "100.00%") {
		t.Fatal(got)
	}
	for _, minimum := range []float64{-1, 0} {
		if got := changedCoverageMinimumFindings(nil, minimum); got != "" {
			t.Fatal(got)
		}
	}
	if got := changedCoverageMinimumFindings(nil, 1); !strings.Contains(got, "0.00%") {
		t.Fatal(got)
	}
}
func TestPublicChangedCoverageAndRealProfileBinding(t *testing.T) {
	t.Parallel()
	if _, err := ChangedCoverage(t.Context(), ChangedRequest{Path: t.TempDir(), Diagnostics: io.Discard}); err == nil {
		t.Fatal("missing module accepted")
	}
	path, remove, err := realChangedOperations().ProfilePath("")
	if err != nil || !remove || path == "" {
		t.Fatalf("%s %t %v", path, remove, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
func TestBaselineArtifactValidationAndDiagnostics(t *testing.T) {
	t.Parallel()
	root := changedFixture(t)
	for _, mode := range []string{"missing", "unusable", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "baseline.json")
			if mode != "missing" {
				raw := []byte("{}")
				if mode == "malformed" {
					raw = []byte("not json")
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var stderr bytes.Buffer
			_, err := loadOrMeasureBaseline(t.Context(), &stderr, root, "base", changedOptions{baselineFile: path})
			if err == nil {
				t.Fatal("bad artifact accepted")
			}
			if mode == "malformed" {
				if !strings.Contains(err.Error(), "--baseline-file") {
					t.Fatal(err)
				}
			} else if !strings.Contains(stderr.String(), "measuring merge base") {
				t.Fatal(stderr.String())
			}
		})
	}
}
