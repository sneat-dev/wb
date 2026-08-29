// Package quality runs read-only coverage and verification checks for a local
// repository fleet. It deliberately reports every selected repository instead
// of stopping at the first failure, so people and AI agents can act on one
// complete index.
package quality

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// CoverageReport is a deterministic, machine-readable coverage index.
type CoverageReport struct {
	SchemaVersion int                  `yaml:"schema_version" json:"schema_version"`
	Repositories  []RepositoryCoverage `yaml:"repositories" json:"repositories"`
	Statements    int                  `yaml:"statements" json:"statements"`
	Covered       int                  `yaml:"covered" json:"covered"`
	Percentage    float64              `yaml:"percentage" json:"percentage"`
}

// RepositoryCoverage records aggregate Go coverage for one repository.
type RepositoryCoverage struct {
	Repository string              `yaml:"repository" json:"repository"`
	Path       string              `yaml:"path" json:"path"`
	Status     Status              `yaml:"status" json:"status"`
	Modules    []ModuleCoverage    `yaml:"modules,omitempty" json:"modules,omitempty"`
	Statements int                 `yaml:"statements" json:"statements"`
	Covered    int                 `yaml:"covered" json:"covered"`
	Percentage float64             `yaml:"percentage" json:"percentage"`
	Error      string              `yaml:"error,omitempty" json:"error,omitempty"`
	Diagnostic *CoverageDiagnostic `yaml:"diagnostic,omitempty" json:"diagnostic,omitempty"`
}

// CoverageDiagnostic points at the private manifest containing lossless raw
// output for failed coverage jobs.
type CoverageDiagnostic struct {
	Manifest string `yaml:"manifest" json:"manifest"`
	SHA256   string `yaml:"sha256" json:"sha256"`
}

// ModuleCoverage records the statement totals from one Go module's generated
// coverage profile.
type ModuleCoverage struct {
	Path       string  `yaml:"path" json:"path"`
	Statements int     `yaml:"statements" json:"statements"`
	Covered    int     `yaml:"covered" json:"covered"`
	Percentage float64 `yaml:"percentage" json:"percentage"`
	Attempts   int     `yaml:"attempts,omitempty" json:"attempts,omitempty"`
}

// Status is the outcome of a repository or a discrete verification command.
type Status string

const (
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
)

// Cover measures all Go modules below path. It creates profiles in the system
// temporary directory, never in the repository.
func Cover(ctx context.Context, repository, path string) RepositoryCoverage {
	return CoverWithOptions(ctx, repository, path, RunOptions{})
}

