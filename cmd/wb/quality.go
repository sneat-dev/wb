package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/quality"
)

type qualityOptions struct {
	fleet           bool
	match           string
	regex           string
	parallel        int
	format          string
	reportDir       string
	checks          string
	timeout         time.Duration
	retry           int
	resume          bool
	testShards      int
	shardPackages   []string
	coverageProfile string
	minimumCoverage float64
	// allowEmpty lets fleet mode return zero targets instead of erroring.
	// an empty fleet with no filter publishes an empty-but-valid snapshot;
	// an unmatched filter is still an error. Quality commands (coverage/verify/check/fleet)
	// want the error: an empty match is almost always a typo'd --filter.
	allowEmpty bool
}

type qualityTarget struct {
	repository string
	path       string
}

func newCoverageCmd() *cobra.Command {
	options := qualityOptions{testShards: 1, minimumCoverage: -1}
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
			if err := validateCoverageExecutionOptions(options); err != nil {
				return err
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
					fmt.Fprintln(os.Stderr, "no failed repositories to resume; nothing to do")
					return nil
				}
			}
			progress := newQualityProgress(cmd.ErrOrStderr(), console.Interactive(cmd.ErrOrStderr(), nonInteractive), "coverage", len(targets))
			progress.start()
			runOptions := runOptions(options)
			runOptions.GoTestShards = options.testShards
			runOptions.GoShardPackages = append([]string(nil), options.shardPackages...)
			runOptions.CoverageProfile = options.coverageProfile
			runOptions.Progress = progress.report
			reports := runCoverageTargets(targets, options.parallel, runOptions)
			progress.finish()
			report := quality.NewCoverageReport(reports)
			if options.resume {
				report = mergeCoverageReports(previous, report)
			}
			if err := writeCoverageOutput(report, options.format, options.reportDir); err != nil {
				return err
			}
			if err := coverageGateError(report, options.minimumCoverage); err != nil {
				return err
			}
			return nil
		},
	}
	bindQualityScopeFlags(command, &options)
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, json, or summary (summary requires --report-dir)")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write coverage.md and coverage.yaml to this directory")
	command.Flags().IntVar(&options.testShards, "test-shards", 1, "process-isolated shards for every explicit --shard-package")
	command.Flags().StringArrayVar(&options.shardPackages, "shard-package", nil, "single Go package safe to shard by top-level test name (repeatable)")
	command.Flags().StringVar(&options.coverageProfile, "coverage-profile", "", "retain the exact merged profile (single repository and Go module only)")
	command.Flags().Float64Var(&options.minimumCoverage, "minimum", -1, "minimum aggregate statement coverage percentage; disabled when omitted")
	return command
}

