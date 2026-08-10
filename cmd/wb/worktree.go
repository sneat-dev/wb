package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/hooks"
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
	command.AddCommand(newWorktreeRenameCmd())
	return command
}

func newWorktreeCreateCmd() *cobra.Command {
	var branch, base, format string
	var resume bool
	command := &cobra.Command{
		Use:   "create <task> [owner/repository...]",
		Short: "Create feature branches below WB's home worktrees directory",
		Long: `Create one isolated feature worktree per repository.

The canonical clone must be clean. WB fetches the exact requested base from
origin without switching or updating any local branch, then creates each
worktree from that verified commit at:

  <wb-home>/worktrees/<task>/<owner>/<repository>

<wb-home> is ~/.wb by default. Set $WB_HOME to use a different directory.
New work never silently falls back to <projects-root>/.wb; when WB_HOME is not
explicit, existing legacy worktrees there remain guardable, listable, and
cleanable during migration.

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
			var err error
			repositories, err = worktrees.ValidateRepositories(repositories)
			if err != nil {
				return err
			}
			if err := refreshManagedHooksBeforeWorktreeCreate(repositories); err != nil {
				return err
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

func refreshManagedHooksBeforeWorktreeCreate(repositories []string) error {
	canonicalRepositories := make([]string, 0, len(repositories))
	for _, repository := range repositories {
		canonical, err := worktrees.CanonicalRepositoryPath(projectsRoot, repository)
		if err != nil {
			return err
		}
		canonicalRepositories = append(canonicalRepositories, canonical)
	}
	for index, repository := range repositories {
		canonical := canonicalRepositories[index]
		_, err := hooks.RefreshManagedShims(canonical, "", hookExecutable(), projectsRoot)
		if err != nil {
			return fmt.Errorf("verify hooks for %s before creating a worktree: %w", repository, err)
		}
	}
	return nil
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
				checkout := result.Branch
				if result.Transient {
					checkout = "detached HEAD (active rebase)"
				}
				_, err = fmt.Fprintf(command.OutOrStdout(), "ok: %s checkout %s on %s\n", result.Kind, result.Path, checkout)
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
			outcome, err := worktrees.ListWithDiagnostics(command.Context(), worktrees.ListOptions{
				ProjectsRoot: projectsRoot,
				Task:         task,
				Base:         base,
				Filter:       filterFlag,
				GitHub:       github,
			})
			if err != nil {
				return err
			}
			for _, diagnostic := range outcome.Diagnostics {
				if _, err := fmt.Fprintf(command.ErrOrStderr(), "warning: task %s candidate %s: %s\n", diagnostic.Task, diagnostic.Path, diagnostic.Message); err != nil {
					return err
				}
			}
			switch format {
			case "text":
				return printWorktreeList(command, outcome.Results)
			case "json":
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(outcome.Results)
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
branch with force-with-lease protection.

--filter (see the root flag) and a named [task] both narrow which candidates
are inspected at all, before any of the above safety checks run. A malformed
candidate outside that selection is invisible to the run. One inside it is
never fatal: it is reported as a warning and blocks eligibility only for its
own coordinated task, exactly like an unclean or locked sibling would.`,
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
				Filter:       filterFlag,
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
			for _, diagnostic := range outcome.Diagnostics {
				if _, err := fmt.Fprintf(command.ErrOrStderr(), "warning: cleanup skipped malformed candidate in task %s: %s: %s\n", diagnostic.Task, diagnostic.Path, diagnostic.Message); err != nil {
					return err
				}
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

func newWorktreeRenameCmd() *cobra.Command {
	var branch, base, reportDir, format string
	var deleteOldBranch, force, apply bool
	command := &cobra.Command{
		Use:   "rename <old-task> <new-task>",
		Short: "Re-home a task's worktrees under a new task name, keeping their working-tree contents",
		Long: `Move every repository worktree below <old-task> to <new-task> using
'git worktree move' — with a plain move plus 'git worktree repair' as a
verified fallback — so Git's own gitdir pointers never go stale.

Renaming preserves working-tree contents such as node_modules, build caches,
and .venv: it exists so a finished task's worktree can be recycled for the
next one instead of deleted and recreated from scratch.

The branch itself is never recycled. After the move, each repository is
switched onto a freshly created branch (default codex/<new-task>; override
with --branch) based on an up-to-date origin/<base>, exactly like
'wb worktree create'. Pass --delete-old-branch to remove the branch the
worktree was on; a branch that is not merged into base is refused unless
--force.

--filter (see the root flag) narrows which repositories under <old-task> are
renamed. A malformed candidate, a dirty or locked worktree, or an already
existing <new-task> blocks the whole (coordinated) rename. The default is a
dry-run plan; --apply performs the move.`,
		Args: cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			outcome, err := worktrees.Rename(command.Context(), worktrees.RenameOptions{
				ProjectsRoot:    projectsRoot,
				OldTask:         args[0],
				NewTask:         args[1],
				Filter:          filterFlag,
				Branch:          branch,
				Base:            base,
				DeleteOldBranch: deleteOldBranch,
				Force:           force,
				Apply:           apply,
				ReportDir:       reportDir,
			})
			if err != nil {
				return err
			}
			for _, diagnostic := range outcome.Diagnostics {
				if _, err := fmt.Fprintf(command.ErrOrStderr(), "warning: rename skipped malformed candidate in task %s: %s: %s\n", diagnostic.Task, diagnostic.Path, diagnostic.Message); err != nil {
					return err
				}
			}
			switch format {
			case "text":
				if err := printWorktreeRename(command, outcome.Results, apply); err != nil {
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
			if apply {
				for _, result := range outcome.Results {
					if result.Applied {
						return nil
					}
				}
				return fmt.Errorf("task %q was not renamed because it did not satisfy rename safety", args[0])
			}
			return nil
		},
	}
	command.Flags().StringVar(&branch, "branch", "", "feature branch for the renamed worktree (default codex/<new-task>)")
	command.Flags().StringVar(&base, "base", "main", "canonical and remote base branch")
	command.Flags().BoolVar(&deleteOldBranch, "delete-old-branch", false, "delete the branch each worktree was on after a successful move")
	command.Flags().BoolVar(&force, "force", false, "delete the old branch even if it is not merged into base")
	command.Flags().BoolVar(&apply, "apply", false, "perform the rename; the default is a dry-run plan")
	command.Flags().StringVar(&reportDir, "report-dir", "", "rename audit directory (default <wb-home>/reports/worktree-rename/<timestamp>)")
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

func printWorktreeRename(command *cobra.Command, results []worktrees.RenameResult, apply bool) error {
	if len(results) == 0 {
		_, err := fmt.Fprintln(command.OutOrStdout(), "no WB worktrees matched")
		return err
	}
	eligible, renamed := 0, 0
	for _, result := range results {
		switch {
		case result.Applied:
			renamed++
			note := ""
			if result.OldBranchDeleted {
				note = " and deleted old branch " + result.OldBranch
			}
			if _, err := fmt.Fprintf(command.OutOrStdout(), "renamed %s %s -> %s (%s)%s\n", result.OldTask, result.Repository, result.NewWorktreeDir, result.NewBranch, note); err != nil {
				return err
			}
		case result.Eligible:
			eligible++
			if _, err := fmt.Fprintf(command.OutOrStdout(), "would rename %s %s -> %s\n", result.OldTask, result.Repository, result.NewWorktreeDir); err != nil {
				return err
			}
		default:
			if _, err := fmt.Fprintf(command.OutOrStdout(), "skip %s %s: %s\n", result.OldTask, result.Repository, result.Reason); err != nil {
				return err
			}
		}
	}
	if apply {
		_, err := fmt.Fprintf(command.OutOrStdout(), "%d renamed\n", renamed)
		return err
	}
	_, err := fmt.Fprintf(command.OutOrStdout(), "%d eligible; dry-run only, pass --apply to rename\n", eligible)
	return err
}
