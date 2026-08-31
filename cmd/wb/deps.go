package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/wbhome"
)

type depsSetOptions struct {
	fleet, dryRun, resume, allowDowngrade, noVerify, propagate bool
	commit, push, pr, merge, order                             bool
	match, regex, ref, checks, format, reportDir, layer        string
	parallel, retry, maxWaves                                  int
	timeout, releasePoll, refreshAfter                         time.Duration
	goPrivate                                                  []string
	layers                                                     deps.LayerSelection
	campaign                                                   *campaignProgress
}

func newDepsCmd() *cobra.Command {
	command := &cobra.Command{
		Use:     "deps",
		Aliases: []string{"dep"},
		Short:   "Inspect and coordinate dependencies across repositories",
	}
	command.AddCommand(newDepsSetCmd())
	command.AddCommand(newDepsBumpCmd())
	command.AddCommand(newDepsPublishCmd())
	command.AddCommand(newDepsGraphCmd())
	command.AddCommand(newDepsDriftCmd())
	command.AddCommand(newDepsPolicyCmd())
	command.AddCommand(newDepsGoDirectiveCmd())
	return command
}

type depsGraphOptions struct {
	fleet, open                                bool
	match, regex, ref, format, reportDir, view string
	ecosystem                                  string
	parallel, retry                            int
	timeout                                    time.Duration
	dependencies                               []string
}

