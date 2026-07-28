package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func newWorktreeCmd() *cobra.Command {
	command := &cobra.Command{
		Use:     "worktree",
		Aliases: []string{"worktrees", "wt"},
		Short:   "Create and enforce isolated development worktrees",
	}
	command.AddCommand(newWorktreeCreateCmd())
	command.AddCommand(newWorktreeGuardCmd())
	command.AddCommand(newWorktreeListCmd())
	command.AddCommand(newWorktreeCleanupCmd())
	return command
}

func newWorktreeCreateCmd() *cobra.Command {
	var branch, base, format string
	var resume bool
	command := &cobra.Command{
		Use:   "create <task> [owner/repository...]",
		Short: "Create feature branches below WB's home worktrees directory",
		Long: `Create one isolated feature worktree per repository.

The canonical clone must be clean and checked out on the base branch. WB pulls
that branch from origin with --ff-only before branching, then creates each
worktree at:

  <wb-home>/worktrees/<task>/<owner>/<repository>

<wb-home> is ~/.wb by default. Set $WB_HOME to use a different directory. An
existing installation with a populated <projects-root>/.wb keeps using that
location instead, so no worktree already in progress is stranded by upgrading.

If no repository is supplied, WB derives owner/repository from the current
checkout's origin remote. Existing branches or worktrees are never reused
unless --resume is explicit.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			repositories := args[1:]
			if len(repositories) == 0 {
				repository, err := worktrees.OriginSlug(command.Context(), ".")
				if err != nil {
					return fmt.Errorf("derive current repository: %w", err)
				}
				repositories = []string{repository}
			}
			results, err := worktrees.Create(command.Context(), repositories, worktrees.CreateOptions{
				ProjectsRoot: projectsRoot,
				Operation:    args[0],
				Branch:       branch,
				Base:         base,
				Resume:       resume,
			})
			if err != nil {
				return err
			}
			switch format {
			case "text":
				for _, result := range results {
					if _, err := fmt.Fprintf(command.OutOrStdout(), "%s %s: %s (%s from origin/%s)\n",
						result.Action, result.Repository, result.WorktreeDir, result.Branch, result.Base); err != nil {
						return err
					}
				}
			case "json":
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(results); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported format %q; use text or json", format)
			}
			return nil
		},
	}
	command.Flags().StringVar(&branch, "branch", "", "feature branch (default codex/<task>)")
	command.Flags().StringVar(&base, "base", "main", "canonical and remote base branch")
	command.Flags().BoolVar(&resume, "resume", false, "reuse only the exact expected branch and worktree")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newWorktreeGuardCmd() *cobra.Command {
	var base, format string
	var quiet bool
	command := &cobra.Command{
		Use:   "guard [repository-path]",
		Short: "Reject unsafe canonical clones and misplaced worktrees",
		Long: `Validate the checkout policy used by agents and WB Git hooks.

A canonical clone is valid only when it is clean and on the base branch. A
linked checkout is valid only when it uses a non-base branch and lives at
<wb-home>/worktrees/<task>/<owner>/<repository> (see 'wb worktree create --help'
for how <wb-home> is resolved).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			result, err := worktrees.Guard(command.Context(), path, worktrees.GuardOptions{
				ProjectsRoot: projectsRoot,
				Base:         base,
			})
			if err != nil {
				return err
			}
			if quiet {
				return nil
			}
			switch format {
			case "text":
				_, err = fmt.Fprintf(command.OutOrStdout(), "ok: %s checkout %s on %s\n", result.Kind, result.Path, result.Branch)
				return err
			case "json":
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(result)
			default:
				return fmt.Errorf("unsupported format %q; use text or json", format)
			}
		},
	}
	command.Flags().StringVar(&base, "base", "main", "protected canonical base branch")
	command.Flags().BoolVar(&quiet, "quiet", false, "write nothing when the checkout is valid")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newWorktreeListCmd() *cobra.Command {
	var base, format string
	var github bool
	command := &cobra.Command{
		Use:   "list [task]",
		Short: "List WB-managed task worktrees and their lifecycle state",
		Long: `List linked worktrees below <wb-home>/worktrees (see
'wb worktree create --help' for how <wb-home> is resolved).

The default report uses only local Git data. Pass --github to include open and
exact-head merged pull request evidence used by worktree cleanup.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			task := ""
			if len(args) == 1 {
				task = args[0]
			}
			results, err := worktrees.List(command.Context(), worktrees.ListOptions{
				ProjectsRoot: projectsRoot,
				Task:         task,
				Base:         base,
				GitHub:       github,
			})
			if err != nil {
				return err
			}
			switch format {
			case "text":
				return printWorktreeList(command, results)
			case "json":
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(results)
			default:
				return fmt.Errorf("unsupported format %q; use text or json", format)
			}
		},
	}
	command.Flags().StringVar(&base, "base", "main", "base branch used to assess local merge state")
	command.Flags().BoolVar(&github, "github", false, "include pull request state from GitHub")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newWorktreeCleanupCmd() *cobra.Command {
	var base, format, reportDir string
	var allMerged, apply, deleteRemote bool
	var olderThan time.Duration
	command := &cobra.Command{
		Use:   "cleanup [task]",
		Short: "Plan or remove clean WB tasks whose exact pull requests merged",
		Long: `Plan or apply cleanup of WB-managed task worktrees and local branches.

Cleanup requires every repository in a coordinated task to be clean, unlocked,
and backed by a merged GitHub pull request whose base and recorded head match
the current branch. The default is a dry-run plan. --apply removes worktrees
and exact local branch refs; --remote additionally deletes an unchanged remote
branch with force-with-lease protection.`,
		Args: func(command *cobra.Command, args []string) error {
			if err := cobra.MaximumNArgs(1)(command, args); err != nil {
				return err
			}
			if len(args) == 0 && !allMerged {
				return fmt.Errorf("supply one task or use --all-merged")
			}
			if len(args) == 1 && allMerged {
				return fmt.Errorf("task and --all-merged cannot be combined")
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			task := ""
			if len(args) == 1 {
				task = args[0]
			}
			now := time.Now()
			outcome, err := worktrees.Cleanup(command.Context(), worktrees.CleanupOptions{
				ProjectsRoot: projectsRoot,
				Task:         task,
				Base:         base,
				AllMerged:    allMerged,
				Apply:        apply,
				DeleteRemote: deleteRemote,
				OlderThan:    olderThan,
				ReportDir:    reportDir,
				Now:          func() time.Time { return now },
			})
			if err != nil {
				return err
			}
			switch format {
			case "text":
				if err := printWorktreeCleanup(command, outcome.Results, apply); err != nil {
					return err
				}
				if outcome.ReportPath != "" {
					if _, err := fmt.Fprintf(command.OutOrStdout(), "report: %s\n", outcome.ReportPath); err != nil {
						return err
					}
				}
			case "json":
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(outcome); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported format %q; use text or json", format)
			}
			if apply && task != "" {
				for _, result := range outcome.Results {
					if result.Applied {
						return nil
					}
				}
				return fmt.Errorf("task %q was not removed because it did not satisfy cleanup safety", task)
			}
			return nil
		},
	}
	command.Flags().StringVar(&base, "base", "main", "expected pull request base branch")
	command.Flags().BoolVar(&allMerged, "all-merged", false, "select every safely merged WB task")
	command.Flags().BoolVar(&apply, "apply", false, "remove eligible worktrees and local branches")
	command.Flags().BoolVar(&deleteRemote, "remote", false, "also delete an unchanged remote branch when applying")
	command.Flags().DurationVar(&olderThan, "older-than", 24*time.Hour, "minimum age of a merged pull request (0 disables)")
	command.Flags().StringVar(&reportDir, "report-dir", "", "cleanup audit directory (default <wb-home>/reports/worktree-cleanup/<timestamp>)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func printWorktreeList(command *cobra.Command, results []worktrees.ListResult) error {
	if len(results) == 0 {
		_, err := fmt.Fprintln(command.OutOrStdout(), "no WB worktrees")
		return err
	}
	for _, result := range results {
		state := "active"
		switch {
		case !result.Clean:
			state = "dirty"
		case result.Locked:
			state = "locked"
		case result.OpenPullRequest != nil:
			state = "open-pr"
		case result.MergedPullRequest != nil:
			state = "merged"
		case result.LocallyMerged:
			state = "locally-merged"
		}
		pr := "-"
		if result.OpenPullRequest != nil {
			pr = result.OpenPullRequest.URL
		} else if result.MergedPullRequest != nil {
			pr = result.MergedPullRequest.URL
		}
		if _, err := fmt.Fprintf(
			command.OutOrStdout(),
			"%s  %s  %s  %s  %s\n",
			result.Task, result.Repository, result.Branch, state, pr,
		); err != nil {
			return err
		}
	}
	return nil
}

func printWorktreeCleanup(command *cobra.Command, results []worktrees.CleanupResult, apply bool) error {
	if len(results) == 0 {
		_, err := fmt.Fprintln(command.OutOrStdout(), "no WB worktrees matched")
		return err
	}
	eligible, removed := 0, 0
	for _, result := range results {
		switch {
		case result.Applied:
			removed++
			remote := ""
			if result.RemoteDeleted {
				remote = " and remote branch"
			}
			if _, err := fmt.Fprintf(command.OutOrStdout(), "removed %s %s%s\n", result.Task, result.Repository, remote); err != nil {
				return err
			}
		case result.Eligible:
			eligible++
			if _, err := fmt.Fprintf(command.OutOrStdout(), "would remove %s %s\n", result.Task, result.Repository); err != nil {
				return err
			}
		default:
			if _, err := fmt.Fprintf(command.OutOrStdout(), "skip %s %s: %s\n", result.Task, result.Repository, result.Reason); err != nil {
				return err
			}
		}
	}
	if apply {
		_, err := fmt.Fprintf(command.OutOrStdout(), "%d removed\n", removed)
		return err
	}
	_, err := fmt.Fprintf(command.OutOrStdout(), "%d eligible; dry-run only, pass --apply to remove\n", eligible)
	return err
}
