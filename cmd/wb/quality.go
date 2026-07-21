package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/quality"
)

type qualityOptions struct {
	fleet     bool
	match     string
	regex     string
	parallel  int
	format    string
	reportDir string
	checks    string
}

type qualityTarget struct {
	repository string
	path       string
}

func newCoverageCmd() *cobra.Command {
	options := qualityOptions{}
	command := &cobra.Command{
		Use:   "coverage [repository-path]",
		Short: "Measure Go test coverage for one repository or the local fleet",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			if options.fleet && len(args) > 0 {
				return fmt.Errorf("repository-path cannot be used with --fleet")
			}
			targets, err := qualityTargets(path, projectsRoot, filterFlag, options)
			if err != nil {
				return err
			}
			reports := runCoverageTargets(targets, options.parallel)
			report := quality.NewCoverageReport(reports)
			if err := writeCoverageOutput(report, options.format, options.reportDir); err != nil {
				return err
			}
			if coverageFailed(report) {
				return &exitError{code: 1}
			}
			return nil
		},
	}
	bindQualityScopeFlags(command, &options)
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write coverage.md and coverage.yaml to this directory")
	return command
}

func newVerifyCmd() *cobra.Command {
	options := qualityOptions{}
	command := &cobra.Command{
		Use:   "verify [repository-path]",
		Short: "Run conventional lint, test, and build checks across local repositories",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			if options.fleet && len(args) > 0 {
				return fmt.Errorf("repository-path cannot be used with --fleet")
			}
			checks, err := quality.ParseChecks(options.checks)
			if err != nil {
				return err
			}
			targets, err := qualityTargets(path, projectsRoot, filterFlag, options)
			if err != nil {
				return err
			}
			reports := runVerificationTargets(targets, checks, options.parallel)
			quality.SortVerificationReports(reports)
			report := verificationIndex{SchemaVersion: 1, Checks: checks, Repositories: reports}
			if err := writeVerificationOutput(report, options.format, options.reportDir); err != nil {
				return err
			}
			if verificationFailed(report) {
				return &exitError{code: 1}
			}
			return nil
		},
	}
	bindQualityScopeFlags(command, &options)
	command.Flags().StringVar(&options.checks, "checks", "", "comma-separated checks: lint,test,build (default all)")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write verify.md and verify.yaml to this directory")
	return command
}

func bindQualityScopeFlags(command *cobra.Command, options *qualityOptions) {
	command.Flags().BoolVar(&options.fleet, "fleet", false, "process every local repository under --projects-root")
	command.Flags().StringVar(&options.match, "match", "", "glob matched against org/repo, e.g. sneat-co/*")
	command.Flags().StringVar(&options.regex, "regex", "", "regular expression matched against org/repo")
	command.Flags().IntVar(&options.parallel, "parallel", 1, "maximum repositories to process concurrently")
}

func qualityTargets(singlePath, root, filter string, options qualityOptions) ([]qualityTarget, error) {
	if options.parallel < 1 {
		return nil, fmt.Errorf("parallelism must be at least 1")
	}
	var expression *regexp.Regexp
	if options.regex != "" {
		compiled, err := regexp.Compile(options.regex)
		if err != nil {
			return nil, fmt.Errorf("invalid --regex: %w", err)
		}
		expression = compiled
	}
	if options.match != "" {
		if _, err := path.Match(options.match, ""); err != nil {
			return nil, fmt.Errorf("invalid --match: %w", err)
		}
	}
	if !options.fleet {
		absolute, err := filepath.Abs(singlePath)
		if err != nil {
			return nil, err
		}
		target := qualityTarget{repository: filepath.Base(absolute), path: absolute}
		if !matchesQualityTarget(target.repository, filter, options.match, expression) {
			return nil, fmt.Errorf("repository %s does not match the selected filters", target.repository)
		}
		return []qualityTarget{target}, nil
	}
	repositories, err := discover.ScanLocal(root)
	if err != nil {
		return nil, err
	}
	targets := make([]qualityTarget, 0, len(repositories))
	for _, repository := range repositories {
		if !matchesQualityTarget(repository.Slug(), filter, options.match, expression) {
			continue
		}
		targets = append(targets, qualityTarget{repository: repository.Slug(), path: repository.Path})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].repository < targets[j].repository })
	if len(targets) == 0 {
		return nil, fmt.Errorf("no local repositories match the selected filters")
	}
	return targets, nil
}

