package qualityrun

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/quality"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ChangedRequest struct {
	Path, Target, BaselineFile, ReportDir string
	BaselineTimeout                       time.Duration
	Run                                   quality.RunOptions
	AffectedPackages                      bool
	Minimum                               float64
	Diagnostics                           io.Writer
	BeforePersist                         func(ChangedReport)
}
type ChangedResult struct {
	Report    ChangedReport
	HasReport bool
	Findings  string
}
type changedOptions struct {
	target, baselineFile, coverageProfile                string
	baselineTimeout                                      time.Duration
	packagePatterns                                      []string
	explicitGoTestPackages, affectedPackages, includeE2E bool
	run                                                  quality.RunOptions
}

func (o changedOptions) runOptions() quality.RunOptions {
	options := o.run
	// Scope selection changes packagePatterns after the request is captured.
	// Both head and merge-base measurements must use that same logical scope.
	options.GoTestPackages = append([]string(nil), o.packagePatterns...)
	return options
}

type ChangedReport struct {
	ScopeIdentity *quality.CoverageScopeIdentity `yaml:"scope_identity,omitempty" json:"scope_identity,omitempty"`
	ScopeReason   string                         `yaml:"scope_reason,omitempty" json:"scope_reason,omitempty"`
	ChangedScope  []string                       `yaml:"changed_scope,omitempty" json:"changed_scope,omitempty"`
	Scope         []string                       `yaml:"scope,omitempty" json:"scope,omitempty"`
	MergeBase     string                         `yaml:"merge_base" json:"merge_base"`
	Target        string                         `yaml:"target" json:"target"`
	Packages      []quality.PackageRatchet       `yaml:"packages" json:"packages"`
	// Warnings are count rises in packages the PR did not itself change
	// (founder decision 2026-09-23, review B1): reported, never failed on.
	Warnings []quality.RatchetWarning `yaml:"warnings" json:"warnings"`
	// RedBase is set when the merge base was measured while its own tests
	// were failing: the ratchet still ran, against an upper-bound baseline.
	RedBase *quality.RedBaseline `yaml:"red_base,omitempty" json:"red_base,omitempty"`
}

