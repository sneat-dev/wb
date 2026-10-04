package cmddeps

import (
	"fmt"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/depsrun"
	"github.com/spf13/cobra"
)

func newSet(runtime shared.Runtime, operations Dependencies) *cobra.Command {
	options := depsSetOptions{}
	command := &cobra.Command{
		Use:   "set <ecosystem> <dependency>@<version> [repository-path]",
		Short: "Set existing dependency references to one exact version",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(command *cobra.Command, args []string) error {
			options := options
			flags := runtime.Flags()
			if options.fleet && len(args) == 3 {
				return fmt.Errorf("repository-path cannot be used with --fleet")
			}
			validationMode, checks, err := dependencyValidationOptions(command, options)
			if err != nil {
				return err
			}
			options.validation = string(validationMode)
			options.parallelExplicit = depsBumpParallelExplicit(command)
			if command.Flags().Changed("layer") && !options.order {
				return fmt.Errorf("--layer requires --dependency-order")
			}
			target, err := deps.ParseTarget(args[0], args[1])
			if err != nil {
				return err
			}
			if options.order {
				if options.propagate {
					return fmt.Errorf("--dependency-order and --propagate cannot be used together; --propagate delegates to deps bump, which recalculates its own release waves")
				}
				if target.Ecosystem != deps.EcosystemGo {
					return fmt.Errorf("--dependency-order is supported only for the go ecosystem; %q references have no module graph", target.Ecosystem)
				}
			}
			if options.layers, err = deps.ParseLayerSelection(options.layer); err != nil {
				return err
			}
			campaign := operations.Campaign(command.ErrOrStderr(), console.Interactive(command.ErrOrStderr(), flags.NonInteractive), "deps set")
			options.campaign = campaign
			repositories, err := dependencyRepositories(command, flags, args, options, operations)
			if err != nil {
				campaign.Finish("failed")
				return err
			}
			lifecycle := dependencyOptions(flags, options, checks)
			lifecycle.Progress = campaign.Reporter()
			if options.propagate {
				if !options.fleet {
					campaign.Finish("failed")
					return fmt.Errorf("--propagate requires --fleet")
				}
				if target.Ecosystem != deps.EcosystemGo {
					campaign.Finish("failed")
					return fmt.Errorf("--propagate is supported only for the go ecosystem; it delegates to deps bump")
				}
				events := []deps.ReleaseEvent{{Dependency: target.Dependency, Version: target.Version, Source: "exact_set"}}
				return runDepsBump(flags, command, deps.EcosystemGo, events, repositories, options, lifecycle, operations)
			}
			result, err := operations.Set(commandExecutionContext(command), depsrun.SetRequest{Target: target, Repositories: repositories, Options: lifecycle, Finish: campaign.Finish})
			if err != nil {
				return err
			}
			report, runErr := result.Report, result.RunError
			if err := writeDependencyReport(command, report, options.format); err != nil {
				return err
			}
			return runErr
		},
	}
	command.Flags().BoolVar(&options.fleet, "fleet", false, "reconcile and process selected local and owned GitHub repositories")
	command.Flags().StringVar(&options.match, "match", "", "glob matched against org/repo, e.g. sneat-co/*")
	command.Flags().StringVar(&options.regex, "regex", "", "regular expression matched against org/repo")
	command.Flags().StringVar(&options.ref, "ref", "main", "base ref for operation worktrees")
	command.Flags().IntVar(&options.parallel, "parallel", 1, "maximum repositories to process concurrently")
	command.Flags().BoolVar(&options.dryRun, "dry-run", false, "inspect and report without creating worktrees or changing dependency files")
	command.Flags().BoolVar(&options.resume, "resume", false, "reuse validated operation worktrees, branches, and open pull requests")
	command.Flags().BoolVar(&options.allowDowngrade, "allow-downgrade", false, "permit a target lower than an observed semantic version")
	command.Flags().BoolVar(&options.order, "dependency-order", false, "process repositories in provider-first dependency layers instead of one batch (go only)")
	command.Flags().StringVar(&options.layer, "layer", "", "restrict --dependency-order to one layer or a range: N, N-M, or N- (default every layer)")
	command.Flags().BoolVar(&options.propagate, "propagate", false, "delegate this exact Go release event to deps bump waves (requires --fleet)")
	command.Flags().IntVar(&options.maxWaves, "max-waves", 20, "maximum recalculated dependency waves when --propagate is used")
	command.Flags().DurationVar(&options.releasePoll, "release-poll", 30*time.Second, "provider release polling interval when --propagate is used")
	command.Flags().DurationVar(&options.refreshAfter, "refresh-after", 5*time.Minute, "recheck release events older than this before a downstream build when --propagate is used (0 disables)")
	command.Flags().StringVar(&options.checks, "checks", "", "comma-separated checks: lint,test,build (default all)")
	command.Flags().StringVar(&options.validation, "validation", string(deps.ValidationModeFull), "validation mode: full, or fast with mandatory exact PR-head CI before merge or PR validation")
	command.Flags().BoolVar(&options.noVerify, "no-verify", false, "legacy explicit escape hatch that skips local verification (distinct from --validation=fast)")
	command.Flags().DurationVar(&options.timeout, "timeout", 30*time.Minute, "maximum duration per external check and CI wait (0 disables)")
	command.Flags().IntVar(&options.retry, "retry", 0, "additional attempts for failed external commands")
	command.Flags().BoolVar(&options.commit, "commit", false, "commit dependency changes on operation branches")
	command.Flags().BoolVar(&options.push, "push", false, "push operation branches; implies --commit")
	command.Flags().BoolVar(&options.pr, "pr", false, "open pull requests; implies --push and --commit")
	command.Flags().BoolVar(&options.merge, "merge", false, "wait for passing GitHub checks and merge; implies --pr, --push, and --commit")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write deps-set.md and deps-set.yaml to this directory")
	command.Flags().StringArrayVar(&options.goPrivate, "go-private", nil, "private Go module path pattern excluded from public proxy and checksum lookup (repeatable)")
	return command
}
