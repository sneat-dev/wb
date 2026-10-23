package cmddeps

import (
	"context"
	"fmt"
	"time"

	cliprogress "github.com/sneat-dev/wb/internal/cli/progress"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/depsrun"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/spf13/cobra"
)

type depsSetOptions struct {
	fleet, dryRun, resume, allowDowngrade, noVerify, propagate      bool
	commit, push, pr, merge, order                                  bool
	match, regex, ref, checks, validation, format, reportDir, layer string
	parallel, retry, maxWaves                                       int
	// parallelExplicit records whether the operator set --parallel themselves;
	// see deps.Options.ParallelExplicit for how the wave engine widens only
	// read-only pools when the flag is left at its default.
	parallelExplicit bool
	// fetchCache is deps bump's opt-in per-run fetch memoization; see
	// deps.BumpOptions.FetchCache. It is registered only on `wb deps bump` —
	// deps set has no prior discovery whose fetch could stand in for the
	// engine's own.
	fetchCache                         bool
	timeout, releasePoll, refreshAfter time.Duration
	goPrivate                          []string
	// exclude removes repositories from the run entirely; hold keeps them in
	// the run but never merges their pull request. See the flag help.
	exclude, hold []string
	// latest derives a bump campaign's seed release events from the registry
	// for the --scope globs, instead of requiring every module@version on the
	// command line. It is only registered on `wb deps bump`.
	latest bool
	// scopes are the published-module globs --latest derives from. The name
	// and glob semantics match `wb deps drift --scope` deliberately: one
	// concept, one spelling, across the deps verbs.
	scopes []string
	// scopeResolutions is what --latest actually read from the registry. It is
	// carried into the persisted report so a campaign seeded from a scope can
	// be audited afterwards, including the matched modules that published
	// nothing and therefore seeded nothing.
	scopeResolutions []deps.LatestScopeResolution
	layers           deps.LayerSelection
	campaign         *cliprogress.Campaign
}

type depsGraphOptions struct {
	fleet, open                                bool
	match, regex, ref, format, reportDir, view string
	ecosystem                                  string
	parallel, retry                            int
	timeout                                    time.Duration
	dependencies                               []string
}

type depsDriftOptions struct {
	fleet, online, failOnDrift, failOnBehind bool
	match, regex, ref, format, reportDir     string
	ecosystem                                string
	parallel, retry                          int
	timeout                                  time.Duration
	dependencies                             []string
	scopes                                   []string
	exclude                                  []string
	goPrivate                                []string
}

type depsPeersOptions struct {
	against, format string
	timeout         time.Duration
	retry           int
}

func dependencyOptions(flags shared.Flags, options depsSetOptions, checks []quality.Check) deps.Options {
	validationMode := depsrun.EffectiveValidationMode(options.validation, options.noVerify)
	return deps.Options{
		GitHubDir: flags.ProjectsRoot, Ref: options.ref, Parallel: options.parallel, ParallelExplicit: options.parallelExplicit,
		DryRun: options.dryRun, Resume: options.resume, AllowDowngrade: options.allowDowngrade,
		ValidationMode: validationMode, Verify: validationMode == deps.ValidationModeFull, Checks: checks, Timeout: options.timeout, Retry: options.retry,
		GoPrivate:           options.goPrivate,
		ExcludeRepositories: options.exclude, Hold: options.hold,
		Commit: options.commit, Push: options.push, PR: options.pr, Merge: options.merge,
		ReportDir: options.reportDir,
		Order:     options.order, Layers: options.layers,
	}
}
func dependencyValidationOptions(command *cobra.Command, options depsSetOptions) (deps.ValidationMode, []quality.Check, error) {
	validationChanged := command != nil && command.Flags().Changed("validation")
	checksChanged := command != nil && command.Flags().Changed("checks")
	if options.noVerify {
		if validationChanged {
			return "", nil, fmt.Errorf("--no-verify and --validation cannot be used together")
		}
		if checksChanged {
			return "", nil, fmt.Errorf("--no-verify and --checks cannot be used together")
		}
		return deps.ValidationModeNone, nil, nil
	}
	mode, err := deps.ParseValidationMode(options.validation)
	if err != nil {
		return "", nil, err
	}
	if mode == deps.ValidationModeFast {
		if checksChanged {
			return "", nil, fmt.Errorf("--validation=fast and --checks cannot be used together")
		}
		if !options.dryRun && !options.pr && !options.merge {
			return "", nil, fmt.Errorf("--validation=fast requires --pr or --merge so exact PR-head GitHub checks remain mandatory")
		}
		return mode, nil, nil
	}
	checks, err := quality.ParseChecks(options.checks)
	if err != nil {
		return "", nil, err
	}
	return mode, checks, nil
}
func depsBumpParallelExplicit(command *cobra.Command) bool {
	return command != nil && command.Flags().Changed("parallel")
}
func commandExecutionContext(command *cobra.Command) context.Context {
	if command != nil && command.Context() != nil {
		return command.Context()
	}
	return context.Background()
}