func changedCoverageProfilePathInjected(existing string, inj *filewrite.Injector) (path string, removeProfile bool, err error) {
	if existing != "" {
		return existing, false, nil
	}
	path, err = filewrite.CreateScratch("", "wb-coverage-changed-*.out", 0, nil, inj)
	if err != nil && path == "" {
		return "", false, err
	}
	return path, true, nil
}
func loadOrMeasureBaseline(ctx context.Context, stderr io.Writer, repoPath, mergeBase string, options changedOptions) (quality.PackageBaseline, error) {
	if options.baselineFile != "" && !options.explicitGoTestPackages {
		baseline, err := quality.LoadBaseline(options.baselineFile)
		switch {
		case err == nil:
			validateErr := quality.ValidateBaseline(baseline, mergeBase)
			if validateErr == nil && baseline.IncludeE2E != options.includeE2E {
				validateErr = fmt.Errorf("baseline include_e2e=%t differs from requested include_e2e=%t", baseline.IncludeE2E, options.includeE2E)
			}
			if validateErr != nil {
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
	return quality.ComputeBaselineAtRef(ctx, repoPath, mergeBase, options.baselineTimeout, options.runOptions())
}

// changedOperations contains actual Git/measurement effects; algorithms and
// ordinary file reads remain concrete. Each request owns its operations value.
type changedOperations struct {
	Abs          func(string) (string, error)
	MergeBase    func(context.Context, string, string) (string, error)
	ChangedLines func(context.Context, string, string) (quality.ChangedLines, error)
	TouchedFiles func(context.Context, string, string) (map[string]bool, error)
	LineOffsets  func(context.Context, string, string) (map[string]quality.FileLineOffsets, error)
	Scope        func(context.Context, string, string, map[string]bool, bool) (quality.CoverageSelection, error)
	ProfilePath  func(string) (string, bool, error)
	Cover        func(context.Context, string, string, quality.RunOptions) quality.RepositoryCoverage
	Baseline     func(context.Context, io.Writer, string, string, changedOptions) (quality.PackageBaseline, error)
}

func realChangedOperations() changedOperations {
	return changedOperations{
		Abs: filepath.Abs, MergeBase: quality.GitMergeBase, ChangedLines: quality.GitChangedLines, TouchedFiles: quality.GitTouchedFiles, LineOffsets: quality.GitLineOffsets, Scope: quality.AffectedCoverageScope, Cover: quality.CoverWithOptions,
		ProfilePath: func(existing string) (string, bool, error) { return changedCoverageProfilePathInjected(existing, nil) }, Baseline: loadOrMeasureBaseline,
	}
}
func ChangedCoverage(ctx context.Context, request ChangedRequest) (ChangedResult, error) {
	return changedWith(ctx, request, realChangedOperations())
}
func changedWith(ctx context.Context, request ChangedRequest, ops changedOperations) (ChangedResult, error) {
	options := changedOptions{target: request.Target, baselineFile: request.BaselineFile, coverageProfile: request.Run.CoverageProfile, baselineTimeout: request.BaselineTimeout, packagePatterns: append([]string(nil), request.Run.GoTestPackages...), explicitGoTestPackages: len(request.Run.GoTestPackages) > 0, affectedPackages: request.AffectedPackages, includeE2E: request.Run.IncludeE2E, run: request.Run}
	// filepath.Abs only fails when os.Getwd fails (an unreadable/removed
	// working directory), which this command's own process would already be
	// unable to run in.
	repoPath, err := ops.Abs(request.Path)
	if err != nil {
		return ChangedResult{}, err
	}
	modulePath, err := quality.ReadModulePath(repoPath)
	if err != nil {
		return ChangedResult{}, fmt.Errorf("--changed requires exactly one Go module at %s: %w", repoPath, err)
	}

	// The timing tolerance is read from repoPath, the checkout being judged,
	// and before anything is measured: a malformed policy must not cost a
	// full coverage run to discover. The merge-base checkout's copy is never
	// consulted, so a change that adds or tightens an entry is judged by it.
	tolerances, err := quality.LoadRatchetTolerances(repoPath)
	if err != nil {
		return ChangedResult{}, err
	}

	mergeBase, err := ops.MergeBase(ctx, repoPath, options.target)
	if err != nil {
		return ChangedResult{}, err
	}

	changedLines, err := ops.ChangedLines(ctx, repoPath, mergeBase)
	if err != nil {
		return ChangedResult{}, err
	}
	// touchedFiles (added, modified, deleted, or renamed against mergeBase)
	// decides which packages the PR "changes" for the per-package ratchet's
	// scope (founder decision 2026-09-23, review B1): a pure deletion (for
	// example removing a test) never appears in changedLines (it added no
	// line) but still makes the PR own that package.
	touchedFiles, err := ops.TouchedFiles(ctx, repoPath, mergeBase)
	if err != nil {
		return ChangedResult{}, err
	}
	var changedOwners []string
	var scopeReason string
	var scopeIdentity *quality.CoverageScopeIdentity
	if options.affectedPackages {
		selection, err := ops.Scope(ctx, repoPath, mergeBase, touchedFiles, options.includeE2E)
		if err != nil {
			return ChangedResult{}, err
		}
		options.packagePatterns = selection.Packages
		changedOwners = selection.ChangedPackages
		scopeReason = selection.Reason
		scopeIdentity = selection.Identity
		options.explicitGoTestPackages = true
	}
	logicalPackages := append([]string(nil), options.packagePatterns...)
	if options.explicitGoTestPackages {
		options.packagePatterns, err = quality.ExistingCoveragePackages(repoPath, logicalPackages)
		if err != nil {
			return ChangedResult{}, err
		}
		if len(options.packagePatterns) == 0 {
			if options.coverageProfile != "" {
				if err := filewrite.WriteBytesAtomic(filepath.Dir(options.coverageProfile), filepath.Base(options.coverageProfile), []byte("mode: set\n"), 0o644); err != nil {
					return ChangedResult{}, err
				}
			}
			report := ChangedReport{MergeBase: mergeBase, Target: options.target, Scope: logicalPackages, ScopeReason: scopeReason, ChangedScope: changedOwners, ScopeIdentity: scopeIdentity}
			if err := PersistChanged(report, request.ReportDir); err != nil {
				return ChangedResult{}, err
			}
			return ChangedResult{Report: report, HasReport: true}, nil
		}
	}

	// lineOffsets lets a count-only rise (review B2) name the current
	// file:line of a pre-existing statement whose file the PR also edited
	// elsewhere, instead of treating every uncovered block in that file as
	// unattributable (review-696 non-blocking #3).
	lineOffsets, err := ops.LineOffsets(ctx, repoPath, mergeBase)
	if err != nil {
		return ChangedResult{}, err
	}

	profilePath, removeProfile, err := ops.ProfilePath(options.coverageProfile)
	if err != nil {
		return ChangedResult{}, err
	}
	if removeProfile {
		defer func() { _ = os.Remove(profilePath) }()
	}

	// Measure through the same sharded, retried, hermetically-wrapped runner
	// the plain `wb coverage` path uses, instead of a bare `go test ./...`:
	// that keeps .wb/quality.yaml's shard policy, --timeout/--retry, and
	// coverage-diagnostics-on-failure working for --changed too
	// (spec/plans/coverage-to-100/README.md task-3, review item 5).
	base := options.runOptions()
	base.CoverageProfile = profilePath
	runOpts, err := quality.RepositoryRunOptions(repoPath, base)
	if err != nil {
		return ChangedResult{}, err
	}
	if options.explicitGoTestPackages {
		runOpts = quality.SelectedCoverageOptions(runOpts, options.packagePatterns)
	}

	coverageReport := ops.Cover(ctx, filepath.Base(repoPath), repoPath, runOpts)
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
		return ChangedResult{Findings: message}, nil
	}

	blocks, err := quality.ParseCoverageProfile(profilePath)
	if err != nil {
		return ChangedResult{}, fmt.Errorf("parse coverage profile %s produced by go test: %w", profilePath, err)
	}

	options.packagePatterns = logicalPackages
	baseline, err := ops.Baseline(ctx, request.Diagnostics, repoPath, mergeBase, options)
	if err != nil {
		return ChangedResult{}, err
	}

	results, warnings := quality.EvaluateRatchet(blocks, changedLines, touchedFiles, lineOffsets, baseline, modulePath, tolerances, changedOwners)

	report := ChangedReport{
		MergeBase:    mergeBase,
		Target:       options.target,
		Scope:        logicalPackages,
		ScopeReason:  scopeReason,
		ChangedScope: changedOwners, ScopeIdentity: scopeIdentity,
		Packages: results,
		Warnings: warnings,
		RedBase:  baseline.RedBase,
	}
	if request.BeforePersist != nil {
		request.BeforePersist(report)
	}
	if err := PersistChanged(report, request.ReportDir); err != nil {
		return ChangedResult{}, err
	}
	findings := changedCoverageMinimumFindings(blocks, request.Minimum)
	if findings == "" {
		findings = changedCoverageRatchetFindings(results)
	}
	return ChangedResult{Report: report, HasReport: true, Findings: findings}, nil
}
func changedCoverageRatchetFindings(results []quality.PackageRatchet) string {
	var failing []string
	for _, result := range results {
		if result.Pass {
			continue
		}
		if result.Rose {
			failing = append(failing, fmt.Sprintf("%s: uncovered count %d rose above baseline %d", result.Package, result.Uncovered, result.BaselineUncovered))
		}
		for _, finding := range result.NewlyUncoveredChanged {
			failing = append(failing, fmt.Sprintf("%s:%d: %s", finding.File, finding.Line, finding.Reason))
		}
	}
	if len(failing) == 0 {
		return ""
	}
	sort.Strings(failing)
	return "coverage ratchet failed:\n  " + strings.Join(failing, "\n  ")
}
func changedCoverageMinimumFindings(blocks []quality.CoverageBlock, minimum float64) string {
	if minimum < 0 {
		return ""
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
		return fmt.Sprintf("coverage %.2f%% is below required %.2f%%", percentage, minimum)
	}
	return ""
}
func PersistChanged(report ChangedReport, reportDir string) error {
	if reportDir != "" {
		if err := os.MkdirAll(reportDir, 0o755); err != nil {
			return err
		}
		// ChangedReport holds only strings, ints, bools, and slices
		// of quality.PackageRatchet — the same directly JSON-marshalable
		// shapes as quality.PackageBaseline — so MarshalIndent's error is
		// discarded rather than kept as an untestable dead branch, the
		// same as WriteBaseline (internal/quality/ratchet.go).
		encoded, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(filepath.Join(reportDir, "coverage-ratchet.json"), append(encoded, '\n'), 0o644); err != nil {
			return err
		}
	}
	return nil
}
