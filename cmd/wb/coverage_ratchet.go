package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/quality"
)

// newCoverageBaselineCmd implements `wb coverage baseline`: it parses an
// already-measured Go coverage profile into the per-package uncovered-count
// baseline JSON go-ci's coverage job publishes as a build artifact on every
// push to main (spec/plans/coverage-to-100/README.md task-3(b)) — the sole
// producer `wb coverage --changed` consumes via --baseline-file.
func newCoverageBaselineCmd() *cobra.Command {
	var (
		module string
		sha    string
		out    string
	)
	command := &cobra.Command{
		Use:   "baseline <coverage-profile>",
		Short: "Write the per-package uncovered-count baseline from a measured coverage profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			modulePath, err := quality.ReadModulePath(module)
			if err != nil {
				return err
			}
			blocks, err := quality.ParseCoverageProfile(args[0])
			if err != nil {
				return err
			}
			baseline := quality.BaselineFromProfile(blocks, modulePath, sha)
			return quality.WriteBaseline(out, baseline)
		},
	}
	command.Flags().StringVar(&module, "module", ".", "path to the Go module root (its go.mod names the module path coverage profiles use)")
	command.Flags().StringVar(&sha, "sha", "", "commit SHA the profile was measured at, recorded in the baseline for traceability")
	command.Flags().StringVar(&out, "out", "coverage-baseline.json", "output path for the baseline JSON")
	return command
}

// runChangedCoverage implements `wb coverage --changed --target <base>`, the
// per-change coverage ratchet (spec/plans/coverage-to-100/README.md task-3):
// a package fails when its uncovered-statement count rises against its
// baseline, or when a statement a PR added or changed against the merge base
// is uncovered and is not a moved, unmodified line.
func runChangedCoverage(cmd *cobra.Command, path string, options qualityOptions) error {
	ctx := cmd.Context()
	// filepath.Abs only fails when os.Getwd fails (an unreadable/removed
	// working directory), which this command's own process would already be
	// unable to run in.
	repoPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	modulePath, err := quality.ReadModulePath(repoPath)
	if err != nil {
		return fmt.Errorf("--changed requires exactly one Go module at %s: %w", repoPath, err)
	}

	mergeBase, err := gitMergeBase(ctx, repoPath, options.target)
	if err != nil {
		return err
	}

	changedLines, err := quality.GitChangedLines(ctx, repoPath, mergeBase)
	if err != nil {
		return err
	}

	profilePath := options.coverageProfile
	removeProfile := false
	if profilePath == "" {
		file, err := os.CreateTemp("", "wb-coverage-changed-*.out")
		if err != nil {
			return err
		}
		profilePath = file.Name()
		_ = file.Close()
		removeProfile = true
	}
	if removeProfile {
		defer func() { _ = os.Remove(profilePath) }()
	}

	// Measure through the same sharded, retried, hermetically-wrapped runner
	// the plain `wb coverage` path uses, instead of a bare `go test ./...`:
	// that keeps .wb/quality.yaml's shard policy, --timeout/--retry, and
	// coverage-diagnostics-on-failure working for --changed too
	// (spec/plans/coverage-to-100/README.md task-3, review item 5).
	base := runOptions(options)
	base.CoverageProfile = profilePath
	runOpts, err := quality.RepositoryRunOptions(repoPath, base)
	if err != nil {
		return err
	}
	coverageReport := quality.CoverWithOptions(ctx, filepath.Base(repoPath), repoPath, runOpts)
	if coverageReport.Status == quality.StatusFailed {
		message := "coverage could not be measured: " + coverageReport.Error
		// coverageReport.Diagnostic is non-nil for a process-isolated shard
		// failure (coverageDiagnosticFor,
		// internal/quality/go_coverage_runner.go): although
		// validateCoverageExecutionOptions rejects --test-shards under
		// --changed, runOpts above still picks up .wb/quality.yaml's own
		// go_test.shards policy through quality.RepositoryRunOptions, so a
		// repository that shards internal/worktrees or internal/orchestrate
		// can still fail with a manifest on disk here.
		if coverageReport.Diagnostic != nil {
			message += fmt.Sprintf(" (diagnostic manifest %s)", coverageReport.Diagnostic.Manifest)
		}
		return &exitError{code: exitFindings, message: message}
	}

	blocks, err := quality.ParseCoverageProfile(profilePath)
	if err != nil {
		return fmt.Errorf("parse coverage profile %s produced by go test: %w", profilePath, err)
	}

	baseline, err := loadOrMeasureBaseline(ctx, cmd.ErrOrStderr(), repoPath, mergeBase, options)
	if err != nil {
		return err
	}

	results := quality.EvaluateRatchet(blocks, changedLines, baseline, modulePath)

	report := changedCoverageReport{
		MergeBase: mergeBase,
		Target:    options.target,
		Packages:  results,
	}
	if err := writeChangedCoverageOutputTo(cmd.OutOrStdout(), report, options.format, options.reportDir); err != nil {
		return err
	}

	if minimumErr := changedCoverageMinimumError(blocks, options.minimumCoverage); minimumErr != nil {
		return minimumErr
	}
	if ratchetErr := changedCoverageRatchetError(results); ratchetErr != nil {
		return ratchetErr
	}
	return nil
}

