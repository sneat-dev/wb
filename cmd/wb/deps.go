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

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/quality"
)

type depsSetOptions struct {
	fleet, dryRun, resume, allowDowngrade, noVerify, propagate bool
	commit, push, pr, merge                                    bool
	match, regex, ref, checks, format, reportDir               string
	parallel, retry, maxWaves                                  int
	timeout, releasePoll                                       time.Duration
	goPrivate                                                  []string
}

func newDepsCmd() *cobra.Command {
	command := &cobra.Command{
		Use:     "deps",
		Aliases: []string{"dep"},
		Short:   "Inspect and coordinate dependencies across repositories",
	}
	command.AddCommand(newDepsSetCmd())
	command.AddCommand(newDepsBumpCmd())
	command.AddCommand(newDepsGraphCmd())
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
			if options.fleet && len(args) == 1 {
				return fmt.Errorf("repository-path cannot be used with --fleet")
			}
			if options.ecosystem != string(deps.EcosystemGo) {
				return fmt.Errorf("dependency graph currently supports only the go ecosystem")
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
			})
			if err != nil {
				return err
			}
			graph, err := deps.BuildGraph(command.Context(), repositories, deps.GraphOptions{
				Ecosystem: deps.EcosystemGo, GitHubDir: projectsRoot, Ref: options.ref,
				Parallel: options.parallel, Timeout: options.timeout, Retry: options.retry,
				Dependencies: options.dependencies,
			})
			if err != nil {
				return err
			}
			reportDirectory := options.reportDir
			if reportDirectory == "" {
				reportDirectory = filepath.Join(projectsRoot, ".wb", "reports", "deps-graph-go")
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
	command.Flags().StringVar(&options.ecosystem, "ecosystem", string(deps.EcosystemGo), "manifest ecosystem (currently go)")
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
			target, err := deps.ParseTarget(args[0], args[1])
			if err != nil {
				return err
			}
			checks, err := quality.ParseChecks(options.checks)
			if err != nil {
				return err
			}
			repositories, err := dependencyRepositories(args, options)
			if err != nil {
				return err
			}
			lifecycle := dependencyOptions(options, checks)
			if options.propagate {
				if !options.fleet {
					return fmt.Errorf("--propagate requires --fleet")
				}
				if target.Ecosystem != deps.EcosystemGo {
					return fmt.Errorf("--propagate is supported only for the go ecosystem; it delegates to deps bump")
				}
				events := []deps.ReleaseEvent{{Dependency: target.Dependency, Version: target.Version, Source: "exact_set"}}
				return runDepsBump(command, events, repositories, options, lifecycle)
			}
			report, runErr := deps.Run(context.Background(), target, repositories, lifecycle)
			reportDirectory := options.reportDir
			if reportDirectory == "" && report.Operation != "" {
				reportDirectory = filepath.Join(projectsRoot, ".wb", "reports", report.Operation)
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
	command.Flags().BoolVar(&options.propagate, "propagate", false, "delegate this exact Go release event to deps bump waves (requires --fleet)")
	command.Flags().IntVar(&options.maxWaves, "max-waves", 20, "maximum recalculated dependency waves when --propagate is used")
	command.Flags().DurationVar(&options.releasePoll, "release-poll", 10*time.Second, "provider release polling interval when --propagate is used")
	command.Flags().StringVar(&options.checks, "checks", "", "comma-separated checks: lint,test,build (default all)")
	command.Flags().BoolVar(&options.noVerify, "no-verify", false, "explicitly skip local verification")
	command.Flags().DurationVar(&options.timeout, "timeout", 30*time.Minute, "maximum duration per external check and CI wait (0 disables)")
	command.Flags().IntVar(&options.retry, "retry", 0, "additional attempts for failed external commands")
	command.Flags().BoolVar(&options.commit, "commit", false, "commit verified changes on operation branches")
	command.Flags().BoolVar(&options.push, "push", false, "push operation branches; implies --commit")
	command.Flags().BoolVar(&options.pr, "pr", false, "open pull requests; implies --push and --commit")
	command.Flags().BoolVar(&options.merge, "merge", false, "wait for passing GitHub checks and merge; implies --pr, --push, and --commit")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown or yaml")
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
			if args[0] != string(deps.EcosystemGo) {
				return fmt.Errorf("dependency waves currently support only the go ecosystem")
			}
			if !options.fleet {
				return fmt.Errorf("deps bump requires --fleet")
			}
			if options.noVerify && command.Flags().Changed("checks") {
				return fmt.Errorf("--no-verify and --checks cannot be used together")
			}
			events, err := parseReleaseEvents(changed)
			if err != nil {
				return err
			}
			checks, err := quality.ParseChecks(options.checks)
			if err != nil {
				return err
			}
			repositories, err := dependencyRepositories([]string{args[0], "events"}, options)
			if err != nil {
				return err
			}
			return runDepsBump(command, events, repositories, options, dependencyOptions(options, checks))
		},
	}
	command.Flags().StringArrayVar(&changed, "changed", nil, "published module@version release event (repeatable)")
	command.Flags().BoolVar(&options.fleet, "fleet", false, "reconcile and process selected local and owned GitHub repositories")
	command.Flags().StringVar(&options.match, "match", "", "glob matched against org/repo, e.g. sneat-co/*")
	command.Flags().StringVar(&options.regex, "regex", "", "regular expression matched against org/repo")
	command.Flags().StringVar(&options.ref, "ref", "main", "base ref for operation worktrees")
	command.Flags().IntVar(&options.parallel, "parallel", 1, "maximum repositories or release observations to process concurrently")
	command.Flags().IntVar(&options.maxWaves, "max-waves", 20, "maximum recalculated dependency waves")
	command.Flags().DurationVar(&options.releasePoll, "release-poll", 10*time.Second, "interval between provider release observations")
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
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown or yaml")
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
	}
}

