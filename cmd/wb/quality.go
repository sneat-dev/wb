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
	"time"

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
	timeout   time.Duration
	retry     int
	resume    bool
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
			var previous quality.CoverageReport
			if options.resume {
				targets, previous, err = resumeCoverageTargets(targets, options.reportDir)
				if err != nil {
					return err
				}
				if len(targets) == 0 {
					fmt.Println("No failed repositories to resume.")
					return nil
				}
			}
			reports := runCoverageTargets(targets, options.parallel, runOptions(options))
			report := quality.NewCoverageReport(reports)
			if options.resume {
				report = mergeCoverageReports(previous, report)
			}
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
			var previous verificationIndex
			if options.resume {
				targets, previous, err = resumeVerificationTargets(targets, options.reportDir, "verify")
				if err != nil {
					return err
				}
				if len(targets) == 0 {
					fmt.Println("No failed repositories to resume.")
					return nil
				}
			}
			reports := runVerificationTargets(targets, checks, options.parallel, runOptions(options))
			quality.SortVerificationReports(reports)
			report := verificationIndex{SchemaVersion: 1, Checks: checks, Repositories: reports}
			if options.resume {
				report = mergeVerificationReports(previous, report)
			}
			if err := writeVerificationOutput(report, options.format, options.reportDir, "verify"); err != nil {
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

func newCheckCmd() *cobra.Command {
	options := qualityOptions{}
	var profile string
	command := &cobra.Command{
		Use:   "check [repository-path]",
		Short: "Run a named local CI-equivalent verification profile",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			if options.fleet && len(args) > 0 {
				return fmt.Errorf("repository-path cannot be used with --fleet")
			}
			checks, err := checksForProfile(profile)
			if err != nil {
				return err
			}
			targets, err := qualityTargets(path, projectsRoot, filterFlag, options)
			if err != nil {
				return err
			}
			var previous verificationIndex
			if options.resume {
				targets, previous, err = resumeVerificationTargets(targets, options.reportDir, "check")
				if err != nil {
					return err
				}
				if len(targets) == 0 {
					fmt.Println("No failed repositories to resume.")
					return nil
				}
			}
			reports := runVerificationTargets(targets, checks, options.parallel, runOptions(options))
			quality.SortVerificationReports(reports)
			report := verificationIndex{SchemaVersion: 1, Profile: profile, Checks: checks, Repositories: reports}
			if options.resume {
				report = mergeVerificationReports(previous, report)
			}
			if err := writeVerificationOutput(report, options.format, options.reportDir, "check"); err != nil {
				return err
			}
			if verificationFailed(report) {
				return &exitError{code: 1}
			}
			return nil
		},
	}
	bindQualityScopeFlags(command, &options)
	command.Flags().StringVar(&profile, "profile", "full", "built-in profile: fast, full, or ci")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write check.md and check.yaml to this directory")
	return command
}

func bindQualityScopeFlags(command *cobra.Command, options *qualityOptions) {
	command.Flags().BoolVar(&options.fleet, "fleet", false, "process every local repository under --projects-root")
	command.Flags().StringVar(&options.match, "match", "", "glob matched against org/repo, e.g. sneat-co/*")
	command.Flags().StringVar(&options.regex, "regex", "", "regular expression matched against org/repo")
	command.Flags().IntVar(&options.parallel, "parallel", 1, "maximum repositories to process concurrently")
	command.Flags().DurationVar(&options.timeout, "timeout", 10*time.Minute, "maximum duration per external check (0 disables)")
	command.Flags().IntVar(&options.retry, "retry", 0, "additional attempts for each failed external check")
	command.Flags().BoolVar(&options.resume, "resume", false, "rerun only repositories that failed in the report directory")
}

func qualityTargets(singlePath, root, filter string, options qualityOptions) ([]qualityTarget, error) {
	if options.parallel < 1 {
		return nil, fmt.Errorf("parallelism must be at least 1")
	}
	if options.retry < 0 {
		return nil, fmt.Errorf("retry count must not be negative")
	}
	if options.timeout < 0 {
		return nil, fmt.Errorf("timeout must not be negative")
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

func runCoverageTargets(targets []qualityTarget, parallel int, options quality.RunOptions) []quality.RepositoryCoverage {
	reports := make([]quality.RepositoryCoverage, len(targets))
	runTargets(len(targets), parallel, func(index int) {
		target := targets[index]
		reports[index] = quality.CoverWithOptions(context.Background(), target.repository, target.path, options)
	})
	return reports
}

func runVerificationTargets(targets []qualityTarget, checks []quality.Check, parallel int, options quality.RunOptions) []quality.VerificationReport {
	reports := make([]quality.VerificationReport, len(targets))
	runTargets(len(targets), parallel, func(index int) {
		target := targets[index]
		reports[index] = quality.VerifyWithOptions(context.Background(), target.repository, target.path, checks, options)
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
	Profile       string                       `yaml:"profile,omitempty" json:"profile,omitempty"`
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

func writeVerificationOutput(report verificationIndex, format, reportDir, name string) error {
	if reportDir != "" {
		if err := os.MkdirAll(reportDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(reportDir, name+".md"), []byte(verificationMarkdown(report)), 0o644); err != nil {
			return err
		}
		raw, err := yaml.Marshal(report)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(reportDir, name+".yaml"), raw, 0o644); err != nil {
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
	if report.Profile != "" {
		fmt.Fprintf(&out, "Profile: `%s`\n\n", report.Profile)
	}
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

func runOptions(options qualityOptions) quality.RunOptions {
	return quality.RunOptions{Timeout: options.timeout, Retry: options.retry}
}

func checksForProfile(profile string) ([]quality.Check, error) {
	switch profile {
	case "fast":
		return []quality.Check{quality.CheckLint}, nil
	case "full":
		return []quality.Check{quality.CheckLint, quality.CheckTest, quality.CheckBuild}, nil
	case "ci":
		return []quality.Check{quality.CheckLint, quality.CheckTest, quality.CheckBuild, quality.CheckSpec}, nil
	default:
		return nil, fmt.Errorf("unknown check profile %q (want fast, full, or ci)", profile)
	}
}

func resumeCoverageTargets(targets []qualityTarget, reportDir string) ([]qualityTarget, quality.CoverageReport, error) {
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

func resumeVerificationTargets(targets []qualityTarget, reportDir, name string) ([]qualityTarget, verificationIndex, error) {
	if reportDir == "" {
		return nil, verificationIndex{}, fmt.Errorf("--resume requires --report-dir")
	}
	contents, err := os.ReadFile(filepath.Join(reportDir, name+".yaml"))
	if err != nil {
		return nil, verificationIndex{}, fmt.Errorf("read %s resume report: %w", name, err)
	}
	var previous verificationIndex
	if err := yaml.Unmarshal(contents, &previous); err != nil {
		return nil, verificationIndex{}, fmt.Errorf("parse %s resume report: %w", name, err)
	}
	failed := map[string]bool{}
	for _, repository := range previous.Repositories {
		if repository.Status == quality.StatusFailed {
			failed[repository.Repository] = true
		}
	}
	return failedTargets(targets, failed), previous, nil
}

func failedTargets(targets []qualityTarget, failed map[string]bool) []qualityTarget {
	resumed := make([]qualityTarget, 0, len(targets))
	for _, target := range targets {
		if failed[target.repository] {
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

func mergeVerificationReports(previous, current verificationIndex) verificationIndex {
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
