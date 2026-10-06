package cmdquality

import (
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/qualityrun"
	"github.com/spf13/cobra"
	"io"
	"strings"
)

func NewStoredCoverage(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var (
		format    string
		reportDir string
		match     string
		regex     string
	)
	command := &cobra.Command{
		Use:   "coverage",
		Short: "Inspect latest test coverage across the fleet without running tests",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			options := qualityOptions{
				fleet:     true,
				format:    format,
				reportDir: reportDir,
				match:     match,
				regex:     regex,
			}
			return runStored(cmd, "", options, runtime, deps)
		},
	}
	command.Flags().StringVar(&format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&reportDir, "report-dir", "", "write coverage.md and coverage.yaml to this directory")
	command.Flags().StringVar(&match, "match", "", "fleet glob matched against org/repo, e.g. sneat-co/*")
	command.Flags().StringVar(&regex, "regex", "", "fleet regular expression matched against org/repo")
	return command
}
func runStored(cmd *cobra.Command, path string, options qualityOptions, runtime shared.Runtime, deps Dependencies) error {
	result, err := deps.Stored(cmd.Context(), qualityrun.StoredRequest{Path: path, Fleet: options.fleet, Match: options.match, Regex: options.regex, ReportDir: options.reportDir})
	if err != nil {
		return err
	}
	if result.NoRecords {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "no CI coverage reports collected yet")
		return err
	}
	if err := writeCoverageOutputTo(cmd.OutOrStdout(), result.Report, options.format, result.Artifacts); err != nil {
		return err
	}
	return coverageGateError(runtime, result.Report, options.minimumCoverage)
}
func runChanged(cmd *cobra.Command, path string, options qualityOptions, runtime shared.Runtime, deps Dependencies) error {
	annotate := deps.WorkflowAnnotations()
	result, err := deps.Changed(cmd.Context(), qualityrun.ChangedRequest{Path: path, Target: options.target, BaselineFile: options.baselineFile, ReportDir: options.reportDir, BaselineTimeout: options.baselineTimeout, Run: coverageOptionsForCommand(options), AffectedPackages: options.affectedPackages, Minimum: options.minimumCoverage, Diagnostics: cmd.ErrOrStderr(), BeforePersist: func(report qualityrun.ChangedReport) {
		warnRedBase(cmd.ErrOrStderr(), report.RedBase, annotate)
		warnToleratedCoverage(cmd.ErrOrStderr(), report.Packages, annotate)
	}})
	if err != nil {
		return err
	}
	if result.HasReport {
		if err := writeChangedCoverageOutputTo(cmd.OutOrStdout(), result.Report, options.format); err != nil {
			return err
		}
	}
	if result.Findings != "" {
		return runtime.ExitError(shared.ExitFindings, result.Findings)
	}
	return nil
}
func warnRedBase(stderr io.Writer, redBase *quality.RedBaseline, annotate bool) {
	if redBase == nil {
		return
	}
	if annotate {
		writeWorkflowWarning(stderr, "Coverage baseline measured on a red merge base", fmt.Sprintf("%d test(s) fail at %s: %s. The count-rise check is looser for their packages.", len(redBase.FailedTests), redBase.SHA, strings.Join(redBase.FailedTests, ", ")))
	}
	_, _ = fmt.Fprintf(stderr, "WARNING: the coverage baseline was measured on a RED merge base: %d test(s) fail at %s.\n", len(redBase.FailedTests), redBase.SHA)
	for _, name := range redBase.FailedTests {
		_, _ = fmt.Fprintf(stderr, "  failed at base: %s\n", name)
	}
	_, _ = fmt.Fprintln(stderr, "  The baseline uses the coverage those test runs still wrote, so the uncovered counts of their packages are an upper bound and the count-rise check is looser for them. Changed statements are still held to full coverage.")
}
func GitHubActionsEnabled(getenv func(string) string) bool {
	return getenv("GITHUB_ACTIONS") == "true"
}
func writeWorkflowWarning(stderr io.Writer, title, message string) {
	replacer := strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A", "::", "%3A%3A")
	_, _ = fmt.Fprintf(stderr, "::warning title=%s::%s\n", title, replacer.Replace(message))
}
func warnToleratedCoverage(stderr io.Writer, results []quality.PackageRatchet, annotate bool) {
	for _, result := range results {
		if len(result.Tolerated) == 0 {
			continue
		}
		if annotate {
			statements := make([]string, 0, len(result.Tolerated))
			for _, statement := range result.Tolerated {
				statements = append(statements, fmt.Sprintf("%s:%d (%s)", statement.File, statement.Line, statement.Function))
			}
			writeWorkflowWarning(stderr, "Coverage ratchet tolerance used", fmt.Sprintf("%s: uncovered count %d is above baseline %d, within the configured tolerance of %d statement(s). Tolerated: %s. Reason: %s", result.Package, result.Uncovered, result.BaselineUncovered, result.Tolerance, strings.Join(statements, ", "), result.Tolerated[0].Reason))
		}
		_, _ = fmt.Fprintf(stderr, "WARNING: coverage ratchet tolerance used for %s: uncovered count %d is above baseline %d, within the configured tolerance of %d statement(s). Reason: %s\n", result.Package, result.Uncovered, result.BaselineUncovered, result.Tolerance, result.Tolerated[0].Reason)
		for _, statement := range result.Tolerated {
			_, _ = fmt.Fprintf(stderr, "  tolerated: %s:%d (in %s): %s\n", statement.File, statement.Line, statement.Function, quality.ReasonNewlyUncoveredAtBase)
		}
	}
}
func writeChangedCoverageOutputTo(out io.Writer, report qualityrun.ChangedReport, format string) error {

	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	_, _ = fmt.Fprintf(out, "coverage ratchet against %s (merge base %s)\n", report.Target, report.MergeBase)
	for _, result := range report.Packages {
		status := "PASS"
		if !result.Pass {
			status = "FAIL"
		}
		baselineText := "no baseline"
		if result.HasBaseline {
			baselineText = fmt.Sprintf("baseline %d", result.BaselineUncovered)
		}
		_, _ = fmt.Fprintf(out, "  %s %s: uncovered %d (%s)\n", status, result.Package, result.Uncovered, baselineText)
		for _, finding := range result.NewlyUncoveredChanged {
			_, _ = fmt.Fprintf(out, "    %s:%d: %s\n", finding.File, finding.Line, finding.Reason)
		}
		for _, statement := range result.Tolerated {
			_, _ = fmt.Fprintf(out, "    TOLERATED (tolerance %d) %s:%d (in %s): %s\n", result.Tolerance, statement.File, statement.Line, statement.Function, statement.Reason)
		}
	}
	for _, warning := range report.Warnings {
		_, _ = fmt.Fprintf(out, "  WARN %s %s:%d: %s\n", warning.Package, warning.File, warning.Line, warning.Reason)
	}
	return nil
}
