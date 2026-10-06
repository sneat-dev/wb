package cmddeps

import (
	"context"
	"fmt"
	"io"
	"time"

	cliprogress "github.com/sneat-dev/wb/internal/cli/progress"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/depsrun"
	"github.com/spf13/cobra"
)

type PublicationDependencies struct {
	Run      func(context.Context, depsrun.PublicationRequest, depsrun.PublicationCallbacks) error
	Campaign func(io.Writer, bool, string) *cliprogress.Campaign
}

func PublicationOperations(service *depsrun.PublicationService) PublicationDependencies {
	return PublicationDependencies{Run: service.Run, Campaign: cliprogress.NewCampaign}
}

type npmPublishOptions struct {
	depsSetOptions
	repositories, workflows, packages, versions, workflowInputs []string
	registry                                                    string
	workflowPoll                                                time.Duration
	apply                                                       bool
}

func NewPublish(runtime shared.Runtime, operations PublicationDependencies) *cobra.Command {
	command := &cobra.Command{
		Use:     "publish",
		Aliases: []string{"release"},
		Short:   "Publish approved npm packages through repository workflows, verify the registry, and propagate dependency waves",
	}
	command.AddCommand(newNpmPublish(runtime, operations))
	return command
}
func newNpmPublish(runtime shared.Runtime, operations PublicationDependencies) *cobra.Command {
	options := npmPublishOptions{}
	command := &cobra.Command{
		Use:   "npm",
		Short: "Publish approved npm packages through repository workflows, verify the registry, and propagate dependency waves",
		Long: `Publishes explicitly named npm package releases through the owning
repository's GitHub Actions workflow. The workflow remains the only publisher:
WB never accepts an npm token or runs npm publish. The default is a dry-run
plan; --apply dispatches each exact repository/workflow/package/version tuple,
waits for its exact workflow run and head, verifies the requested version in
the npm registry, then hands the confirmed events to the same recalculated
deps bump engine used by ` + "`wb deps bump npm`" + `.

A plan validates the explicit publication tuples and invokes the existing
dependency-wave engine in dry-run mode. This preserves real fleet findings
(such as duplicate declarations) and a durable wave report below
` + "`<report-dir>/plan`" + `, but it never dispatches a GitHub Actions workflow,
queries the npm registry, or changes downstream dependency files.

Use --resume with the same tuples and --report-dir after a workflow or registry
failure. A receipt that already has a dispatch timestamp is never dispatched
again. --merge is an independent explicit opt-in for downstream dependency PR
publication; without it the confirmed events are passed to deps bump in
dry-run mode.

Each repeatable --repo, --workflow, --package, and --version flag contributes
one aligned tuple. Workflow inputs are tuple-scoped: use
--workflow-input INDEX:KEY=VALUE (zero-based INDEX), for example
--workflow-input 0:package=runtime --workflow-input 1:package=ui. A bare
KEY=VALUE is accepted only for a one-tuple command and belongs to tuple 0.
Inputs are passed in deterministic key order to their repository-owned
workflow dispatch only.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			options.parallelExplicit = command.Flags().Changed("parallel")
			flags := runtime.Flags()
			request := depsrun.PublicationRequest{Repositories: options.repositories, Workflows: options.workflows, Packages: options.packages, Versions: options.versions, WorkflowInputs: options.workflowInputs, Registry: options.registry, Checks: options.checks, NoVerify: options.noVerify, Apply: options.apply, WorkflowPoll: options.workflowPoll, ReleasePoll: options.releasePoll, RefreshAfter: options.refreshAfter, MaxWaves: options.maxWaves, Lifecycle: dependencyOptions(flags, options.depsSetOptions, nil), Selection: depsrun.Selection{ProjectsRoot: flags.ProjectsRoot, Filter: flags.Filter, ExtraOrgs: flags.ExtraOrgs, Fleet: options.fleet, Match: options.match, Regex: options.regex, Parallel: options.parallel, Retry: options.retry, Timeout: options.timeout}}
			return operations.Run(command.Context(), request, depsrun.PublicationCallbacks{ValidateOutput: func() error { return validateNpmPublishFormat(options.format) }, Emit: func(output depsrun.PublicationOutput) error {
				return writeNpmPublishOutput(command.OutOrStdout(), output, options.format)
			}, StartProgress: func(label string) depsrun.PublicationProgress {
				campaign := operations.Campaign(command.ErrOrStderr(), console.Interactive(command.ErrOrStderr(), runtime.Flags().NonInteractive), label)
				return depsrun.PublicationProgress{Reporter: campaign.Reporter(), Finish: campaign.Finish}
			}})
		},
	}
	command.Flags().StringArrayVar(&options.repositories, "repo", nil, "provider GitHub repository owner/name (repeatable, aligned with --workflow/--package/--version)")
	command.Flags().StringArrayVar(&options.workflows, "workflow", nil, "repository-owned release workflow file ending .yml or .yaml (repeatable, aligned)")
	command.Flags().StringArrayVar(&options.packages, "package", nil, "exact npm package name (repeatable, aligned)")
	command.Flags().StringArrayVar(&options.versions, "version", nil, "exact npm semver without a v prefix (repeatable, aligned)")
	command.Flags().StringArrayVar(&options.workflowInputs, "workflow-input", nil, "tuple-scoped workflow_dispatch input INDEX:KEY=VALUE (repeatable; bare KEY=VALUE only for one tuple)")
	command.Flags().StringVar(&options.registry, "registry", "https://registry.npmjs.org", "npm registry URL used only for read-only release evidence")
	command.Flags().StringVar(&options.ref, "ref", "main", "provider branch whose exact head is dispatched")
	command.Flags().BoolVar(&options.fleet, "fleet", false, "select downstream dependency consumers from the local and owned repository fleet")
	command.Flags().StringVar(&options.match, "match", "", "glob matched against downstream org/repo, e.g. sneat-co/*")
	command.Flags().StringVar(&options.regex, "regex", "", "regular expression matched against downstream org/repo")
	command.Flags().IntVar(&options.parallel, "parallel", 1, "maximum downstream repositories or wave observations concurrently")
	command.Flags().IntVar(&options.maxWaves, "max-waves", 20, "maximum recalculated downstream dependency waves")
	command.Flags().DurationVar(&options.workflowPoll, "workflow-poll", 30*time.Second, "interval between exact GitHub workflow run observations")
	command.Flags().DurationVar(&options.releasePoll, "release-poll", 30*time.Second, "interval between downstream published-release observations")
	command.Flags().DurationVar(&options.refreshAfter, "refresh-after", 5*time.Minute, "recheck stale downstream release events before CI (0 disables)")
	command.Flags().BoolVar(&options.apply, "apply", false, "dispatch repository-owned workflows and perform registry verification")
	command.Flags().BoolVar(&options.dryRun, "dry-run", false, "plan publication and downstream waves without workflow dispatch, npm registry queries, or dependency-file changes")
	command.Flags().BoolVar(&options.resume, "resume", false, "resume the persisted publication and dependency-wave reports without redispatching receipted workflows")
	command.Flags().BoolVar(&options.allowDowngrade, "allow-downgrade", false, "permit downstream dependency references lower than an observed version")
	command.Flags().StringVar(&options.checks, "checks", "", "downstream checks: lint,test,build (default all)")
	command.Flags().BoolVar(&options.noVerify, "no-verify", false, "explicitly skip downstream local verification")
	command.Flags().DurationVar(&options.timeout, "timeout", 30*time.Minute, "maximum duration per workflow, registry, downstream check, or CI wait")
	command.Flags().IntVar(&options.retry, "retry", 0, "additional attempts for eligible downstream external commands")
	command.Flags().BoolVar(&options.commit, "commit", false, "commit downstream dependency changes (requires --merge for this command)")
	command.Flags().BoolVar(&options.push, "push", false, "push downstream dependency branches (requires --merge for this command)")
	command.Flags().BoolVar(&options.pr, "pr", false, "open downstream dependency pull requests (requires --merge for this command)")
	command.Flags().BoolVar(&options.merge, "merge", false, "publish and merge passing downstream dependency waves after exact CI evidence")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "stable directory for npm-publish.yaml/json and apply deps-bump reports (plans use plan/deps-bump)")
	return command
}
func validateNpmPublishFormat(format string) error {
	switch format {
	case "markdown", "yaml", "json":
		return nil
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}