func parseReleaseEvents(values []string) ([]deps.ReleaseEvent, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("at least one --changed module@version event is required")
	}
	events := make([]deps.ReleaseEvent, 0, len(values))
	for _, value := range values {
		target, err := deps.ParseTarget(string(deps.EcosystemGo), value)
		if err != nil {
			return nil, err
		}
		events = append(events, deps.ReleaseEvent{Dependency: target.Dependency, Version: target.Version, Source: "explicit"})
	}
	return events, nil
}

func runDepsBump(command *cobra.Command, events []deps.ReleaseEvent, repositories []deps.Repository, options depsSetOptions, lifecycle deps.Options) error {
	operation := deps.BumpOperationID(events)
	reportDirectory := options.reportDir
	if reportDirectory == "" {
		reportDirectory = filepath.Join(projectsRoot, ".wb", "reports", operation)
	}
	bumpOptions := deps.BumpOptions{
		Options: lifecycle, MaxWaves: options.maxWaves, PollInterval: options.releasePoll,
		Persist: func(report deps.BumpReport) error { return deps.WriteBumpReports(reportDirectory, report) },
	}
	if options.resume {
		if previous, err := deps.LoadBumpReport(reportDirectory); err == nil {
			bumpOptions.Previous = &previous
		} else {
			if os.IsNotExist(err) {
				return fmt.Errorf("--resume requires %s: %w", filepath.Join(reportDirectory, "deps-bump.yaml"), err)
			}
			return err
		}
	}
	report, runErr := deps.RunBump(context.Background(), events, repositories, bumpOptions)
	if report.Operation == "" {
		return runErr
	}
	if err := deps.WriteBumpReports(reportDirectory, report); err != nil {
		return err
	}
	if err := writeDepsBumpReport(command, report, options.format); err != nil {
		return err
	}
	return runErr
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
	default:
		return fmt.Errorf("unknown --format %q (want markdown or yaml)", format)
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
	default:
		return fmt.Errorf("unknown --format %q (want markdown or yaml)", format)
	}
}
