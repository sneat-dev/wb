package qualityrun

import (
	"crypto/sha256"
	"fmt"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/reposelection"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type VerificationIndex struct {
	SchemaVersion int                          `yaml:"schema_version" json:"schema_version"`
	GeneratedAt   time.Time                    `yaml:"generated_at" json:"generated_at"`
	Profile       string                       `yaml:"profile,omitempty" json:"profile,omitempty"`
	Checks        []quality.Check              `yaml:"checks" json:"checks"`
	Repositories  []quality.VerificationReport `yaml:"repositories" json:"repositories"`
}
type ArtifactReference struct {
	Path   string
	SHA256 string
}
type coverageDiagnosticIndex struct {
	SchemaVersion int                            `yaml:"schema_version" json:"schema_version"`
	Repositories  []coverageDiagnosticIndexEntry `yaml:"repositories" json:"repositories"`
}
type coverageDiagnosticIndexEntry struct {
	Repository string `yaml:"repository" json:"repository"`
	Manifest   string `yaml:"manifest" json:"manifest"`
	SHA256     string `yaml:"sha256" json:"sha256"`
}

func CoverageMarkdown(report quality.CoverageReport) string {
	var out strings.Builder
	out.WriteString("# WB Go coverage\n\n")
	out.WriteString("| Repository | Status | Modules | Statements | Covered | Coverage |\n|---|---|---:|---:|---:|---:|\n")
	for _, repository := range report.Repositories {
		fmt.Fprintf(&out, "| `%s` | `%s` | %d | %d | %d | %.2f%% |\n", repository.Repository, repository.Status, len(repository.Modules), repository.Statements, repository.Covered, repository.Percentage)
		if repository.Error != "" {
			fmt.Fprintf(&out, "\n`%s`: %s\n\n", repository.Repository, repository.Error)
		}
	}
	fmt.Fprintf(&out, "\n**Fleet total:** %.2f%% (%d/%d statements)\n", report.Percentage, report.Covered, report.Statements)
	return out.String()
}

func VerificationMarkdown(report VerificationIndex) string {
	var out strings.Builder
	out.WriteString("# WB verification\n\n")
	if report.Profile != "" {
		fmt.Fprintf(&out, "Profile: `%s`\n\n", report.Profile)
	}
	fmt.Fprintf(&out, "Checks: `%s`\n\n", strings.Join(CheckNames(report.Checks), ","))
	out.WriteString("| Repository | Language | Module | Check | Status | Command |\n|---|---|---|---|---|---|\n")
	for _, repository := range report.Repositories {
		if len(repository.Results) == 0 {
			fmt.Fprintf(&out, "| `%s` | — | — | — | `%s` | — |\n", repository.Repository, repository.Status)
			continue
		}
		for _, result := range repository.Results {
			fmt.Fprintf(&out, "| `%s` | `%s` | `%s` | `%s` | `%s` | `%s` |\n", repository.Repository, result.Language, result.Module, result.Check, result.Status, result.Command)
			if result.Detail != "" {
				fmt.Fprintf(&out, "\n`%s` %s: %s\n\n", repository.Repository, result.Check, result.Detail)
			}
		}
	}
	return out.String()
}

func CheckNames(checks []quality.Check) []string {
	names := make([]string, len(checks))
	for index, check := range checks {
		names[index] = string(check)
	}
	return names
}

func writeCoverageDiagnosticsIndex(report quality.CoverageReport, reportDir string) (*ArtifactReference, error) {
	entries := make([]coverageDiagnosticIndexEntry, 0)
	for _, repository := range report.Repositories {
		if repository.Diagnostic == nil {
			continue
		}
		entries = append(entries, coverageDiagnosticIndexEntry{
			Repository: repository.Repository,
			Manifest:   repository.Diagnostic.Manifest,
			SHA256:     repository.Diagnostic.SHA256,
		})
	}
	if len(entries) == 0 {
		return nil, nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Repository < entries[j].Repository })
	// The private index contains only strings, an int and a concrete slice.
	raw, _ := yaml.Marshal(coverageDiagnosticIndex{SchemaVersion: 1, Repositories: entries})
	path := filepath.Join(reportDir, "coverage-diagnostics.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return &ArtifactReference{Path: path, SHA256: fmt.Sprintf("%x", digest)}, nil
}

func resumeCoverageTargets(targets []reposelection.Target, reportDir string) ([]reposelection.Target, quality.CoverageReport, error) {
	if reportDir == "" {
		return nil, quality.CoverageReport{}, fmt.Errorf("--resume requires --report-dir")
	}
	contents, err := os.ReadFile(filepath.Join(reportDir, "coverage.yaml"))
	if err != nil {
		return nil, quality.CoverageReport{}, fmt.Errorf("read coverage resume report: %w", err)
	}
	var previous quality.CoverageReport
	if err := yaml.Unmarshal(contents, &previous); err != nil {
		return nil, quality.CoverageReport{}, fmt.Errorf("parse coverage resume report: %w", err)
	}
	failed := map[string]bool{}
	for _, repository := range previous.Repositories {
		if repository.Status == quality.StatusFailed {
			failed[repository.Repository] = true
		}
	}
	return failedTargets(targets, failed), previous, nil
}