// loadOrMeasureBaseline reads the published per-package baseline when
// options.baselineFile names a readable file, and otherwise measures the
// merge base directly, bounded by options.baselineTimeout
// (spec/plans/coverage-to-100/README.md task-3(b)).
func loadOrMeasureBaseline(ctx context.Context, stderr io.Writer, repoPath, mergeBase string, options qualityOptions) (quality.PackageBaseline, error) {
	if options.baselineFile != "" {
		baseline, err := quality.LoadBaseline(options.baselineFile)
		switch {
		case err == nil:
			if validateErr := quality.ValidateBaseline(baseline, mergeBase); validateErr != nil {
				// A baseline that parses as JSON but is not usable (wrong
				// schema, empty, or measured for a different commit) must
				// never pass the ratchet silently: fall back to measuring
				// the merge base directly, the same as a missing artifact.
				_, _ = fmt.Fprintf(stderr, "baseline artifact at %s is unusable (%v); measuring merge base %s directly (bounded by --baseline-timeout %s)\n", options.baselineFile, validateErr, mergeBase, options.baselineTimeout)
				break
			}
			return baseline, nil
		case os.IsNotExist(err):
			_, _ = fmt.Fprintf(stderr, "no baseline artifact at %s; measuring merge base %s directly (bounded by --baseline-timeout %s)\n", options.baselineFile, mergeBase, options.baselineTimeout)
		default:
			return quality.PackageBaseline{}, fmt.Errorf("--baseline-file %s: %w", options.baselineFile, err)
		}
	}
	return quality.ComputeBaselineAtRef(ctx, repoPath, mergeBase, options.baselineTimeout, runOptions(options))
}

func gitMergeBase(ctx context.Context, repoPath, target string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "merge-base", "HEAD", target)
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git merge-base HEAD %s: %w: %s", target, err, string(exitErr.Stderr))
		}
		return "", fmt.Errorf("git merge-base HEAD %s: %w", target, err)
	}
	return strings.TrimSpace(string(output)), nil
}

type changedCoverageReport struct {
	MergeBase string                   `yaml:"merge_base" json:"merge_base"`
	Target    string                   `yaml:"target" json:"target"`
	Packages  []quality.PackageRatchet `yaml:"packages" json:"packages"`
}

func changedCoverageRatchetError(results []quality.PackageRatchet) error {
	var failing []string
	for _, result := range results {
		if result.Pass {
			continue
		}
		if result.Rose {
			failing = append(failing, fmt.Sprintf("%s: uncovered count %d rose above baseline %d", result.Package, result.Uncovered, result.BaselineUncovered))
		}
		for _, finding := range result.NewlyUncoveredChanged {
			failing = append(failing, fmt.Sprintf("%s:%d: added or changed statement is not covered by a test", finding.File, finding.Line))
		}
	}
	if len(failing) == 0 {
		return nil
	}
	sort.Strings(failing)
	return &exitError{
		code:    exitFindings,
		message: "coverage ratchet failed:\n  " + strings.Join(failing, "\n  "),
	}
}

func changedCoverageMinimumError(blocks []quality.CoverageBlock, minimum float64) error {
	if minimum < 0 {
		return nil
	}
	statements, covered := 0, 0
	for _, block := range blocks {
		statements += block.Statements
		if block.Count > 0 {
			covered += block.Statements
		}
	}
	percentage := 0.0
	if statements > 0 {
		percentage = float64(covered) * 100 / float64(statements)
	}
	if percentage < minimum {
		return &exitError{
			code:    exitFindings,
			message: fmt.Sprintf("coverage %.2f%% is below required %.2f%%", percentage, minimum),
		}
	}
	return nil
}

func writeChangedCoverageOutputTo(out io.Writer, report changedCoverageReport, format, reportDir string) error {
	if reportDir != "" {
		if err := os.MkdirAll(reportDir, 0o755); err != nil {
			return err
		}
		// changedCoverageReport holds only strings, ints, bools, and slices
		// of quality.PackageRatchet — the same directly JSON-marshalable
		// shapes as quality.PackageBaseline — so MarshalIndent's error is
		// discarded rather than kept as an untestable dead branch, the
		// same as WriteBaseline (internal/quality/ratchet.go).
		encoded, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(filepath.Join(reportDir, "coverage-ratchet.json"), append(encoded, '\n'), 0o644); err != nil {
			return err
		}
	}
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
			_, _ = fmt.Fprintf(out, "    %s:%d: added or changed statement is not covered by a test\n", finding.File, finding.Line)
		}
	}
	return nil
}
