package main

import (
	"context"
	"fmt"
	"net/url"
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
	fleet, dryRun, resume, allowDowngrade, noVerify bool
	commit, push, pr, merge                         bool
	match, regex, ref, checks, format, reportDir    string
	parallel, retry                                 int
	timeout                                         time.Duration
}

func newDepsCmd() *cobra.Command {
	command := &cobra.Command{
		Use:     "deps",
		Aliases: []string{"dep"},
		Short:   "Inspect and coordinate dependencies across repositories",
	}
	command.AddCommand(newDepsSetCmd())
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
			report, runErr := deps.Run(context.Background(), target, repositories, deps.Options{
				GitHubDir: projectsRoot, Ref: options.ref, Parallel: options.parallel,
				DryRun: options.dryRun, Resume: options.resume, AllowDowngrade: options.allowDowngrade,
				Verify: !options.noVerify, Checks: checks, Timeout: options.timeout, Retry: options.retry,
				Commit: options.commit, Push: options.push, PR: options.pr, Merge: options.merge,
				ReportDir: options.reportDir,
			})
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
	return command
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
