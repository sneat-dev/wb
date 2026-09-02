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
	campaign         *campaignProgress
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
	command.AddCommand(newDepsPeersCmd())
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

func newDepsDriftCmd() *cobra.Command {
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
			campaign := newCampaignProgress(command.ErrOrStderr(), console.Interactive(command.ErrOrStderr(), nonInteractive), "deps drift")
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
				Ecosystem: ecosystem,
				GitHubDir: projectsRoot, Ref: options.ref, Parallel: options.parallel,
				Timeout: options.timeout, Retry: options.retry, GoPrivate: options.goPrivate,
				Dependencies: options.dependencies, Scopes: options.scopes, ExcludeRepositories: options.exclude,
				Online: options.online, FailOnDrift: options.failOnDrift, FailOnBehind: options.failOnBehind,
				Progress: campaign.reporter(),
			})
			if err != nil {
				campaign.finish("failed")
				return err
			}
			if deps.DriftFailedWith(report, options.failOnDrift, options.failOnBehind) {
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
			if deps.DriftFailedWith(report, options.failOnDrift, options.failOnBehind) {
				return &exitError{
					code:    exitFindings,
					message: "dependency drift or inspection errors were reported; see the index above",
				}
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

type depsPeersOptions struct {
	against, format string
	timeout         time.Duration
	retry           int
}

func newDepsPeersCmd() *cobra.Command {
	options := depsPeersOptions{}
	command := &cobra.Command{
		Use:   "peers <package>[@<version>]",
		Short: "Report a published npm package's peer requirements against one checkout",
		Long: `Answer "can I reuse this package here" with evidence instead of an install.

The question gets asked constantly and answered badly: run the install, read
whatever the package manager prints about peer conflicts, and hope the warning
names the real culprit. That mutates the checkout to find out, and a workspace's
peer warnings do not distinguish "you are two majors behind" from "the publisher
marked this peer optional".

So WB reads the published package's own peerDependencies (and
peerDependenciesMeta), reads what the target checkout actually resolves for each
of them — the version the governing pnpm-lock.yaml or package-lock.json
installs, not the caret range a manifest declares — and prints one row per peer:

    wb deps peers @sneat/core --against ../renewon

Nothing is installed and nothing is written. Each row's verdict is one of:

  satisfied         the target's version is admitted by the peer range
  unsatisfied       the target has it, at a version the range rejects
  missing           the target does not have it at all
  optional_missing  the publisher marked it optional; the target omits it
  unevaluated       WB will not guess this specifier shape, and says so

The last one is deliberate. WB evaluates the specifier subset a Sneat manifest
actually uses; a union, a hyphen range, or a workspace:/catalog: protocol is
reported unevaluated with its reason rather than silently counted as compatible.
An unevaluated row is therefore never a pass.

Exit code 1 when any required peer is unsatisfied or missing, 0 otherwise.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			report, err := deps.InspectPeers(commandExecutionContext(command), deps.PeerOptions{
				Package: args[0], Against: options.against,
				Timeout: options.timeout, Retry: options.retry,
			})
			if err != nil {
				return err
			}
			if err := writeDepsPeersReport(command, report, options.format); err != nil {
				return err
			}
			if deps.PeersFailed(report) {
				return &exitError{
					code:    exitFindings,
					message: "one or more peer requirements are unsatisfied or missing in the target checkout; see the table above",
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&options.against, "against", "", "path of the checkout whose installed versions the peers are judged against")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().DurationVar(&options.timeout, "timeout", 5*time.Minute, "maximum duration per registry command (0 disables)")
	command.Flags().IntVar(&options.retry, "retry", 0, "additional attempts for a failed registry command")
	return command
}

func writeDepsPeersReport(command *cobra.Command, report deps.PeerReport, format string) error {
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

func newDepsBumpCmd() *cobra.Command {
	options := depsSetOptions{}
	var changed []string
	command := &cobra.Command{
		Use:   "bump <ecosystem>",
		Short: "Propagate published dependency versions through recalculated waves",
		Long: `Propagate published dependency versions through recalculated consumer waves.

Seeding the campaign:

  --changed <module@version>  The published release event, typed exactly.

  --latest --scope <glob>     WB reads the modules the selected repositories
                              declare, keeps the ones a --scope glob matches,
                              and asks the registry for each one's published
                              latest version — producing the same --changed
                              list without typing it. Every matched module is
                              listed in the report, including the ones that
                              published nothing, so a scope's coverage is
                              auditable rather than assumed. The two compose:
                              the newest version observed for a dependency
                              wins, so a release still in flight can be named
                              with --changed alongside a --latest sweep.

Two flags shape which repositories the campaign touches, and they mean
different things:

  --exclude <org/repo glob>   The repository is removed from the campaign
                              entirely, before anything is discovered: no graph
                              entry, no wave membership, no worktree, no pull
                              request. Use it for an archived or irrelevant
                              repository. Excluded slugs are listed in the
                              report, so "needed nothing" is never confused
                              with "never looked at".

  --hold <org/repo glob>      The repository IS bumped, verified, pushed, and
                              has its pull request opened and its exact PR-head
                              GitHub checks waited on — and is then left OPEN,
                              even under --merge. Use it for a repository whose
                              merge is a human decision, such as a gated deploy
                              repository. Because a release that needs a human
                              merge cannot be waited for, a wave containing a
                              held repository stops the campaign with status
                              awaiting_hold_release and names the pull requests
                              the remaining waves are waiting on.

Both accept path.Match globs, where "*" never crosses a "/", and an exact
"owner/name" always matches itself. --scope uses the same glob semantics
against a module path or package name, exactly as "wb deps drift --scope"
does: "@sneat/*" matches "@sneat/core", and "github.com/dal-go/*" matches
"github.com/dal-go/dalgo" but not a nested "github.com/dal-go/dalgo/x".`,
		Args: cobra.ExactArgs(1),
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
			validationMode, checks, err := dependencyValidationOptions(command, options)
			if err != nil {
				return err
			}
			options.validation = string(validationMode)
			options.parallelExplicit = depsBumpParallelExplicit(command)
			// Both halves of the derivation are checked before a single
			// repository is discovered: a fleet-wide registry sweep with no
			// selection is nobody's intended default, and a scope that
			// silently does nothing is worse than a refusal.
			if len(options.scopes) > 0 && !options.latest {
				return fmt.Errorf("--scope selects which published modules --latest derives release events from; pass --latest, or name the events with --changed")
			}
			if options.latest && len(deps.NormalizeScopes(options.scopes)) == 0 {
				return fmt.Errorf("--latest derives release events for the modules --scope selects; pass at least one --scope glob, e.g. --scope '@sneat/*'")
			}
			campaign := newCampaignProgress(command.ErrOrStderr(), console.Interactive(command.ErrOrStderr(), nonInteractive), "deps bump")
			options.campaign = campaign
			// Repository selection comes first now: --latest derives its seed
			// events from the modules the selected repositories actually
			// declare, so there is nothing to derive from until the selection
			// exists.
			repositories, err := dependencyRepositories([]string{args[0], "events"}, options)
			if err != nil {
				campaign.finish("failed")
				return err
			}
			lifecycle := dependencyOptions(options, checks)
			events, err := depsBumpSeedEvents(command, ecosystem, changed, repositories, &options, lifecycle)
			if err != nil {
				campaign.finish("failed")
				return err
			}
			return runDepsBump(command, ecosystem, events, repositories, options, lifecycle)
		},
	}
	command.Flags().StringArrayVar(&changed, "changed", nil, "published module@version release event (repeatable)")
	command.Flags().BoolVar(&options.latest, "latest", false, "derive the seed release events from the registry's published latest version of every module matching --scope")
	command.Flags().StringArrayVar(&options.scopes, "scope", nil, "with --latest, glob matched against a published module path or package name, e.g. @sneat/* (repeatable)")
	command.Flags().StringArrayVar(&options.exclude, "exclude", nil, "org/repo glob removed from the campaign entirely: no graph entry, no wave, no worktree, no PR (repeatable)")
	command.Flags().StringArrayVar(&options.hold, "hold", nil, "org/repo glob whose PR is opened and CI-waited but never merged, even under --merge; downstream waves stop and name the held PRs (repeatable)")
	command.Flags().BoolVar(&options.fleet, "fleet", false, "reconcile and process selected local and owned GitHub repositories")
	command.Flags().StringVar(&options.match, "match", "", "glob matched against org/repo, e.g. sneat-co/*")
	command.Flags().StringVar(&options.regex, "regex", "", "regular expression matched against org/repo")
	command.Flags().StringVar(&options.ref, "ref", "main", "base ref for operation worktrees")
	command.Flags().IntVar(&options.parallel, "parallel", 1, "maximum repositories or release observations to process concurrently")
	command.Flags().IntVar(&options.maxWaves, "max-waves", 20, "maximum recalculated dependency waves")
	command.Flags().DurationVar(&options.releasePoll, "release-poll", 30*time.Second, "interval between provider release observations")
	command.Flags().DurationVar(&options.refreshAfter, "refresh-after", 5*time.Minute, "recheck release events older than this before starting a downstream build (0 disables)")
	command.Flags().BoolVar(&options.dryRun, "dry-run", false, "inspect the first wave without creating worktrees or changing dependency files")
	command.Flags().BoolVar(&options.fetchCache, "fetch-cache", false, "memoize DISCOVERY fetches for up to 15m within this run for repositories the campaign never pushed to, opened a PR for, or merged (opt-in; wave mutation bases always re-fetch; nothing persists across invocations; avoid when others may land on main mid-campaign)")
	command.Flags().BoolVar(&options.resume, "resume", false, "reuse existing wave worktrees, branches, PRs, and report state")
	command.Flags().BoolVar(&options.allowDowngrade, "allow-downgrade", false, "permit a release event lower than an observed semantic version")
	command.Flags().StringVar(&options.checks, "checks", "", "comma-separated checks: lint,test,build (default all)")
	command.Flags().StringVar(&options.validation, "validation", string(deps.ValidationModeFull), "validation mode: full, or fast with mandatory exact PR-head CI before merge or PR validation")
	command.Flags().BoolVar(&options.noVerify, "no-verify", false, "legacy explicit escape hatch that skips local verification (distinct from --validation=fast)")
	command.Flags().DurationVar(&options.timeout, "timeout", 30*time.Minute, "maximum duration per external check, CI wait, or release wait (0 disables)")
	command.Flags().IntVar(&options.retry, "retry", 0, "additional attempts for failed external commands")
	command.Flags().BoolVar(&options.commit, "commit", false, "commit dependency changes on wave branches")
	command.Flags().BoolVar(&options.push, "push", false, "push wave branches; implies --commit")
	command.Flags().BoolVar(&options.pr, "pr", false, "open pull requests; implies --push and --commit")
	command.Flags().BoolVar(&options.merge, "merge", false, "merge passing PRs and observe releases; implies --pr, --push, and --commit")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write deps-bump.md and deps-bump.yaml to this directory")
	command.Flags().StringArrayVar(&options.goPrivate, "go-private", nil, "private Go module path pattern excluded from public proxy and checksum lookup (repeatable)")
	return command
}

func dependencyOptions(options depsSetOptions, checks []quality.Check) deps.Options {
	validationMode := deps.ValidationMode(options.validation)
	if options.noVerify {
		validationMode = deps.ValidationModeNone
	} else if validationMode == "" {
		validationMode = deps.ValidationModeFull
	}
	return deps.Options{
		GitHubDir: projectsRoot, Ref: options.ref, Parallel: options.parallel, ParallelExplicit: options.parallelExplicit,
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

// depsBumpSeedEvents produces the campaign's seed release events, from what
// the operator typed, from what the registry has published, or from both.
//
// Typing every `module@version` by hand is a dozen chances to get a
// coordinated release wrong, and the worst failure is silent: an omitted
// provider is not an error, it is a consumer that stays stale. --latest reads
// the published version of every module matching --scope instead, so the seed
// list is derived from what exists rather than from what was remembered.
//
// Explicit --changed events compose with derived ones under the wave engine's
// own rule: the newest version observed for a dependency wins. So an operator
// seeding a release still in flight keeps their newer version, while a scope
// that has since published something newer than a stale hand-typed event
// corrects it rather than propagating the stale one.
func depsBumpSeedEvents(command *cobra.Command, ecosystem deps.Ecosystem, changed []string, repositories []deps.Repository, options *depsSetOptions, lifecycle deps.Options) ([]deps.ReleaseEvent, error) {
	if !options.latest {
		return parseReleaseEvents(ecosystem, changed)
	}
	var explicit []deps.ReleaseEvent
	if len(changed) > 0 {
		parsed, err := parseReleaseEvents(ecosystem, changed)
		if err != nil {
			return nil, err
		}
		explicit = parsed
	}
	if options.campaign != nil {
		lifecycle.Progress = options.campaign.reporter()
	}
	derived, resolutions, err := deps.DeriveLatestReleaseEvents(
		commandExecutionContext(command), repositories, options.scopes,
		deps.BumpOptions{Options: lifecycle, Ecosystem: ecosystem},
	)
	if err != nil {
		return nil, err
	}
	options.scopeResolutions = resolutions
	return deps.MergeReleaseEvents(explicit, derived), nil
}

// bumpDerivedScopes records scopes in the report only when they actually
// derived this campaign's seed events. `deps set --propagate` and the
// composite publication commands share this execution path without a --latest
// derivation, and a report that named scopes they never used would claim
// provenance the run does not have.
func bumpDerivedScopes(options depsSetOptions) []string {
	if !options.latest {
		return nil
	}
	return options.scopes
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
		Previous: previous, NoRegistry: noRegistry, FetchCache: options.fetchCache,
		Scopes: bumpDerivedScopes(options), ScopeResolutions: options.scopeResolutions,
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
		report.ParallelExplicit = true
		return lifecycle, report, nil
	}
	if report.Parallel < 1 {
		return deps.Options{}, deps.BumpReport{}, fmt.Errorf("resume report has invalid parallelism %d", report.Parallel)
	}
	lifecycle.Parallel = report.Parallel
	// Restore the original run's explicit-parallel authority too: a resumed
	// `--parallel 1` campaign must not regain the read-only worker floor
	// merely because the resume invocation itself omitted the flag.
	lifecycle.ParallelExplicit = report.ParallelExplicit
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
