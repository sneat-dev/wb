package quality

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

const coverageSummarySchemaVersion = 1

// CoverageSummary is the deterministic JSON artifact published by CI coverage runs.
type CoverageSummary struct {
	SchemaVersion  int                       `json:"schema_version"`
	Repository     string                    `json:"repository,omitempty"`
	SHA            string                    `json:"sha,omitempty"`
	Ref            string                    `json:"ref,omitempty"`
	WorkflowRunID  int64                     `json:"workflow_run_id,omitempty"`
	WorkflowRunURL string                    `json:"workflow_run_url,omitempty"`
	ReportedAt     time.Time                 `json:"reported_at"`
	Status         Status                    `json:"status"`
	Statements     int                       `json:"statements"`
	Covered        int                       `json:"covered"`
	Percentage     float64                   `json:"percentage"`
	Modules        []ModuleCoverageSummary   `json:"modules,omitempty"`
	Packages       map[string]PackageSummary `json:"packages,omitempty"`
}

// ModuleCoverageSummary records statements and coverage for a single Go module.
type ModuleCoverageSummary struct {
	Path       string  `json:"path"`
	Statements int     `json:"statements"`
	Covered    int     `json:"covered"`
	Percentage float64 `json:"percentage"`
}

// PackageSummary records statements and coverage for a single Go package.
type PackageSummary struct {
	Statements int     `json:"statements"`
	Covered    int     `json:"covered"`
	Percentage float64 `json:"percentage"`
}

// CoverageSummaryMeta holds build metadata injected into the summary.
type CoverageSummaryMeta struct {
	Repository     string
	SHA            string
	Ref            string
	WorkflowRunID  int64
	WorkflowRunURL string
	ReportedAt     time.Time
	Status         Status
}

// SummaryFromProfile builds a CoverageSummary from parsed coverage blocks.
func SummaryFromProfile(blocks []CoverageBlock, modulePath string, meta CoverageSummaryMeta) CoverageSummary {
	packages := make(map[string]*PackageSummary)
	totalStatements := 0
	totalCovered := 0

	for _, block := range blocks {
		totalStatements += block.Statements
		if block.Count > 0 {
			totalCovered += block.Statements
		}

		pkg := PackageOf(block.File, modulePath)
		entry, exists := packages[pkg]
		if !exists {
			entry = &PackageSummary{}
			packages[pkg] = entry
		}
		entry.Statements += block.Statements
		if block.Count > 0 {
			entry.Covered += block.Statements
		}
	}

	pkgMap := make(map[string]PackageSummary, len(packages))
	for pkg, entry := range packages {
		entry.Percentage = percent(entry.Covered, entry.Statements)
		pkgMap[pkg] = *entry
	}

	reportedAt := meta.ReportedAt
	if reportedAt.IsZero() {
		reportedAt = time.Now().UTC()
	}
	status := meta.Status
	if status == "" {
		status = StatusPassed
	}

	moduleSummary := ModuleCoverageSummary{
		Path:       ".",
		Statements: totalStatements,
		Covered:    totalCovered,
		Percentage: percent(totalCovered, totalStatements),
	}

	return CoverageSummary{
		SchemaVersion:  coverageSummarySchemaVersion,
		Repository:     meta.Repository,
		SHA:            meta.SHA,
		Ref:            meta.Ref,
		WorkflowRunID:  meta.WorkflowRunID,
		WorkflowRunURL: meta.WorkflowRunURL,
		ReportedAt:     reportedAt,
		Status:         status,
		Statements:     totalStatements,
		Covered:        totalCovered,
		Percentage:     percent(totalCovered, totalStatements),
		Modules:        []ModuleCoverageSummary{moduleSummary},
		Packages:       pkgMap,
	}
}

// WriteCoverageSummary writes summary as deterministic, indented JSON.
func WriteCoverageSummary(path string, summary CoverageSummary) error {
	encoded, _ := json.MarshalIndent(summary, "", "  ")
	encoded = append(encoded, '\n')
	return os.WriteFile(path, encoded, 0o644)
}

// ReadCoverageSummary reads a CoverageSummary JSON file.
func ReadCoverageSummary(path string) (CoverageSummary, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return CoverageSummary{}, err
	}
	var summary CoverageSummary
	if err := json.Unmarshal(contents, &summary); err != nil {
		return CoverageSummary{}, fmt.Errorf("parse coverage summary %s: %w", path, err)
	}
	return summary, nil
}
