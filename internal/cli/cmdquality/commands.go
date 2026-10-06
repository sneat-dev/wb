package cmdquality

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/qualityrun"
	"github.com/sneat-dev/wb/internal/reposelection"
	"github.com/spf13/cobra"
	"time"
)

type qualityOptions struct {
	ci                     bool
	fleet                  bool
	match                  string
	regex                  string
	parallel               int
	format                 string
	reportDir              string
	checks                 string
	timeout                time.Duration
	retry                  int
	resume                 bool
	testShards             int
	shardPackages          []string
	explicitGoTestSharding bool
	packagePatterns        []string
	explicitGoTestPackages bool
	coverageProfile        string
	includeE2E             bool
	minimumCoverage        float64
	// changed selects the per-change coverage ratchet
	// (spec/plans/coverage-to-100/README.md task-3): a package fails when its
	// uncovered-statement count rises against its baseline, or when a changed,
	// non-moved line is uncovered.
	changed bool
	// target is the merge-base branch/ref --changed diffs against.
	affectedPackages bool
	target           string
	// baselineFile is the per-package uncovered-count baseline published by
	// go-ci's coverage job as a build artifact. When empty or missing, the
	// merge base is measured directly instead, bounded by baselineTimeout.
	baselineFile    string
	baselineTimeout time.Duration
	// allowEmpty lets fleet mode return zero targets instead of erroring.
	// an empty fleet with no filter publishes an empty-but-valid snapshot;
	// an unmatched filter is still an error. Quality commands (coverage/verify/check/fleet)
	// want the error: an empty match is almost always a typo'd --filter.
	allowEmpty bool
}

func NewCoverage(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	options := qualityOptions{testShards: 1, minimumCoverage: -1, baselineTimeout: 20 * time.Minute}
	command := &cobra.Command{
		Use:   "coverage [repository-path]",
		Short: "Measure Go test coverage for one repository or the local fleet",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			options.explicitGoTestSharding = coverageShardingExplicit(cmd)
			options.explicitGoTestPackages = coveragePackagesExplicit(cmd)
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			if options.fleet && len(args) > 0 {
				return fmt.Errorf("repository-path cannot be used with --fleet")
			}
			if !options.changed && cmd.Flags().Changed("baseline-timeout") {
				// exitUsage (not the plain fmt.Errorf the surrounding
				// validation uses): --baseline-timeout is flatly ignored
				// without --changed, which AGENTS.md's ignored-flags rule
				// treats as a usage error (exit 2), the same contract
				// cobra's own flag-parse errors already get.
				return runtime.ExitError(shared.ExitUsage, "--baseline-timeout requires --changed")
			}
			if err := validateCoverageExecutionOptions(runtime, options); err != nil {
				return err
			}
			if options.ci {
				return runStored(cmd, path, options, runtime, deps)
			}
			if options.changed {
				return runChanged(cmd, path, options, runtime, deps)
			}
			result, err := deps.Coverage(context.Background(), qualityrun.CoverageRequest{
				Selection: selectionRequest(path, runtime.Flags(), options), Run: coverageOptionsForCommand(options), Resume: options.resume, ReportDir: options.reportDir, Observer: newObserver(cmd, runtime, "coverage"),
			})
			if err != nil {
				return err
			}
			if result.NoWork {
				_, err := fmt.Fprintln(cmd.ErrOrStderr(), "no failed repositories to resume; nothing to do")
				return err
			}
			if err := writeCoverageOutputTo(cmd.OutOrStdout(), result.Report, options.format, result.Artifacts); err != nil {
				return err
			}
			return coverageGateError(runtime, result.Report, options.minimumCoverage)
		},
	}
	bindQualityScopeFlags(command, &options)
	command.Flags().BoolVar(&options.ci, "ci", false, "display latest CI-reported test coverage without running tests locally")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, json, or summary (summary requires --report-dir)")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write coverage.md and coverage.yaml to this directory")
	command.Flags().IntVar(&options.testShards, "test-shards", 1, "process-isolated shards for every explicit --shard-package")
	command.Flags().StringArrayVar(&options.packagePatterns, "package", nil, "restrict coverage to explicit Go package patterns; packages outside this scope are not tested (repeatable)")
	command.Flags().StringArrayVar(&options.shardPackages, "shard-package", nil, "single Go package within the --package scope safe to shard by top-level test name; selected packages not named here run once (repeatable)")
	command.Flags().StringVar(&options.coverageProfile, "coverage-profile", "", "retain the exact merged profile (single repository and Go module only)")
	command.Flags().BoolVar(&options.includeE2E, "include-e2e", false, "merge native E2E and contract coverage with the default test tier")
	command.Flags().Float64Var(&options.minimumCoverage, "minimum", -1, "minimum aggregate statement coverage percentage; disabled when omitted")
	command.Flags().BoolVar(&options.affectedPackages, "affected-packages", false, "scope --changed coverage to changed packages and affected dependents across both revisions")
	command.Flags().BoolVar(&options.changed, "changed", false, "apply the per-change coverage ratchet against --target instead of a plain repository/fleet run")
	command.Flags().StringVar(&options.target, "target", "", "merge-base branch or ref for --changed (required with --changed)")
	command.Flags().StringVar(&options.baselineFile, "baseline-file", "", "per-package uncovered-count baseline JSON for --changed; measures the merge base directly when empty or missing")
	command.Flags().DurationVar(&options.baselineTimeout, "baseline-timeout", 20*time.Minute, "wall-time budget for measuring the merge base directly when --baseline-file is empty or missing")
	command.AddCommand(newCoverageBaselineCmd(deps))
	command.AddCommand(newCoverageSummaryCmd(deps))
	command.AddCommand(newCoverageWorklistCmd(runtime, deps))
	return command
}