func resumeVerificationTargets(targets []reposelection.Target, reportDir, name string) ([]reposelection.Target, VerificationIndex, error) {
	if reportDir == "" {
		return nil, VerificationIndex{}, fmt.Errorf("--resume requires --report-dir")
	}
	contents, err := os.ReadFile(filepath.Join(reportDir, name+".yaml"))
	if err != nil {
		return nil, VerificationIndex{}, fmt.Errorf("read %s resume report: %w", name, err)
	}
	var previous VerificationIndex
	if err := yaml.Unmarshal(contents, &previous); err != nil {
		return nil, VerificationIndex{}, fmt.Errorf("parse %s resume report: %w", name, err)
	}
	failed := map[string]bool{}
	for _, repository := range previous.Repositories {
		if repository.Status == quality.StatusFailed {
			failed[repository.Repository] = true
		}
	}
	return failedTargets(targets, failed), previous, nil
}

func failedTargets(targets []reposelection.Target, failed map[string]bool) []reposelection.Target {
	resumed := make([]reposelection.Target, 0, len(targets))
	for _, target := range targets {
		if failed[target.Repository] {
			resumed = append(resumed, target)
		}
	}
	return resumed
}

func mergeCoverageReports(previous, current quality.CoverageReport) quality.CoverageReport {
	byRepository := map[string]quality.RepositoryCoverage{}
	for _, repository := range previous.Repositories {
		byRepository[repository.Repository] = repository
	}
	for _, repository := range current.Repositories {
		byRepository[repository.Repository] = repository
	}
	repositories := make([]quality.RepositoryCoverage, 0, len(byRepository))
	for _, repository := range byRepository {
		repositories = append(repositories, repository)
	}
	return quality.NewCoverageReport(repositories)
}

func mergeVerificationReports(previous, current VerificationIndex) VerificationIndex {
	byRepository := map[string]quality.VerificationReport{}
	for _, repository := range previous.Repositories {
		byRepository[repository.Repository] = repository
	}
	for _, repository := range current.Repositories {
		byRepository[repository.Repository] = repository
	}
	repositories := make([]quality.VerificationReport, 0, len(byRepository))
	for _, repository := range byRepository {
		repositories = append(repositories, repository)
	}
	quality.SortVerificationReports(repositories)
	current.Repositories = repositories
	return current
}

type CoverageArtifacts struct {
	Report      ArtifactReference
	Diagnostics *ArtifactReference
}

func PersistCoverage(report quality.CoverageReport, reportDir string) (CoverageArtifacts, error) {
	var durableReport []byte
	var durableReportPath string
	var diagnosticsIndex *ArtifactReference
	if reportDir != "" {
		if err := os.MkdirAll(reportDir, 0o755); err != nil {
			return CoverageArtifacts{}, err
		}
		if err := os.WriteFile(filepath.Join(reportDir, "coverage.md"), []byte(CoverageMarkdown(report)), 0o644); err != nil {
			return CoverageArtifacts{}, err
		}
		// The concrete report graph contains scalars, slices and time.Time only;
		// none of its domain types implements MarshalYAML. Encoding cannot fail.
		raw, _ := yaml.Marshal(report)
		durableReport = raw
		durableReportPath = filepath.Join(reportDir, "coverage.yaml")
		if err := os.WriteFile(durableReportPath, durableReport, 0o644); err != nil {
			return CoverageArtifacts{}, err
		}
		var err error
		diagnosticsIndex, err = writeCoverageDiagnosticsIndex(report, reportDir)
		if err != nil {
			return CoverageArtifacts{}, err
		}
	}
	artifacts := CoverageArtifacts{Diagnostics: diagnosticsIndex}
	if durableReportPath != "" {
		digest := sha256.Sum256(durableReport)
		artifacts.Report = ArtifactReference{Path: durableReportPath, SHA256: fmt.Sprintf("%x", digest)}
	}
	return artifacts, nil
}
func PersistVerification(report VerificationIndex, reportDir, name string) error {
	if reportDir != "" {
		if err := os.MkdirAll(reportDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(reportDir, name+".md"), []byte(VerificationMarkdown(report)), 0o644); err != nil {
			return err
		}
		// The concrete report graph contains scalars, slices and time.Time only;
		// none of its domain types implements MarshalYAML. Encoding cannot fail.
		raw, _ := yaml.Marshal(report)
		if err := os.WriteFile(filepath.Join(reportDir, name+".yaml"), raw, 0o644); err != nil {
			return err
		}
	}
	return nil
}