// CoverWithOptions measures coverage with a deadline and retries for each Go
// module's test command.
func CoverWithOptions(ctx context.Context, repository, path string, options RunOptions) RepositoryCoverage {
	if options.CoverageDiagnosticsRepository == "" {
		options.CoverageDiagnosticsRepository = repository
	}
	report := RepositoryCoverage{Repository: repository, Path: path}
	modules, err := goModules(path)
	if err != nil {
		report.Status = StatusFailed
		report.Error = err.Error()
		return report
	}
	if len(modules) == 0 {
		report.Status = StatusSkipped
		return report
	}
	if options.CoverageProfile != "" && len(modules) != 1 {
		report.Status = StatusFailed
		report.Error = "--coverage-profile requires exactly one Go module"
		return report
	}
	report.Status = StatusPassed
	for _, module := range modules {
		profilePath, removeProfile, err := coverageProfilePath(options.CoverageProfile)
		if err != nil {
			report.Status = StatusFailed
			report.Error = err.Error()
			return report
		}
		command := coverageCommandDescription(options)
		reportQualityProgress(options, Progress{
			Language: "go", Module: relativePath(path, module), Check: CheckTest,
			Command: command, State: ProgressStarted,
		})
		output, attempts, err := runCoverageWithOptions(ctx, options, module, profilePath)
		if err != nil {
			if removeProfile {
				_ = os.Remove(profilePath)
			}
			report.Status = StatusFailed
			report.Error = commandError(command, output, err)
			if options.CoverageDiagnosticsDir != "" {
				report.Diagnostic = coverageDiagnosticFor(options.CoverageDiagnosticsDir, repository, module)
			}
			reportQualityProgress(options, Progress{
				Language: "go", Module: relativePath(path, module), Check: CheckTest,
				Command: command, State: ProgressCompleted, Status: StatusFailed, Attempts: attempts,
			})
			return report
		}
		statements, covered, err := profileTotals(profilePath)
		if removeProfile {
			_ = os.Remove(profilePath)
		}
		if err != nil {
			report.Status = StatusFailed
			report.Error = err.Error()
			return report
		}
		moduleReport := ModuleCoverage{
			Path:       relativePath(path, module),
			Statements: statements,
			Covered:    covered,
			Percentage: percent(covered, statements),
			Attempts:   attempts,
		}
		report.Modules = append(report.Modules, moduleReport)
		report.Statements += statements
		report.Covered += covered
		reportQualityProgress(options, Progress{
			Language: "go", Module: relativePath(path, module), Check: CheckTest,
			Command: command, State: ProgressCompleted, Status: StatusPassed, Attempts: attempts,
		})
	}
	report.Percentage = percent(report.Covered, report.Statements)
	return report
}

func coverageProfilePath(retain string) (path string, remove bool, err error) {
	if retain != "" {
		absolute, err := filepath.Abs(retain)
		if err != nil {
			return "", false, err
		}
		return absolute, false, nil
	}
	profile, err := os.CreateTemp("", "wb-coverage-*.out")
	if err != nil {
		return "", false, err
	}
	path = profile.Name()
	if err := profile.Close(); err != nil {
		_ = os.Remove(path)
		return "", false, err
	}
	return path, true, nil
}

func coverageCommandDescription(options RunOptions) string {
	if options.GoTestShards > 1 {
		return fmt.Sprintf("go test -coverprofile … ./... (%d process-isolated shards for %s)", options.GoTestShards, strings.Join(options.GoShardPackages, ","))
	}
	return "go test -coverprofile … ./..."
}

// NewCoverageReport aggregates reports in deterministic repository order.
func NewCoverageReport(repositories []RepositoryCoverage) CoverageReport {
	report := CoverageReport{SchemaVersion: 1, Repositories: append([]RepositoryCoverage(nil), repositories...)}
	sort.Slice(report.Repositories, func(i, j int) bool {
		return report.Repositories[i].Repository < report.Repositories[j].Repository
	})
	for _, repository := range report.Repositories {
		report.Statements += repository.Statements
		report.Covered += repository.Covered
	}
	report.Percentage = percent(report.Covered, report.Statements)
	return report
}

func goModules(root string) ([]string, error) {
	var modules []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == "go.mod" {
			modules = append(modules, filepath.Dir(path))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(modules)
	return modules, nil
}

func profileTotals(path string) (statements, covered int, err error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	for lineNumber, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
		if lineNumber == 0 && strings.HasPrefix(line, "mode: ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return 0, 0, fmt.Errorf("invalid coverage profile %s at line %d", path, lineNumber+1)
		}
		count, countErr := strconv.ParseInt(fields[2], 10, 64)
		statementCount, statementErr := strconv.Atoi(fields[1])
		if countErr != nil || statementErr != nil {
			return 0, 0, fmt.Errorf("invalid coverage profile %s at line %d", path, lineNumber+1)
		}
		statements += statementCount
		if count > 0 {
			covered += statementCount
		}
	}
	return statements, covered, nil
}

func percent(covered, statements int) float64 {
	if statements == 0 {
		return 0
	}
	return float64(covered) * 100 / float64(statements)
}

func relativePath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." {
		return "."
	}
	return filepath.ToSlash(relative)
}