func NewVerify(runtime shared.Runtime, deps Dependencies) *cobra.Command {
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
			result, err := deps.Verification(context.Background(), qualityrun.VerificationRequest{
				Selection: selectionRequest(path, runtime.Flags(), options), Run: runOptions(options), Checks: checks, Name: "verify", Profile: "", ReportDir: options.reportDir, Resume: options.resume, Observer: newObserver(cmd, runtime, "verify"),
			})
			if err != nil {
				return err
			}
			if result.NoWork {
				_, err := fmt.Fprintln(cmd.ErrOrStderr(), "no failed repositories to resume; nothing to do")
				return err
			}
			if err := writeVerificationOutput(cmd.OutOrStdout(), result.Report, options.format); err != nil {
				return err
			}
			if verificationFailed(result.Report) {
				return runtime.ExitError(shared.ExitFindings, "verification failed in one or more repositories; the failing check and its output are in the `detail` of each `failed` row above")
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

func NewCheck(runtime shared.Runtime, deps Dependencies) *cobra.Command {
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
			result, err := deps.Verification(context.Background(), qualityrun.VerificationRequest{
				Selection: selectionRequest(path, runtime.Flags(), options), Run: runOptions(options), Checks: checks, Name: "check", Profile: profile, ReportDir: options.reportDir, Resume: options.resume, Observer: newObserver(cmd, runtime, "check"),
			})
			if err != nil {
				return err
			}
			if result.NoWork {
				_, err := fmt.Fprintln(cmd.ErrOrStderr(), "no failed repositories to resume; nothing to do")
				return err
			}
			if err := writeVerificationOutput(cmd.OutOrStdout(), result.Report, options.format); err != nil {
				return err
			}
			if verificationFailed(result.Report) {
				return runtime.ExitError(shared.ExitFindings, "profile checks failed in one or more repositories; the failing check and its output are in the `detail` of each `failed` row above")
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

func validateCoverageExecutionOptions(runtime shared.Runtime, options qualityOptions) error {
	if options.includeE2E && (options.ci || options.resume) {
		return fmt.Errorf("--include-e2e requires a fresh coverage run; it cannot be combined with --ci or --resume")
	}
	if options.ci {
		if options.changed {
			return runtime.ExitError(shared.ExitUsage, "--ci cannot be combined with --changed")
		}
		if options.resume {
			return fmt.Errorf("--ci cannot be combined with --resume")
		}
		if options.coverageProfile != "" {
			return fmt.Errorf("--ci cannot be combined with --coverage-profile")
		}
		if options.explicitGoTestSharding {
			return fmt.Errorf("--ci cannot be combined with --test-shards or --shard-package")
		}
		if options.explicitGoTestPackages {
			return fmt.Errorf("--ci cannot be combined with --package")
		}
	}
	if options.affectedPackages && (!options.changed || options.explicitGoTestPackages) {
		return runtime.ExitError(shared.ExitUsage, "--affected-packages requires --changed and cannot be combined with --package")
	}
	if options.changed && options.explicitGoTestPackages {
		return fmt.Errorf("--changed cannot be combined with --package")
	}
	if options.affectedPackages && options.minimumCoverage >= 0 {
		return runtime.ExitError(shared.ExitUsage, "scoped changed coverage cannot establish a repository-wide --minimum")
	}
	if options.changed && options.explicitGoTestSharding {
		return fmt.Errorf("--changed cannot be combined with --test-shards or --shard-package")
	}
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
	if options.explicitGoTestPackages {
		if err := quality.ValidateGoCoveragePackagePatterns(options.packagePatterns); err != nil {
			return err
		}
		if options.fleet {
			return fmt.Errorf("--package is repository-specific and cannot be combined with --fleet")
		}
		if options.resume {
			return fmt.Errorf("--package cannot be combined with --resume")
		}
	}
	if options.changed {
		if options.target == "" {
			return fmt.Errorf("--changed requires --target <merge-base branch or ref>")
		}
		if options.fleet {
			return fmt.Errorf("--changed is repository-specific and cannot be combined with --fleet")
		}
		if options.resume {
			return fmt.Errorf("--changed cannot be combined with --resume")
		}
		if options.format != "markdown" && options.format != "json" {
			// exitUsage for the same reason as --baseline-timeout above: an
			// unsupported --format under --changed is rejected, not
			// silently ignored, so it is a usage error.
			return runtime.ExitError(shared.ExitUsage, fmt.Sprintf("--changed supports --format markdown or json only, not %q", options.format))
		}
	} else if options.target != "" {
		// exitUsage: --target is a flag this PR added, and every ignored or
		// misused flag this PR added exits 2 (AGENTS.md's ignored-flags
		// rule), matching --baseline-timeout and --format above.
		return runtime.ExitError(shared.ExitUsage, "--target requires --changed")
	} else if options.baselineFile != "" {
		return runtime.ExitError(shared.ExitUsage, "--baseline-file requires --changed")
	}
	return nil
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

func runOptions(options qualityOptions) quality.RunOptions {
	return quality.RunOptions{Timeout: options.timeout, Retry: options.retry, IncludeE2E: options.includeE2E, CoverageDiagnosticsDir: options.reportDir}
}

func coverageShardingExplicit(command *cobra.Command) bool {
	return command.Flags().Changed("test-shards") || command.Flags().Changed("shard-package")
}

func coveragePackagesExplicit(command *cobra.Command) bool {
	return command.Flags().Changed("package")
}

func coverageOptionsForCommand(options qualityOptions) quality.RunOptions {
	runOptions := runOptions(options)
	runOptions.GoTestShards = options.testShards
	runOptions.GoShardPackages = append([]string(nil), options.shardPackages...)
	runOptions.GoTestPackages = append([]string(nil), options.packagePatterns...)
	runOptions.ExplicitGoTestSharding = options.explicitGoTestSharding
	runOptions.CoverageProfile = options.coverageProfile
	return runOptions
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

func selectionRequest(path string, flags shared.Flags, options qualityOptions) reposelection.Request {
	return reposelection.Request{Path: path, ProjectsRoot: flags.ProjectsRoot, Filter: flags.Filter, Fleet: options.fleet, Match: options.match, Regex: options.regex, Parallel: options.parallel, Retry: options.retry, Timeout: options.timeout, AllowEmpty: options.allowEmpty}
}
func newObserver(cmd *cobra.Command, runtime shared.Runtime, operation string) qualityrun.Observer {
	var progress *qualityProgress
	return qualityrun.Observer{
		Started: func(total int) {
			progress = newQualityProgress(cmd.ErrOrStderr(), console.Interactive(cmd.ErrOrStderr(), runtime.Flags().NonInteractive), operation, total)
			progress.start()
		},
		Progress: func(event quality.Progress) { progress.report(event) }, Finished: func() { progress.finish() },
	}
}