func matchesQualityTarget(repository, filter, glob string, expression *regexp.Regexp) bool {
	if filter != "" && !strings.Contains(repository, filter) {
		return false
	}
	if glob != "" {
		matched, err := path.Match(glob, repository)
		if err != nil || !matched {
			return false
		}
	}
	return expression == nil || expression.MatchString(repository)
}

func runCoverageTargets(targets []qualityTarget, parallel int) []quality.RepositoryCoverage {
	reports := make([]quality.RepositoryCoverage, len(targets))
	runTargets(len(targets), parallel, func(index int) {
		target := targets[index]
		reports[index] = quality.Cover(context.Background(), target.repository, target.path)
	})
	return reports
}

func runVerificationTargets(targets []qualityTarget, checks []quality.Check, parallel int) []quality.VerificationReport {
	reports := make([]quality.VerificationReport, len(targets))
	runTargets(len(targets), parallel, func(index int) {
		target := targets[index]
		reports[index] = quality.Verify(context.Background(), target.repository, target.path, checks)
	})
	return reports
}

func runTargets(count, parallel int, run func(int)) {
	if count == 0 {
		return
	}
	if parallel > count {
		parallel = count
	}
	jobs := make(chan int)
	var group sync.WaitGroup
	for range parallel {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				run(index)
			}
		}()
	}
	for index := range count {
		jobs <- index
	}
	close(jobs)
	group.Wait()
}

func coverageFailed(report quality.CoverageReport) bool {
	for _, repository := range report.Repositories {
		if repository.Status == quality.StatusFailed {
			return true
		}
	}
	return false
}

type verificationIndex struct {
	SchemaVersion int                          `yaml:"schema_version" json:"schema_version"`
	Checks        []quality.Check              `yaml:"checks" json:"checks"`
	Repositories  []quality.VerificationReport `yaml:"repositories" json:"repositories"`
}

func verificationFailed(report verificationIndex) bool {
	for _, repository := range report.Repositories {
		if repository.Status == quality.StatusFailed {
			return true
		}
	}
	return false
}

func writeCoverageOutput(report quality.CoverageReport, format, reportDir string) error {
	if reportDir != "" {
		if err := os.MkdirAll(reportDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(reportDir, "coverage.md"), []byte(coverageMarkdown(report)), 0o644); err != nil {
			return err
		}
		raw, err := yaml.Marshal(report)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(reportDir, "coverage.yaml"), raw, 0o644); err != nil {
			return err
		}
	}
	switch format {
	case "markdown":
		_, err := fmt.Print(coverageMarkdown(report))
		return err
	case "yaml":
		raw, err := yaml.Marshal(report)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(raw)
		return err
	case "json":
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}

func writeVerificationOutput(report verificationIndex, format, reportDir string) error {
	if reportDir != "" {
		if err := os.MkdirAll(reportDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(reportDir, "verify.md"), []byte(verificationMarkdown(report)), 0o644); err != nil {
			return err
		}
		raw, err := yaml.Marshal(report)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(reportDir, "verify.yaml"), raw, 0o644); err != nil {
			return err
		}
	}
	switch format {
	case "markdown":
		_, err := fmt.Print(verificationMarkdown(report))
		return err
	case "yaml":
		raw, err := yaml.Marshal(report)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(raw)
		return err
	case "json":
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}

func coverageMarkdown(report quality.CoverageReport) string {
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

func verificationMarkdown(report verificationIndex) string {
	var out strings.Builder
	out.WriteString("# WB verification\n\n")
	fmt.Fprintf(&out, "Checks: `%s`\n\n", strings.Join(checkNames(report.Checks), ","))
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

func checkNames(checks []quality.Check) []string {
	names := make([]string, len(checks))
	for index, check := range checks {
		names[index] = string(check)
	}
	return names
}
