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

func newDrift(runtime shared.Runtime, operations Dependencies) *cobra.Command {
	options := depsDriftOptions{}
	command := &cobra.Command{
		Use:   "drift [repository-path]",
		Short: "Report dependency version convergence for one repository or a fleet",
		Long: `Produce a read-only dependency convergence report for the go or npm ecosystem.

For each dependency the report distinguishes declared, selected, replaced, and
(optionally) latest-known versions. Fleet runs group each module path by the
versions found across repositories and classify the state as converged,
divergent, replaced, major-path split, behind latest, unavailable, or error.

--ecosystem=go reads every go.mod and resolves the selected version with
` + "`go list -m`" + `. --ecosystem=npm reads every package.json and
pnpm-workspace.yaml and resolves the selected version from the governing
pnpm-lock.yaml or package-lock.json — the number a build actually installs,
which a caret range on its own does not tell you.

By default the command stays offline and never labels an unqueried version as
latest. Pass --online to consult the module proxy or the npm registry. Because
an online fleet run makes one registry query per retained dependency, restrict
the question with --scope (glob, repeatable) so a run costs what it should:

    wb deps drift --fleet --ecosystem npm --online --scope '@sneat/*'

--scope uses path.Match semantics, so "*" never crosses a "/". A dependency is
retained when --scope matches it or --dependency names it exactly; with neither
flag every dependency is retained. --exclude removes whole repositories from
the run by "owner/name" glob, and the excluded slugs are listed in the report so
"clean" is never confused with "never inspected".

Pass --fail-on-drift to exit non-zero after the complete report when divergent,
replaced, or major-path-split groups are present, and --fail-on-behind to exit
non-zero when any repository provably lags a published latest version.
Inspection errors always exit non-zero after the report.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			options := options
			flags := runtime.Flags()
			campaign := operations.Campaign(command.ErrOrStderr(), console.Interactive(command.ErrOrStderr(), flags.NonInteractive), "deps drift")
			if options.fleet && len(args) == 1 {
				return fmt.Errorf("repository-path cannot be used with --fleet")
			}
			ecosystem := deps.Ecosystem(options.ecosystem)
			if ecosystem == "" {
				ecosystem = deps.EcosystemGo
			}
			switch ecosystem {
			case deps.EcosystemGo, deps.EcosystemNPM:
			default:
				return fmt.Errorf("dependency drift currently supports only the go and npm ecosystems")
			}
			repositoryArgs := []string{string(ecosystem), "drift"}
			if len(args) == 1 {
				repositoryArgs = append(repositoryArgs, args[0])
			}
			repositories, err := dependencyRepositories(command, flags, repositoryArgs, depsSetOptions{
				fleet: options.fleet, match: options.match, regex: options.regex, ref: options.ref,
				parallel: options.parallel, retry: options.retry, timeout: options.timeout,
				goPrivate: options.goPrivate, campaign: campaign,
			}, operations)
			if err != nil {
				campaign.Finish("failed")
				return err
			}
			report, err := operations.Drift(command.Context(), depsrun.DriftRequest{Repositories: repositories, Options: deps.DriftOptions{
				Ecosystem: ecosystem,
				GitHubDir: flags.ProjectsRoot, Ref: options.ref, Parallel: options.parallel,
				Timeout: options.timeout, Retry: options.retry, GoPrivate: options.goPrivate,
				Dependencies: options.dependencies, Scopes: options.scopes, ExcludeRepositories: options.exclude,
				Online: options.online, FailOnDrift: options.failOnDrift, FailOnBehind: options.failOnBehind,
				Progress: campaign.Reporter(),
			}, ReportDir: options.reportDir, Finish: campaign.Finish})
			if err != nil {
				return err
			}
			if err := writeDependencyReport(command, report, options.format); err != nil {
				return err
			}
			if deps.DriftFailedWith(report, options.failOnDrift, options.failOnBehind) {
				return runtime.ExitError(shared.ExitFindings, "dependency drift or inspection errors were reported; see the index above")
			}
			return nil
		},
	}
	command.Flags().StringVar(&options.ecosystem, "ecosystem", string(deps.EcosystemGo), "manifest ecosystem: go or npm")
	command.Flags().BoolVar(&options.fleet, "fleet", false, "inspect selected local and owned GitHub repositories under --projects-root")
	command.Flags().StringVar(&options.match, "match", "", "glob matched against org/repo, e.g. sneat-co/*")
	command.Flags().StringVar(&options.regex, "regex", "", "regular expression matched against org/repo")
	command.Flags().StringVar(&options.ref, "ref", "main", "base ref recorded in the report metadata")
	command.Flags().IntVar(&options.parallel, "parallel", 1, "maximum repositories to inspect concurrently")
	command.Flags().DurationVar(&options.timeout, "timeout", 5*time.Minute, "maximum duration per external Go command (0 disables)")
	command.Flags().IntVar(&options.retry, "retry", 0, "additional attempts for failed external commands")
	command.Flags().StringArrayVar(&options.dependencies, "dependency", nil, "exact dependency module to retain (repeatable)")
	command.Flags().StringArrayVar(&options.scopes, "scope", nil, "glob matched against a dependency path or package name, e.g. @sneat/* (repeatable)")
	command.Flags().StringArrayVar(&options.exclude, "exclude", nil, "glob matched against org/repo; matching repositories are never inspected and are listed as excluded (repeatable)")
	command.Flags().BoolVar(&options.online, "online", false, "query the module proxy or npm registry for latest versions")
	command.Flags().BoolVar(&options.failOnDrift, "fail-on-drift", false, "exit non-zero after the report when divergent, replaced, or major-path-split groups are present")
	command.Flags().BoolVar(&options.failOnBehind, "fail-on-behind", false, "exit non-zero after the report when any repository provably lags a published latest version (requires --online)")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write deps-drift.md, deps-drift.yaml, and deps-drift.json here")
	command.Flags().StringArrayVar(&options.goPrivate, "go-private", nil, "private Go module path pattern excluded from public proxy and checksum lookup (repeatable)")
	return command
}
