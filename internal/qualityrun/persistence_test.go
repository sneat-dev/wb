package qualityrun

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/quality"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoverageArtifactsBindExactBytesAndSortedPrivateDiagnostics(t *testing.T) {
	t.Parallel()
	report := quality.NewCoverageReport([]quality.RepositoryCoverage{{Repository: "z", Status: quality.StatusFailed, Error: "failure detail", Diagnostic: &quality.CoverageDiagnostic{Manifest: "z.json", SHA256: "z-digest"}}, {Repository: "a", Status: quality.StatusPassed, Diagnostic: &quality.CoverageDiagnostic{Manifest: "a.json", SHA256: "a-digest"}}})
	dir := t.TempDir()
	refs, err := PersistCoverage(report, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []ArtifactReference{refs.Report, *refs.Diagnostics} {
		raw, err := os.ReadFile(ref.Path)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != ref.SHA256 {
			t.Errorf("digest does not bind %s", ref.Path)
		}
	}
	info, err := os.Stat(refs.Diagnostics.Path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private permissions: %v %v", info, err)
	}
	raw, err := os.ReadFile(refs.Diagnostics.Path)
	if err != nil {
		t.Fatal(err)
	}
	var index coverageDiagnosticIndex
	if err := yaml.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	if index.SchemaVersion != 1 || len(index.Repositories) != 2 || index.Repositories[0].Repository != "a" || index.Repositories[1].Manifest != "z.json" {
		t.Fatal(index)
	}
	md, err := os.ReadFile(filepath.Join(dir, "coverage.md"))
	if err != nil || !strings.Contains(string(md), "failure detail") {
		t.Fatalf("markdown=%s %v", md, err)
	}
}
func TestReportWriteFailuresPreserveConcretePathErrors(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"coverage.md", "coverage.yaml", "coverage-diagnostics.yaml", "verify.md", "verify.yaml"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			blocked := filepath.Join(dir, name)
			if err := os.Mkdir(blocked, 0700); err != nil {
				t.Fatal(err)
			}
			var err error
			if strings.HasPrefix(name, "coverage") {
				_, err = PersistCoverage(quality.CoverageReport{Repositories: []quality.RepositoryCoverage{{Diagnostic: &quality.CoverageDiagnostic{Manifest: "diagnostic"}}}}, dir)
			} else {
				err = PersistVerification(VerificationIndex{}, dir, "verify")
			}
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := PersistVerification(VerificationIndex{}, blocked, "verify"); err == nil {
		t.Fatal("mkdir succeeded")
	}
}
func TestVerificationMarkdownKeepsEmptyRowsAndFailureDetail(t *testing.T) {
	t.Parallel()
	if text := VerificationMarkdown(cwCovVerificationFixture()); !strings.Contains(text, "boom") {
		t.Fatal(text)
	}
	report := VerificationIndex{Profile: "fast", Checks: []quality.Check{quality.CheckTest}, Repositories: []quality.VerificationReport{{Repository: "empty", Status: quality.StatusSkipped}, {Repository: "failed", Results: []quality.VerificationEntry{{Check: quality.CheckTest, Status: quality.StatusFailed, Detail: "specific output"}}}}}
	text := VerificationMarkdown(report)
	for _, want := range []string{"Profile: `fast`", "Checks: `test`", "| `empty` | — |", "specific output"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q: %s", want, text)
		}
	}
	if err := PersistVerification(report, t.TempDir(), "verify"); err != nil {
		t.Fatal(err)
	}
	if err := PersistVerification(report, "", "verify"); err != nil {
		t.Fatal(err)
	}
}
func TestPersistChangedCarriesToleranceAndOmitsUnusedTolerance(t *testing.T) {
	t.Parallel()
	report := ChangedReport{Target: "main", MergeBase: "base", Packages: []quality.PackageRatchet{{Package: "pkg", Pass: true, Tolerance: 2, Tolerated: []quality.ToleratedStatement{{File: "pkg/file.go", Line: 42, Function: "wait", Reason: "timing"}}}, {Package: "other", Pass: true}}}
	dir := t.TempDir()
	if err := PersistChanged(report, dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "coverage-ratchet.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded ChangedReport
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	got := decoded.Packages[0]
	if got.Tolerance != 2 || len(got.Tolerated) != 1 || got.Tolerated[0] != report.Packages[0].Tolerated[0] {
		t.Fatal(got)
	}
	var rows struct{ Packages []map[string]any }
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if _, ok := rows.Packages[1]["tolerance"]; ok {
		t.Fatal("unused tolerance serialized")
	}
	if _, ok := rows.Packages[1]["tolerated"]; ok {
		t.Fatal("unused tolerated serialized")
	}
	if got := changedCoverageRatchetFindings(report.Packages); got != "" {
		t.Fatal(got)
	}
}

func TestQualityMarkdownIncludesTotalsAndCommands(t *testing.T) {
	coverage := quality.NewCoverageReport([]quality.RepositoryCoverage{{Repository: "acme/repo", Status: quality.StatusPassed, Statements: 4, Covered: 3, Percentage: 75}})
	if markdown := CoverageMarkdown(coverage); !strings.Contains(markdown, "Fleet total:** 75.00%") {
		t.Fatalf("coverage markdown = %s", markdown)
	}
	verification := VerificationIndex{Checks: []quality.Check{quality.CheckTest}, Repositories: []quality.VerificationReport{{Repository: "acme/repo", Status: quality.StatusPassed, Results: []quality.VerificationEntry{{Language: "go", Module: ".", Check: quality.CheckTest, Command: "go test ./...", Status: quality.StatusPassed}}}}}
	if markdown := VerificationMarkdown(verification); !strings.Contains(markdown, "go test ./...") {
		t.Fatalf("verification markdown = %s", markdown)
	}
}