func dependencyRepositories(command *cobra.Command, flags shared.Flags, args []string, options depsSetOptions, operations Dependencies) ([]deps.Repository, error) {
	path := ""
	if len(args) == 3 {
		path = args[2]
	}
	var reporter = options.campaign.Reporter()
	return operations.Select(commandExecutionContext(command), depsrun.Selection{ProjectsRoot: flags.ProjectsRoot, Filter: flags.Filter, ExtraOrgs: flags.ExtraOrgs, RepositoryPath: path, Fleet: options.fleet, Match: options.match, Regex: options.regex, Parallel: options.parallel, Retry: options.retry, Timeout: options.timeout, Progress: reporter})
}
func depsBumpSeedEvents(command *cobra.Command, ecosystem deps.Ecosystem, changed []string, repositories []deps.Repository, options *depsSetOptions, lifecycle deps.Options, operations Dependencies) ([]deps.ReleaseEvent, error) {
	if options.campaign != nil {
		lifecycle.Progress = options.campaign.Reporter()
	}
	result, err := operations.Seed(commandExecutionContext(command), depsrun.SeedRequest{Ecosystem: ecosystem, Changed: changed, Scopes: options.scopes, Latest: options.latest, Repositories: repositories, Options: deps.BumpOptions{Options: lifecycle, Ecosystem: ecosystem}})
	if err != nil {
		return nil, err
	}
	options.scopeResolutions = result.Resolutions
	return result.Events, nil
}
func runDepsBump(flags shared.Flags, command *cobra.Command, ecosystem deps.Ecosystem, events []deps.ReleaseEvent, repositories []deps.Repository, options depsSetOptions, lifecycle deps.Options, operations Dependencies) error {
	lifecycle.Progress = options.campaign.Reporter()
	result, runErr := operations.Bump(commandExecutionContext(command), depsrun.BumpRequest{ProjectsRoot: flags.ProjectsRoot, ReportDir: options.reportDir, Resume: options.resume, Events: events, Repositories: repositories, ResumeParallelExplicit: depsBumpParallelExplicit(command), Options: deps.BumpOptions{Options: lifecycle, Ecosystem: ecosystem, MaxWaves: options.maxWaves, PollInterval: options.releasePoll, RefreshAfter: options.refreshAfter, FetchCache: options.fetchCache, Scopes: depsrun.DerivedScopes(options.latest, options.scopes), ScopeResolutions: options.scopeResolutions}})
	if runErr != nil {
		options.campaign.Finish("failed")
	} else {
		options.campaign.Finish("completed")
	}
	if result.Report.Operation == "" {
		return runErr
	}
	if err := writeDependencyReport(command, result.Report, options.format); err != nil {
		return err
	}
	return runErr
}