func newDepsGraphCmd() *cobra.Command {
	options := depsGraphOptions{}
	command := &cobra.Command{
		Use:   "graph [repository-path]",
		Short: "Project dependency evidence as repository, dependency, and version graphs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			campaign := newCampaignProgress(command.ErrOrStderr(), console.Interactive(command.ErrOrStderr(), nonInteractive), "deps graph")
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
			repositories, err := dependencyRepositories(repositoryArgs, depsSetOptions{
				fleet: options.fleet, match: options.match, regex: options.regex, ref: options.ref,
				parallel: options.parallel, retry: options.retry, timeout: options.timeout,
				campaign: campaign,
			})
			if err != nil {
				campaign.finish("failed")
				return err
			}
			graph, err := deps.BuildGraph(command.Context(), repositories, deps.GraphOptions{
				Ecosystem: ecosystem, GitHubDir: projectsRoot, Ref: options.ref,
				Parallel: options.parallel, Timeout: options.timeout, Retry: options.retry,
				Dependencies: options.dependencies,
				Progress:     campaign.reporter(),
			})
			if err != nil {
				campaign.finish("failed")
				return err
			}
			campaign.finish("completed")
			reportDirectory := options.reportDir
			if reportDirectory == "" {
				home, err := wbhome.EnsureRoot(projectsRoot)
				if err != nil {
					return err
				}
				reportDirectory = filepath.Join(home, "reports", "deps-graph-"+options.ecosystem)
			}
			paths, err := deps.WriteGraphReports(reportDirectory, graph, view)
			if err != nil {
				return err
			}
			contents, err := graph.Output(options.format, view)
			if err != nil {
				return err
			}
			if _, err := command.OutOrStdout().Write(contents); err != nil {
				return err
			}
			if options.open {
				if err := openBrowser(paths.HTML); err != nil {
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

type depsDriftOptions struct {
	fleet, online, failOnDrift           bool
	match, regex, ref, format, reportDir string
	ecosystem                            string
	parallel, retry                      int
	timeout                              time.Duration
	dependencies                         []string
	goPrivate                            []string
}

func newDepsDriftCmd() *cobra.Command {
	options := depsDriftOptions{}
	command := &cobra.Command{
		Use:   "drift [repository-path]",
		Short: "Report dependency version convergence for one repository or a fleet",
		Long: `Produce a read-only Go dependency convergence report.

For each dependency the report distinguishes declared, selected, replaced, and
(optionally) latest-known versions. Fleet runs group each module path by the
versions found across repositories and classify the state as converged,
divergent, replaced, major-path split, unavailable, or error.

By default the command stays offline and never labels an unqueried version as
latest. Pass --online to consult the module proxy. Pass --fail-on-drift to exit
non-zero after the complete report when divergent, replaced, or major-path-split
groups are present. Inspection errors always exit non-zero after the report.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			campaign := newCampaignProgress(command.ErrOrStderr(), console.Interactive(command.ErrOrStderr(), nonInteractive), "deps drift")
			if options.fleet && len(args) == 1 {
				return fmt.Errorf("repository-path cannot be used with --fleet")
			}
			ecosystem := deps.Ecosystem(options.ecosystem)
			if ecosystem == "" {
				ecosystem = deps.EcosystemGo
			}
			if ecosystem != deps.EcosystemGo {
				return fmt.Errorf("dependency drift currently supports only the go ecosystem")
			}
			repositoryArgs := []string{string(ecosystem), "drift"}
			if len(args) == 1 {
				repositoryArgs = append(repositoryArgs, args[0])
			}
			repositories, err := dependencyRepositories(repositoryArgs, depsSetOptions{
				fleet: options.fleet, match: options.match, regex: options.regex, ref: options.ref,
				parallel: options.parallel, retry: options.retry, timeout: options.timeout,
				goPrivate: options.goPrivate, campaign: campaign,
			})
			if err != nil {
				campaign.finish("failed")
				return err
			}
			report, err := deps.AnalyzeDrift(command.Context(), repositories, deps.DriftOptions{
				GitHubDir: projectsRoot, Ref: options.ref, Parallel: options.parallel,
				Timeout: options.timeout, Retry: options.retry, GoPrivate: options.goPrivate,
				Dependencies: options.dependencies, Online: options.online, FailOnDrift: options.failOnDrift,
				Progress: campaign.reporter(),
			})
			if err != nil {
				campaign.finish("failed")
				return err
			}
			if deps.DriftFailed(report, options.failOnDrift) {
				campaign.finish("completed with findings")
			} else {
				campaign.finish("completed")
			}
			reportDirectory := options.reportDir
			if reportDirectory == "" {
				home, homeErr := wbhome.EnsureRoot(projectsRoot)
				if homeErr != nil {
					return homeErr
				}
				reportDirectory = filepath.Join(home, "reports", "deps-drift")
			}
			if err := deps.WriteDriftReports(reportDirectory, report); err != nil {
				return err
			}
			if err := writeDepsDriftReport(command, report, options.format); err != nil {
				return err
			}
			if deps.DriftFailed(report, options.failOnDrift) {
				return &exitError{
					code:    exitFindings,
					message: "dependency drift or inspection errors were reported; see the index above",
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&options.ecosystem, "ecosystem", string(deps.EcosystemGo), "manifest ecosystem: go")
	command.Flags().BoolVar(&options.fleet, "fleet", false, "inspect selected local and owned GitHub repositories under --projects-root")
	command.Flags().StringVar(&options.match, "match", "", "glob matched against org/repo, e.g. sneat-co/*")
	command.Flags().StringVar(&options.regex, "regex", "", "regular expression matched against org/repo")
	command.Flags().StringVar(&options.ref, "ref", "main", "base ref recorded in the report metadata")
	command.Flags().IntVar(&options.parallel, "parallel", 1, "maximum repositories to inspect concurrently")
	command.Flags().DurationVar(&options.timeout, "timeout", 5*time.Minute, "maximum duration per external Go command (0 disables)")
	command.Flags().IntVar(&options.retry, "retry", 0, "additional attempts for failed external commands")
	command.Flags().StringArrayVar(&options.dependencies, "dependency", nil, "exact dependency module to retain (repeatable)")
	command.Flags().BoolVar(&options.online, "online", false, "query the module proxy for latest versions")
	command.Flags().BoolVar(&options.failOnDrift, "fail-on-drift", false, "exit non-zero after the report when divergent, replaced, or major-path-split groups are present")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write deps-drift.md, deps-drift.yaml, and deps-drift.json here")
	command.Flags().StringArrayVar(&options.goPrivate, "go-private", nil, "private Go module path pattern excluded from public proxy and checksum lookup (repeatable)")
	return command
}

func writeDepsDriftReport(command *cobra.Command, report deps.DriftReport, format string) error {
	switch format {
	case "markdown":
		_, err := command.OutOrStdout().Write([]byte(report.Markdown()))
		return err
	case "yaml":
		raw, err := report.YAML()
		if err != nil {
			return err
		}
		_, err = command.OutOrStdout().Write(raw)
		return err
	case "json":
		raw, err := report.JSON()
		if err != nil {
			return err
		}
		_, err = command.OutOrStdout().Write(raw)
		return err
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}

func newDepsSetCmd() *cobra.Command {
	options := depsSetOptions{}
	command := &cobra.Command{
		Use:   "set <ecosystem> <dependency>@<version> [repository-path]",
		Short: "Set existing dependency references to one exact version",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(command *cobra.Command, args []string) error {
			if options.fleet && len(args) == 3 {
				return fmt.Errorf("repository-path cannot be used with --fleet")
			}
			if options.noVerify && command.Flags().Changed("checks") {
				return fmt.Errorf("--no-verify and --checks cannot be used together")
			}
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
			checks, err := quality.ParseChecks(options.checks)
			if err != nil {
				return err
			}
			campaign := newCampaignProgress(command.ErrOrStderr(), console.Interactive(command.ErrOrStderr(), nonInteractive), "deps set")
			options.campaign = campaign
			repositories, err := dependencyRepositories(args, options)
			if err != nil {
				campaign.finish("failed")
				return err
			}
			lifecycle := dependencyOptions(options, checks)
			lifecycle.Progress = campaign.reporter()
			if options.propagate {
				if !options.fleet {
					return fmt.Errorf("--propagate requires --fleet")
				}
				if target.Ecosystem != deps.EcosystemGo {
					return fmt.Errorf("--propagate is supported only for the go ecosystem; it delegates to deps bump")
				}
				events := []deps.ReleaseEvent{{Dependency: target.Dependency, Version: target.Version, Source: "exact_set"}}
				return runDepsBump(command, deps.EcosystemGo, events, repositories, options, lifecycle)
			}
			report, runErr := deps.Run(commandExecutionContext(command), target, repositories, lifecycle)
			if runErr != nil {
				campaign.finish("failed")
			} else {
				campaign.finish("completed")
			}
			reportDirectory := options.reportDir
			if reportDirectory == "" && report.Operation != "" {
				home, homeErr := wbhome.EnsureRoot(projectsRoot)
				if homeErr != nil {
					return homeErr
				}
				reportDirectory = filepath.Join(home, "reports", report.Operation)
			}
			if reportDirectory != "" {
				if err := deps.WriteReports(reportDirectory, report); err != nil {
					return err
				}
			}
			if err := writeDepsSetReport(command, report, options.format); err != nil {
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
	command.Flags().BoolVar(&options.noVerify, "no-verify", false, "explicitly skip local verification")
	command.Flags().DurationVar(&options.timeout, "timeout", 30*time.Minute, "maximum duration per external check and CI wait (0 disables)")
	command.Flags().IntVar(&options.retry, "retry", 0, "additional attempts for failed external commands")
	command.Flags().BoolVar(&options.commit, "commit", false, "commit verified changes on operation branches")
	command.Flags().BoolVar(&options.push, "push", false, "push operation branches; implies --commit")
	command.Flags().BoolVar(&options.pr, "pr", false, "open pull requests; implies --push and --commit")
	command.Flags().BoolVar(&options.merge, "merge", false, "wait for passing GitHub checks and merge; implies --pr, --push, and --commit")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write deps-set.md and deps-set.yaml to this directory")
	command.Flags().StringArrayVar(&options.goPrivate, "go-private", nil, "private Go module path pattern excluded from public proxy and checksum lookup (repeatable)")
	return command
}

func newDepsBumpCmd() *cobra.Command {
	options := depsSetOptions{}
	var changed []string
	command := &cobra.Command{
		Use:   "bump <ecosystem>",
		Short: "Propagate published dependency versions through recalculated waves",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			ecosystem := deps.Ecosystem(args[0])
			switch ecosystem {
			case deps.EcosystemGo, deps.EcosystemNPM:
			default:
				return fmt.Errorf("dependency waves currently support only the go and npm ecosystems")
			}
			if !options.fleet {
				return fmt.Errorf("deps bump requires --fleet")
			}
			if options.noVerify && command.Flags().Changed("checks") {
				return fmt.Errorf("--no-verify and --checks cannot be used together")
			}
			events, err := parseReleaseEvents(ecosystem, changed)
			if err != nil {
				return err
			}
			checks, err := quality.ParseChecks(options.checks)
			if err != nil {
				return err
			}
			campaign := newCampaignProgress(command.ErrOrStderr(), console.Interactive(command.ErrOrStderr(), nonInteractive), "deps bump")
			options.campaign = campaign
			repositories, err := dependencyRepositories([]string{args[0], "events"}, options)
			if err != nil {
				campaign.finish("failed")
				return err
			}
			return runDepsBump(command, ecosystem, events, repositories, options, dependencyOptions(options, checks))
		},
	}
	command.Flags().StringArrayVar(&changed, "changed", nil, "published module@version release event (repeatable)")
	command.Flags().BoolVar(&options.fleet, "fleet", false, "reconcile and process selected local and owned GitHub repositories")
	command.Flags().StringVar(&options.match, "match", "", "glob matched against org/repo, e.g. sneat-co/*")
	command.Flags().StringVar(&options.regex, "regex", "", "regular expression matched against org/repo")
	command.Flags().StringVar(&options.ref, "ref", "main", "base ref for operation worktrees")
	command.Flags().IntVar(&options.parallel, "parallel", 1, "maximum repositories or release observations to process concurrently")
	command.Flags().IntVar(&options.maxWaves, "max-waves", 20, "maximum recalculated dependency waves")
	command.Flags().DurationVar(&options.releasePoll, "release-poll", 30*time.Second, "interval between provider release observations")
	command.Flags().DurationVar(&options.refreshAfter, "refresh-after", 5*time.Minute, "recheck release events older than this before starting a downstream build (0 disables)")
	command.Flags().BoolVar(&options.dryRun, "dry-run", false, "inspect the first wave without creating worktrees or changing dependency files")
	command.Flags().BoolVar(&options.resume, "resume", false, "reuse validated wave worktrees, branches, PRs, and report state")
	command.Flags().BoolVar(&options.allowDowngrade, "allow-downgrade", false, "permit a release event lower than an observed semantic version")
	command.Flags().StringVar(&options.checks, "checks", "", "comma-separated checks: lint,test,build (default all)")
	command.Flags().BoolVar(&options.noVerify, "no-verify", false, "explicitly skip local verification")
	command.Flags().DurationVar(&options.timeout, "timeout", 30*time.Minute, "maximum duration per external check, CI wait, or release wait (0 disables)")
	command.Flags().IntVar(&options.retry, "retry", 0, "additional attempts for failed external commands")
	command.Flags().BoolVar(&options.commit, "commit", false, "commit verified changes on wave branches")
	command.Flags().BoolVar(&options.push, "push", false, "push wave branches; implies --commit")
	command.Flags().BoolVar(&options.pr, "pr", false, "open pull requests; implies --push and --commit")
	command.Flags().BoolVar(&options.merge, "merge", false, "merge passing PRs and observe releases; implies --pr, --push, and --commit")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write deps-bump.md and deps-bump.yaml to this directory")
	command.Flags().StringArrayVar(&options.goPrivate, "go-private", nil, "private Go module path pattern excluded from public proxy and checksum lookup (repeatable)")
	return command
}

func dependencyOptions(options depsSetOptions, checks []quality.Check) deps.Options {
	return deps.Options{
		GitHubDir: projectsRoot, Ref: options.ref, Parallel: options.parallel,
		DryRun: options.dryRun, Resume: options.resume, AllowDowngrade: options.allowDowngrade,
		Verify: !options.noVerify, Checks: checks, Timeout: options.timeout, Retry: options.retry,
		GoPrivate: options.goPrivate,
		Commit:    options.commit, Push: options.push, PR: options.pr, Merge: options.merge,
		ReportDir: options.reportDir,
		Order:     options.order, Layers: options.layers,
	}
}

func parseReleaseEvents(ecosystem deps.Ecosystem, values []string) ([]deps.ReleaseEvent, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("at least one --changed module@version event is required")
	}
	events := make([]deps.ReleaseEvent, 0, len(values))
	for _, value := range values {
		target, err := deps.ParseTarget(string(ecosystem), value)
		if err != nil {
			return nil, err
		}
		events = append(events, deps.ReleaseEvent{Dependency: target.Dependency, Version: target.Version, Source: "explicit"})
	}
	return events, nil
}

func runDepsBump(command *cobra.Command, ecosystem deps.Ecosystem, events []deps.ReleaseEvent, repositories []deps.Repository, options depsSetOptions, lifecycle deps.Options) error {
	report, _, runErr := executeDepsBump(command, ecosystem, events, repositories, options, lifecycle)
	if options.campaign != nil {
		if runErr != nil {
			options.campaign.finish("failed")
		} else {
			options.campaign.finish("completed")
		}
	}
	if report.Operation == "" {
		return runErr
	}
	if err := writeDepsBumpReport(command, report, options.format); err != nil {
		return err
	}
	return runErr
}

// executeDepsBump runs the shared persisted wave engine without committing its
// report to stdout. Composite release commands use this seam to embed the
// exact same BumpReport alongside provider publication receipts rather than
// cloning or reimplementing wave orchestration.
func executeDepsBump(command *cobra.Command, ecosystem deps.Ecosystem, events []deps.ReleaseEvent, repositories []deps.Repository, options depsSetOptions, lifecycle deps.Options) (deps.BumpReport, string, error) {
	return executeDepsBumpWithRegistryPolicy(command, ecosystem, events, repositories, options, lifecycle, false)
}

// executeDepsBumpWithRegistryPolicy keeps composite commands on the existing
// wave engine while allowing a publication plan to prove that it did not
// consult an npm registry before a provider workflow has run.
func executeDepsBumpWithRegistryPolicy(command *cobra.Command, ecosystem deps.Ecosystem, events []deps.ReleaseEvent, repositories []deps.Repository, options depsSetOptions, lifecycle deps.Options, noRegistry bool) (deps.BumpReport, string, error) {
	campaign := options.campaign
	ownedCampaign := false
	if campaign == nil {
		campaign = newCampaignProgress(command.ErrOrStderr(), console.Interactive(command.ErrOrStderr(), nonInteractive), "deps bump")
		ownedCampaign = true
	}
	lifecycle.Progress = campaign.reporter()
	operation := deps.BumpOperationIDFor(ecosystem, events)
	reportDirectory := options.reportDir
	if reportDirectory == "" {
		home, err := wbhome.EnsureRoot(projectsRoot)
		if err != nil {
			return deps.BumpReport{}, "", err
		}
		reportDirectory = filepath.Join(home, "reports", operation)
	}
	var previous *deps.BumpReport
	if options.resume {
		if loaded, err := deps.LoadBumpReport(reportDirectory); err == nil {
			lifecycle, loaded, err = resolveDepsBumpResumeParallel(lifecycle, loaded, depsBumpParallelExplicit(command))
			if err != nil {
				return deps.BumpReport{}, reportDirectory, err
			}
			previous = &loaded
		} else {
			if os.IsNotExist(err) {
				return deps.BumpReport{}, reportDirectory, fmt.Errorf("--resume requires %s: %w", filepath.Join(reportDirectory, "deps-bump.yaml"), err)
			}
			return deps.BumpReport{}, reportDirectory, err
		}
	}
	bumpOptions := deps.BumpOptions{
		Options: lifecycle, Ecosystem: ecosystem, MaxWaves: options.maxWaves, PollInterval: options.releasePoll, RefreshAfter: options.refreshAfter,
		Previous: previous, NoRegistry: noRegistry,
		Persist: func(report deps.BumpReport) error { return deps.WriteBumpReports(reportDirectory, report) },
	}
	report, runErr := deps.RunBump(commandExecutionContext(command), events, repositories, bumpOptions)
	if ownedCampaign {
		if runErr != nil {
			campaign.finish("failed")
		} else {
			campaign.finish("completed")
		}
	}
	if report.Operation == "" {
		return report, reportDirectory, runErr
	}
	if err := deps.WriteBumpReports(reportDirectory, report); err != nil {
		return report, reportDirectory, err
	}
	return report, reportDirectory, runErr
}

func depsBumpParallelExplicit(command *cobra.Command) bool {
	return command != nil && command.Flags().Changed("parallel")
}

// resolveDepsBumpResumeParallel keeps campaign identity from the persisted
// report while treating concurrency as an intentional live-runtime override.
func resolveDepsBumpResumeParallel(lifecycle deps.Options, report deps.BumpReport, explicit bool) (deps.Options, deps.BumpReport, error) {
	if explicit {
		// Parallelism bounds only the live worker pool. It neither selects
		// repositories nor changes the campaign's identity, so an operator may
		// safely raise or lower it while resuming.
		report.Parallel = lifecycle.Parallel
		return lifecycle, report, nil
	}
	if report.Parallel < 1 {
		return deps.Options{}, deps.BumpReport{}, fmt.Errorf("resume report has invalid parallelism %d", report.Parallel)
	}
	lifecycle.Parallel = report.Parallel
	return lifecycle, report, nil
}

func commandExecutionContext(command *cobra.Command) context.Context {
	if command != nil && command.Context() != nil {
		return command.Context()
	}
	return context.Background()
}

func dependencyRepositories(args []string, options depsSetOptions) ([]deps.Repository, error) {
	if options.parallel < 1 {
		return nil, fmt.Errorf("parallelism must be at least 1")
	}
	if options.retry < 0 {
		return nil, fmt.Errorf("retry count must not be negative")
	}
	if options.timeout < 0 {
		return nil, fmt.Errorf("timeout must not be negative")
	}
	expression, err := compileDependencyRegex(options.regex)
	if err != nil {
		return nil, err
	}
	if options.match != "" {
		if _, err := path.Match(options.match, ""); err != nil {
			return nil, fmt.Errorf("invalid --match: %w", err)
		}
	}
	reporter := progress.Reporter(nil)
	if options.campaign != nil {
		reporter = options.campaign.reporter()
	}
	progress.Report(reporter, progress.Event{Operation: "deps", Phase: "select_repositories", State: progress.Waiting})
	if !options.fleet {
		repositoryPath := "."
		if len(args) == 3 {
			repositoryPath = args[2]
		}
		absolute, err := filepath.Abs(repositoryPath)
		if err != nil {
			return nil, err
		}
		slug, cloneURL, err := repositoryIdentity(absolute, projectsRoot)
		if err != nil {
			return nil, err
		}
		if !matchesDependencyRepository(slug, options.match, expression) {
			return nil, fmt.Errorf("repository %s does not match selected filters", slug)
		}
		if filterFlag != "" && !strings.Contains(slug, filterFlag) {
			return nil, fmt.Errorf("repository %s does not match --filter %q", slug, filterFlag)
		}
		progress.Report(reporter, progress.Event{Operation: "deps", Phase: "select_repositories", Repository: slug, State: progress.Completed, Completed: 1, Total: 1})
		return []deps.Repository{{Slug: slug, Path: absolute, CloneURL: cloneURL}}, nil
	}
	selected, err := fleet(projectsRoot, filterFlag, func() []string { return fleetOwners(extraOrgs) })
	if err != nil {
		return nil, err
	}
	repositories := make([]deps.Repository, 0, len(selected))
	for _, repository := range selected {
		if !matchesDependencyRepository(repository.Slug(), options.match, expression) {
			continue
		}
		repositories = append(repositories, deps.Repository{
			Slug: repository.Slug(), Path: repository.Path, CloneURL: repository.CloneURL, Archived: repository.Archived,
		})
	}
	sort.Slice(repositories, func(i, j int) bool { return repositories[i].Slug < repositories[j].Slug })
	if len(repositories) == 0 {
		return nil, fmt.Errorf("no repositories match the selected fleet filters")
	}
	progress.Report(reporter, progress.Event{Operation: "deps", Phase: "select_repositories", State: progress.Completed, Completed: len(repositories), Total: len(repositories)})
	return repositories, nil
}

func compileDependencyRegex(value string) (*regexp.Regexp, error) {
	if value == "" {
		return nil, nil
	}
	expression, err := regexp.Compile(value)
	if err != nil {
		return nil, fmt.Errorf("invalid --regex: %w", err)
	}
	return expression, nil
}

func matchesDependencyRepository(slug, glob string, expression *regexp.Regexp) bool {
	if glob != "" {
		matched, err := path.Match(glob, slug)
		if err != nil || !matched {
			return false
		}
	}
	return expression == nil || expression.MatchString(slug)
}

func repositoryIdentity(repositoryPath, root string) (string, string, error) {
	output, err := exec.Command("git", "-C", repositoryPath, "remote", "get-url", "origin").Output()
	if err == nil {
		remote := strings.TrimSpace(string(output))
		if slug := githubSlug(remote); slug != "" {
			return slug, remote, nil
		}
	}
	relative, relErr := filepath.Rel(root, repositoryPath)
	if relErr == nil {
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if len(parts) == 2 && parts[0] != ".." && parts[0] != "." {
			return parts[0] + "/" + parts[1], "", nil
		}
	}
	return "", "", fmt.Errorf("cannot determine GitHub owner/repository identity for %s", repositoryPath)
}

func githubSlug(remote string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	if strings.HasPrefix(trimmed, "git@github.com:") {
		return strings.TrimPrefix(trimmed, "git@github.com:")
	}
	parsed, err := url.Parse(trimmed)
	if err == nil && strings.EqualFold(parsed.Hostname(), "github.com") {
		return strings.TrimPrefix(parsed.Path, "/")
	}
	return ""
}

func writeDepsSetReport(command *cobra.Command, report deps.Report, format string) error {
	switch format {
	case "markdown":
		_, err := fmt.Fprint(command.OutOrStdout(), report.Markdown())
		return err
	case "yaml":
		raw, err := report.YAML()
		if err != nil {
			return err
		}
		_, err = command.OutOrStdout().Write(raw)
		return err
	case "json":
		raw, err := report.JSON()
		if err != nil {
			return err
		}
		_, err = command.OutOrStdout().Write(raw)
		return err
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}

func writeDepsBumpReport(command *cobra.Command, report deps.BumpReport, format string) error {
	switch format {
	case "markdown":
		_, err := fmt.Fprint(command.OutOrStdout(), report.Markdown())
		return err
	case "yaml":
		raw, err := report.YAML()
		if err != nil {
			return err
		}
		_, err = command.OutOrStdout().Write(raw)
		return err
	case "json":
		raw, err := report.JSON()
		if err != nil {
			return err
		}
		_, err = command.OutOrStdout().Write(raw)
		return err
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}