func validateCoverageExecutionOptions(options qualityOptions) error {
	if options.testShards < 1 {
		return fmt.Errorf("--test-shards must be at least 1")
	}
	if options.testShards > 1 && len(options.shardPackages) == 0 {
		return fmt.Errorf("--test-shards greater than 1 requires at least one --shard-package")
	}
	if options.testShards == 1 && len(options.shardPackages) > 0 {
		return fmt.Errorf("--shard-package requires --test-shards greater than 1")
	}
	if options.minimumCoverage < -1 || options.minimumCoverage > 100 {
		return fmt.Errorf("--minimum must be between 0 and 100 when provided")
	}
	if options.coverageProfile != "" && (options.fleet || options.resume) {
		return fmt.Errorf("--coverage-profile requires one fresh repository run; it cannot be combined with --fleet or --resume")
	}
	if len(options.shardPackages) > 0 && options.fleet {
		return fmt.Errorf("--shard-package is repository-specific and cannot be combined with --fleet")
	}
	return nil
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
					fmt.Fprintln(os.Stderr, "no failed repositories to resume; nothing to do")
					return nil
				}
			}
			progress := newQualityProgress(cmd.ErrOrStderr(), console.Interactive(cmd.ErrOrStderr(), nonInteractive), "verify", len(targets))
			progress.start()
			runOptions := runOptions(options)
			runOptions.Progress = progress.report
			reports := runVerificationTargets(targets, checks, options.parallel, runOptions)
			progress.finish()
			quality.SortVerificationReports(reports)
			report := verificationIndex{SchemaVersion: 1, GeneratedAt: time.Now().UTC(), Checks: checks, Repositories: reports}
			if options.resume {
				report = mergeVerificationReports(previous, report)
			}
			if err := writeVerificationOutput(report, options.format, options.reportDir, "verify"); err != nil {
				return err
			}
			if verificationFailed(report) {
				return &exitError{
					code:    exitFindings,
					message: "verification failed in one or more repositories; the failing check and its output are in the `detail` of each `failed` row above",
				}
			}
			return nil
		},
	}
	bindQualityScopeFlags(command, &options)
	command.Flags().StringVar(&options.checks, "checks", "", "comma-separated checks: lint,test,build (default all)")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write verify.md and verify.yaml to this directory")
	command.AddCommand(newVerifyReceiptCmd())
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
					fmt.Fprintln(os.Stderr, "no failed repositories to resume; nothing to do")
					return nil
				}
			}
			progress := newQualityProgress(cmd.ErrOrStderr(), console.Interactive(cmd.ErrOrStderr(), nonInteractive), "check", len(targets))
			progress.start()
			runOptions := runOptions(options)
			runOptions.Progress = progress.report
			reports := runVerificationTargets(targets, checks, options.parallel, runOptions)
			progress.finish()
			quality.SortVerificationReports(reports)
			report := verificationIndex{SchemaVersion: 1, GeneratedAt: time.Now().UTC(), Profile: profile, Checks: checks, Repositories: reports}
			if options.resume {
				report = mergeVerificationReports(previous, report)
			}
			if err := writeVerificationOutput(report, options.format, options.reportDir, "check"); err != nil {
				return err
			}
			if verificationFailed(report) {
				return &exitError{
					code:    exitFindings,
					message: "profile checks failed in one or more repositories; the failing check and its output are in the `detail` of each `failed` row above",
				}
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
	command.Flags().StringVar(&options.match, "match", "", "fleet-only glob matched against org/repo, e.g. sneat-co/*")
	command.Flags().StringVar(&options.regex, "regex", "", "fleet-only regular expression matched against org/repo")
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
		if filter != "" {
			return nil, fmt.Errorf("--filter requires fleet mode for owner/repository selection")
		}
		if options.match != "" || options.regex != "" {
			return nil, fmt.Errorf("--match and --regex require fleet mode because a direct repository path has no guaranteed owner/repository identity")
		}
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
	if len(targets) == 0 && !options.allowEmpty {
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
		targetOptions := qualityRunOptionsForTarget(options, target.repository)
		reports[index] = quality.CoverWithOptions(context.Background(), target.repository, target.path, targetOptions)
		reportQualityRepositoryCompleted(options, target.repository, reports[index].Status)
	})
	return reports
}

func runVerificationTargets(targets []qualityTarget, checks []quality.Check, parallel int, options quality.RunOptions) []quality.VerificationReport {
	reports := make([]quality.VerificationReport, len(targets))
	runTargets(len(targets), parallel, func(index int) {
		target := targets[index]
		targetOptions := qualityRunOptionsForTarget(options, target.repository)
		before := verificationGitSnapshot(target.path)
		report := quality.VerifyWithOptions(context.Background(), target.repository, target.path, checks, targetOptions)
		after := verificationGitSnapshot(target.path)
		if before.err == nil && after.err == nil && before.clean && after.clean && before.revision == after.revision {
			report.Revision = before.revision
			report.WorkspaceClean = true
		}
		reports[index] = report
		reportQualityRepositoryCompleted(options, target.repository, reports[index].Status)
	})
	return reports
}

type verificationGitState struct {
	revision string
	clean    bool
	err      error
}

func verificationGitSnapshot(repositoryPath string) verificationGitState {
	revisionOutput, err := exec.Command("git", "-C", repositoryPath, "rev-parse", "--verify", "HEAD").Output()
	if err != nil {
		return verificationGitState{err: err}
	}
	revision := strings.ToLower(strings.TrimSpace(string(revisionOutput)))
	if !exactGitObjectID.MatchString(revision) {
		return verificationGitState{err: fmt.Errorf("invalid Git revision %q", revision)}
	}
	statusOutput, err := exec.Command("git", "-C", repositoryPath, "status", "--porcelain=v1", "--untracked-files=all").Output()
	if err != nil {
		return verificationGitState{err: err}
	}
	return verificationGitState{revision: revision, clean: len(statusOutput) == 0}
}

