package main

import (
	"context"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cli/cmddeps"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/depsrun"
	"github.com/sneat-dev/wb/internal/quality"
)

type depsSetOptions struct {
	fleet, dryRun, resume, allowDowngrade, noVerify          bool
	commit, push, pr, merge, order                           bool
	match, regex, ref, checks, validation, format, reportDir string
	parallel, retry, maxWaves                                int
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
	campaign         *campaignProgress
}

func newDepsCmd(inv *invocation) *cobra.Command {
	command := cmddeps.New(newCLIRuntime(inv), cmddeps.Operations(newDependencyService(), openBrowser))
	command.AddCommand(newDepsPublishCmd(inv), newDepsPropagateCmd(inv), newDepsPolicyCmd(inv), cmddeps.NewGoDirective(newCLIRuntime(inv), cmddeps.DirectiveOperations(newDependencyService())))
	return command
}
func newDependencyService() *depsrun.Service {
	return depsrun.New(depsrun.DefaultDependencies(os.Stderr))
}

func dependencyOptions(inv *invocation, options depsSetOptions, checks []quality.Check) deps.Options {
	validationMode := depsrun.EffectiveValidationMode(options.validation, options.noVerify)
	return deps.Options{
		GitHubDir: inv.projectsRoot, Ref: options.ref, Parallel: options.parallel, ParallelExplicit: options.parallelExplicit,
		DryRun: options.dryRun, Resume: options.resume, AllowDowngrade: options.allowDowngrade,
		ValidationMode: validationMode, Verify: validationMode == deps.ValidationModeFull, Checks: checks, Timeout: options.timeout, Retry: options.retry,
		GoPrivate:           options.goPrivate,
		ExcludeRepositories: options.exclude, Hold: options.hold,
		Commit: options.commit, Push: options.push, PR: options.pr, Merge: options.merge,
		ReportDir: options.reportDir,
		Order:     options.order, Layers: options.layers,
	}
}

// executeDepsBumpWithRegistryPolicy keeps composite commands on the existing
// wave engine while allowing a publication plan to prove that it did not
// consult an npm registry before a provider workflow has run.
func executeDepsBumpWithRegistryPolicy(inv *invocation, command *cobra.Command, ecosystem deps.Ecosystem, events []deps.ReleaseEvent, repositories []deps.Repository, options depsSetOptions, lifecycle deps.Options, noRegistry bool) (deps.BumpReport, string, error) {
	campaign := newCampaignProgress(command.ErrOrStderr(), console.Interactive(command.ErrOrStderr(), inv.nonInteractive), "deps bump")
	lifecycle.Progress = campaign.reporter()

	result, err := newDependencyService().Bump(commandExecutionContext(command), depsrun.BumpRequest{ProjectsRoot: inv.projectsRoot, ReportDir: options.reportDir, Resume: options.resume, Events: events, Repositories: repositories, ResumeParallelExplicit: depsBumpParallelExplicit(command), Finish: campaign.finish, Options: deps.BumpOptions{Options: lifecycle, Ecosystem: ecosystem, MaxWaves: options.maxWaves, PollInterval: options.releasePoll, RefreshAfter: options.refreshAfter, NoRegistry: noRegistry, FetchCache: options.fetchCache, Scopes: depsrun.DerivedScopes(options.latest, options.scopes), ScopeResolutions: options.scopeResolutions}})
	return result.Report, result.ReportDir, err
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

func dependencyRepositories(inv *invocation, args []string, options depsSetOptions) ([]deps.Repository, error) {
	repositoryPath := ""
	if len(args) == 3 {
		repositoryPath = args[2]
	}
	return newDependencyService().Select(context.Background(), depsrun.Selection{ProjectsRoot: inv.projectsRoot, Filter: inv.filterFlag, ExtraOrgs: inv.extraOrgs, RepositoryPath: repositoryPath, Fleet: options.fleet, Match: options.match, Regex: options.regex, Parallel: options.parallel, Retry: options.retry, Timeout: options.timeout, Progress: options.campaign.reporter()})
}
