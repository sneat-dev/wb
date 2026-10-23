package ciaudit

import (
	"github.com/sneat-dev/wb/internal/discover"
	"path/filepath"
	"sort"
	"strings"
)

// BatchOptions selects repositories; command output and exit policy stay above this service.
type BatchOptions struct {
	Path, ProjectsRoot, Filter, Target string
	Fleet                              bool
}
type batchDependencies struct {
	abs     func(string) (string, error)
	scan    func(string) ([]discover.Repo, error)
	audit   func(string) (Report, error)
	compare func(string, string) ([]Finding, error)
}

func defaultBatchDependencies() batchDependencies {
	return batchDependencies{filepath.Abs, discover.ScanLocal, Audit, CompareAgainstTarget}
}
func AuditBatch(options BatchOptions) ([]Report, error) {
	return auditBatchWithDeps(options, defaultBatchDependencies())
}
func auditBatchWithDeps(options BatchOptions, deps batchDependencies) ([]Report, error) {
	paths := []string{options.Path}
	if options.Fleet {
		repos, err := deps.scan(options.ProjectsRoot)
		if err != nil {
			return nil, err
		}
		paths = paths[:0]
		for _, repo := range repos {
			if options.Filter != "" && !strings.Contains(repo.Slug(), options.Filter) {
				continue
			}
			paths = append(paths, repo.Path)
		}
	}
	return auditReportsWithDeps(paths, options.Target, deps)
}
func auditReportsWithDeps(paths []string, target string, deps batchDependencies) ([]Report, error) {
	reports := make([]Report, 0, len(paths))
	for _, repoPath := range paths {
		absolute, err := deps.abs(repoPath)
		if err != nil {
			return nil, err
		}
		report, err := deps.audit(absolute)
		if err != nil {
			return nil, err
		}
		if target != "" {
			targetFindings, err := deps.compare(absolute, target)
			if err != nil {
				return nil, err
			}
			report.Findings = append(report.Findings, targetFindings...)
			sort.Slice(report.Findings, func(i, j int) bool {
				if report.Findings[i].Code == report.Findings[j].Code {
					return report.Findings[i].File < report.Findings[j].File
				}
				return report.Findings[i].Code < report.Findings[j].Code
			})
		}
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].Path < reports[j].Path })
	return reports, nil
}