func qualityRunOptionsForTarget(options quality.RunOptions, repository string) quality.RunOptions {
	options.CoverageDiagnosticsRepository = repository
	progress := options.Progress
	if progress == nil {
		return options
	}
	options.Progress = func(event quality.Progress) {
		event.Repository = repository
		progress(event)
	}
	return options
}

func reportQualityRepositoryCompleted(options quality.RunOptions, repository string, status quality.Status) {
	if options.Progress != nil {
		options.Progress(quality.Progress{Repository: repository, State: quality.ProgressRepositoryCompleted, Status: status})
	}
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

func coverageGateError(report quality.CoverageReport, minimum float64) error {
	if coverageFailed(report) {
		return &exitError{
			code:    exitFindings,
			message: "coverage could not be measured in one or more repositories; see the `error` column above, then rerun just those with --resume --report-dir",
		}
	}
	if minimum >= 0 && report.Percentage < minimum {
		return &exitError{
			code:    exitFindings,
			message: fmt.Sprintf("coverage %.2f%% is below required %.2f%%", report.Percentage, minimum),
		}
	}
	return nil
}

type verificationIndex struct {
	SchemaVersion int                          `yaml:"schema_version" json:"schema_version"`
	GeneratedAt   time.Time                    `yaml:"generated_at" json:"generated_at"`
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
	return writeCoverageOutputTo(os.Stdout, report, format, reportDir)
}

func writeCoverageOutputTo(out io.Writer, report quality.CoverageReport, format, reportDir string) error {
	var durableReport []byte
	var durableReportPath string
	var diagnosticsIndex *coverageDiagnosticArtifact
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
		durableReport = raw
		durableReportPath = filepath.Join(reportDir, "coverage.yaml")
		if err := os.WriteFile(durableReportPath, durableReport, 0o644); err != nil {
			return err
		}
		diagnosticsIndex, err = writeCoverageDiagnosticsIndex(report, reportDir)
		if err != nil {
			return err
		}
	}
	switch format {
	case "markdown":
		_, err := io.WriteString(out, coverageMarkdown(report))
		return err
	case "yaml":
		raw, err := yaml.Marshal(report)
		if err != nil {
			return err
		}
		_, err = out.Write(raw)
		return err
	case "json":
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	case "summary":
		if durableReportPath == "" || len(durableReport) == 0 {
			return fmt.Errorf("--format summary requires --report-dir so the full report has a durable reference")
		}
		digest := sha256.Sum256(durableReport)
		status := "passed"
		if coverageFailed(report) {
			status = "failed"
		}
		if _, err := fmt.Fprintf(out, "WB coverage %s: %.2f%% (%d/%d statements); report=%s; sha256=%x",
			status, report.Percentage, report.Covered, report.Statements, durableReportPath, digest); err != nil {
			return err
		}
		if diagnosticsIndex != nil {
			if _, err := fmt.Fprintf(out, "; diagnostics=%s; diagnostics-sha256=%s", diagnosticsIndex.Path, diagnosticsIndex.SHA256); err != nil {
				return err
			}
		}
		_, err := io.WriteString(out, "\n")
		return err
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, json, or summary)", format)
	}
}

type coverageDiagnosticArtifact struct {
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

func writeCoverageDiagnosticsIndex(report quality.CoverageReport, reportDir string) (*coverageDiagnosticArtifact, error) {
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
	raw, err := yaml.Marshal(coverageDiagnosticIndex{SchemaVersion: 1, Repositories: entries})
	if err != nil {
		return nil, err
	}
	path := filepath.Join(reportDir, "coverage-diagnostics.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return &coverageDiagnosticArtifact{Path: path, SHA256: fmt.Sprintf("%x", digest)}, nil
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
	return quality.RunOptions{Timeout: options.timeout, Retry: options.retry, CoverageDiagnosticsDir: options.reportDir}
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
