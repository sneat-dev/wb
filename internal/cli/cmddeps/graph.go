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

func newGraph(runtime shared.Runtime, operations Dependencies) *cobra.Command {
	options := depsGraphOptions{}
	command := &cobra.Command{
		Use:   "graph [repository-path]",
		Short: "Project dependency evidence as repository, dependency, and version graphs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			options := options
			flags := runtime.Flags()
			campaign := operations.Campaign(command.ErrOrStderr(), console.Interactive(command.ErrOrStderr(), flags.NonInteractive), "deps graph")
			if options.fleet && len(args) == 1 {
				return fmt.Errorf("repository-path cannot be used with --fleet")
			}
			ecosystem := deps.Ecosystem(options.ecosystem)
			switch ecosystem {
			case deps.EcosystemGo, deps.EcosystemNPM:
			default:
				return fmt.Errorf("dependency graph currently supports only the go and npm ecosystems")
			}
			view, err := deps.ParseGraphView(options.view)
			if err != nil {
				return err
			}
			repositoryArgs := []string{options.ecosystem, "graph"}
			if len(args) == 1 {
				repositoryArgs = append(repositoryArgs, args[0])
			}
			repositories, err := dependencyRepositories(command, flags, repositoryArgs, depsSetOptions{
				fleet: options.fleet, match: options.match, regex: options.regex, ref: options.ref,
				parallel: options.parallel, retry: options.retry, timeout: options.timeout,
				campaign: campaign,
			}, operations)
			if err != nil {
				campaign.Finish("failed")
				return err
			}
			result, err := operations.Graph(command.Context(), depsrun.GraphRequest{Repositories: repositories, Options: deps.GraphOptions{
				Ecosystem: ecosystem, GitHubDir: flags.ProjectsRoot, Ref: options.ref,
				Parallel: options.parallel, Timeout: options.timeout, Retry: options.retry,
				Dependencies: options.dependencies,
				Progress:     campaign.Reporter(),
			}, View: view, ReportDir: options.reportDir, Finish: campaign.Finish})
			if err != nil {
				return err
			}
			graph, paths := result.Graph, result.Paths
			contents, err := graph.Output(options.format, view)
			if err != nil {
				return err
			}
			if _, err := command.OutOrStdout().Write(contents); err != nil {
				return err
			}
			if options.open {
				if err := operations.OpenBrowser(paths.HTML); err != nil {
					return fmt.Errorf("reports were written; open %s manually: %w", paths.HTML, err)
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&options.ecosystem, "ecosystem", string(deps.EcosystemGo), "manifest ecosystem: go or npm")
	command.Flags().BoolVar(&options.fleet, "fleet", false, "reconcile and inspect selected local and owned GitHub repositories")
	command.Flags().StringVar(&options.match, "match", "", "glob matched against org/repo, e.g. dal-go/*")
	command.Flags().StringVar(&options.regex, "regex", "", "regular expression matched against org/repo")
	command.Flags().StringVar(&options.ref, "ref", "main", "remote ref whose manifests are inspected")
	command.Flags().IntVar(&options.parallel, "parallel", 1, "maximum repositories to inspect concurrently")
	command.Flags().DurationVar(&options.timeout, "timeout", 5*time.Minute, "maximum duration per fetch or inspection command (0 disables)")
	command.Flags().IntVar(&options.retry, "retry", 0, "additional attempts for failed external commands")
	command.Flags().StringArrayVar(&options.dependencies, "dependency", nil, "exact dependency module to retain (repeatable)")
	command.Flags().StringVar(&options.view, "view", string(deps.GraphViewRepositories), "default graph view: repos, dependencies, or selections")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, json, svg, or html")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write deps-graph Markdown, YAML, JSON, SVG, and HTML here")
	command.Flags().BoolVar(&options.open, "open", false, "open the self-contained HTML report in the default browser after writing it")
	return command
}
