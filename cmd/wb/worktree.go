package main

import (
	"encoding/json"
	"fmt"
	"strings"
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
	command.AddCommand(newWorktreeAbortCmd())
	return command
}

func newWorktreeAbortCmd() *cobra.Command {
	var base, disposition, successor, format string
	var apply, deleteRemote bool
	command := &cobra.Command{
		Use:   "abort <task>",
		Short: "Seal an interrupted task so it can be resumed or explicitly discarded",
		Long: `Abort is the audited alternative to manually deleting an unfinished worktree.
It seals each local Work Log and queues an offline-safe outbox event. --apply
with handoff or not_landed transfers one active claim to the required
--successor while leaving the branch/worktree resumable; --apply with
discarded removes only clean, unlocked worktrees and their exact local branch
refs after their archive has been sealed. A discarded apply requires --remote;
an exact matching remote source branch is then retired with force-with-lease.
If interruption happens after worktree removal, the same command inspects and
resumes the durable exact local-branch cleanup backlog.
The default is a dry-run plan.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			results, err := worktrees.Abort(command.Context(), worktrees.AbortOptions{
				ProjectsRoot: projectsRoot, Task: args[0], Base: base,
				Disposition: worktrees.AbortDisposition(disposition), Successor: successor,
				DeleteRemote: deleteRemote, Apply: apply,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(results)
			}
			for _, result := range results {
				state := "would seal"
				if result.Applied && result.WorktreeGone {
					state = "sealed and removed"
				} else if result.Applied {
					state = "sealed and resumable"
				}
				if _, err := fmt.Fprintf(command.OutOrStdout(), "%s %s %s\n", state, result.Repository, result.Disposition); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&base, "base", "main", "base branch for managed-worktree validation")
	command.Flags().StringVar(&disposition, "disposition", "", "required: handoff, not_landed, or discarded")
	command.Flags().StringVar(&successor, "successor", "", "one successor agent/session ID (required for handoff or not_landed)")
	command.Flags().BoolVar(&apply, "apply", false, "seal Work Logs and apply the selected disposition")
	command.Flags().BoolVar(&deleteRemote, "remote", false, "retire an exact unchanged remote source branch when applying discarded")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newWorktreeCreateCmd() *cobra.Command {
	var branch, branchPrefix, base, format string
	var resume bool
	var effortID, runID, initiator, agentID, agentRuntime, model, originalPrompt string
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
unless --resume is explicit.

--original-prompt-file is mandatory. WB snapshots its exact non-empty bytes
into the private Work Log under WB_HOME before any worktree is created; prompt
text never enters the worktree projection, source Git, or normal output.`,
		Args: func(command *cobra.Command, args []string) error {
			if err := cobra.MinimumNArgs(1)(command, args); err != nil {
				return err
			}
			return validateWorktreeBranchFlags(command, branch)
		},
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			workLog := worktrees.WorkLogOptions{
				EffortID: effortID, RunID: runID, Initiator: initiator, AgentID: agentID,
				AgentRuntime: agentRuntime, Model: model, OriginalPrompt: originalPrompt,
				RequireOriginalPrompt: true,
			}
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
			workLog, err = worktrees.PrepareWorkLogOptions(projectsRoot, args[0], workLog)
			if err != nil {
				return err
			}
			if err := refreshManagedHooksBeforeWorktreeCreate(repositories); err != nil {
				return err
			}
			results, err := worktrees.Create(command.Context(), repositories, worktrees.CreateOptions{
				ProjectsRoot:       projectsRoot,
				Operation:          args[0],
				Branch:             branch,
				BranchChosen:       command.Flags().Changed("branch"),
				BranchPrefix:       branchPrefix,
				BranchPrefixChosen: command.Flags().Changed("branch-prefix"),
				Base:               base,
				Resume:             resume,
				WorkLog:            workLog,
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
	command.Flags().StringVar(&branch, "branch", "", "exact feature branch (overrides branch-prefix configuration)")
	command.Flags().StringVar(&branchPrefix, "branch-prefix", "", "derive <prefix><task>; an explicit empty value disables configured prefixes")
	command.Flags().StringVar(&base, "base", "main", "canonical and remote base branch")
	command.Flags().BoolVar(&resume, "resume", false, "reuse only the exact expected branch and worktree")
	command.Flags().StringVar(&effortID, "effort", "", "stable Synchestra/WB effort id (default task)")
	command.Flags().StringVar(&runID, "run", "", "agent run id (default generated locally)")
	command.Flags().StringVar(&initiator, "initiator", "", "human or agent that started the effort")
	command.Flags().StringVar(&agentID, "agent", "", "agent identity")
	command.Flags().StringVar(&agentRuntime, "agent-runtime", "", "agent runtime, e.g. codex or claude")
	command.Flags().StringVar(&model, "model", "", "agent model identifier")
	command.Flags().StringVar(&originalPrompt, "original-prompt-file", "", "required readable non-empty file containing the exact original prompt; archived under WB_HOME only")
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
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
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
		Short: "List live WB-managed task worktrees and their lifecycle state",
		Long: `List live linked checkout inventory below <wb-home>/worktrees (see
'wb worktree create --help' for how <wb-home> is resolved).

The default report uses only local Git data. Pass --github to include open and
exact fetched origin-target and pull request evidence used by worktree cleanup.
JSON output is a versioned control-plane envelope containing results,
diagnostics, and WB lifecycle artifacts so automation cannot miss cleanup
backlog that is not represented by a live Git worktree. This replaces the
legacy bare JSON result array; consumers must migrate to the envelope and
check schema_version.
Archived Work Logs and the approved seven-day recent-history view are not yet
joined into this command.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
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
			for _, artifact := range outcome.Artifacts {
				if _, err := fmt.Fprintf(command.ErrOrStderr(), "info: inventory classified WB internal %s as %s: %s\n", artifact.Kind, artifact.State, artifact.Path); err != nil {
					return err
				}
			}
			switch format {
			case "text":
				return printWorktreeList(command, outcome.Results)
			case "json":
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(outcome)
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
		Short: "Plan or remove clean WB tasks integrated into the exact origin target",
		Long: `Plan or apply cleanup of WB-managed task worktrees and local branches.

Cleanup requires every repository in a coordinated task to be clean, unlocked,
and to have its current branch head integrated into the freshly fetched exact
origin target. A matching merged GitHub pull request supplies merge-time age
evidence, but a verified direct push to the target is also eligible. A local
merge that was not pushed remains awaiting_push. The default is a dry-run plan.
--apply removes worktrees and exact local branch refs; --remote additionally
deletes an unchanged remote branch with force-with-lease protection. Durable
Work Log archive/outbox evidence is written before any remote or local deletion.
The same named dry-run/apply command inspects and resumes a durable exact-ref
backlog if interruption happened after a worktree disappeared.

Reserved .wb-stage-* and .wb-retired-stage-* entries are first-class cleanup
backlog. Apply descriptor-safely archives an exact recognized empty stage
outside the active task. A non-empty, symlinked, replaced, or invalid stage
blocks the task and is preserved for audited recovery.

For a specifically named task, the implicit age window is zero and --apply
requires --remote: definition of done includes retirement of the source remote
branch as well as the local worktree/branch. --all-merged fleet sweeping keeps
the 24-hour merged-PR grace window unless --older-than overrides it.

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
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			task := ""
			if len(args) == 1 {
				task = args[0]
			}
			if task != "" && !command.Flags().Changed("older-than") {
				olderThan = 0
			}
			if task != "" && apply && !deleteRemote {
				return fmt.Errorf("named terminal cleanup requires --remote so the retired source branch cannot remain as backlog")
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
			for _, artifact := range outcome.Artifacts {
				if _, err := fmt.Fprintf(command.ErrOrStderr(), "info: cleanup WB internal %s %s: disposition=%s eligible=%t applied=%t reason=%s\n",
					artifact.Kind, artifact.Path, artifact.Disposition, artifact.Eligible, artifact.Applied, artifact.Reason); err != nil {
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
				for _, artifact := range outcome.Artifacts {
					if artifact.Applied {
						return nil
					}
				}
				return fmt.Errorf("task %q was not removed because it did not satisfy cleanup safety", task)
			}
			return nil
		},
	}
	command.Flags().StringVar(&base, "base", "main", "exact origin target branch required to contain the work")
	command.Flags().BoolVar(&allMerged, "all-merged", false, "select every safely merged WB task")
	command.Flags().BoolVar(&apply, "apply", false, "remove eligible worktrees and local branches")
	command.Flags().BoolVar(&deleteRemote, "remote", false, "also delete an unchanged remote branch when applying")
	command.Flags().DurationVar(&olderThan, "older-than", 24*time.Hour, "minimum age of a merged pull request (0 disables)")
	command.Flags().StringVar(&reportDir, "report-dir", "", "cleanup audit directory (default <wb-home>/reports/worktree-cleanup/<timestamp>)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newWorktreeRenameCmd() *cobra.Command {
	var branch, branchPrefix, base, reportDir, format string
	var force, apply, deleteRemote bool
	var preserveCachePaths []string
	var effortID, runID, initiator, agentID, agentRuntime, model, originalPrompt string
	command := &cobra.Command{
		Use:   "rename <old-task> <new-task>",
		Short: "Re-home a task's worktrees under a new task name, keeping their working-tree contents",
		Long: `Move every repository worktree below <old-task> to <new-task> with a
descriptor-relative no-replace directory move. WB retains the exact checkout
identity through 'git worktree repair' and registration verification, so Git's
own gitdir pointers never go stale and a substituted endpoint is never moved.

Recycling is opt-in. WB refuses to carry arbitrary ignored/untracked state
into the next effort. Pass --preserve-cache for each repository-relative cache
path that may survive (for example node_modules); every other local file must
be removed or archived before recycling.

The branch itself is never recycled. After the move, each repository is
switched onto a freshly created branch (default <new-task>; derive a configured
prefix with --branch-prefix, or override exactly with --branch) based on an up-to-date origin/<base>, exactly like
'wb worktree create'. The old local branch is always deleted. Apply requires
--remote: an existing exact remote source branch is retired with force-with-
lease after the old Work Log is sealed. A branch that is not integrated into
origin/<base> is refused; --force is explicit authorization to discard that
old work, locally and remotely, after its Work Log is sealed.

--filter (see the root flag) narrows which repositories under <old-task> are
renamed. A malformed candidate, a dirty or locked worktree, or an already
existing <new-task> blocks the whole (coordinated) rename. The default is a
dry-run plan; --apply performs the move and requires --original-prompt-file
for the new private Work Log.`,
		Args: func(command *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(2)(command, args); err != nil {
				return err
			}
			return validateWorktreeBranchFlags(command, branch)
		},
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			outcome, err := worktrees.Rename(command.Context(), worktrees.RenameOptions{
				ProjectsRoot:       projectsRoot,
				OldTask:            args[0],
				NewTask:            args[1],
				Filter:             filterFlag,
				Branch:             branch,
				BranchChosen:       command.Flags().Changed("branch"),
				BranchPrefix:       branchPrefix,
				BranchPrefixChosen: command.Flags().Changed("branch-prefix"),
				Base:               base,
				Force:              force,
				DeleteRemote:       deleteRemote,
				PreserveCachePaths: preserveCachePaths,
				WorkLog: worktrees.WorkLogOptions{
					EffortID: effortID, RunID: runID, Initiator: initiator, AgentID: agentID,
					AgentRuntime: agentRuntime, Model: model, OriginalPrompt: originalPrompt,
					RequireOriginalPrompt: apply,
				},
				Apply:     apply,
				ReportDir: reportDir,
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
	command.Flags().StringVar(&branch, "branch", "", "exact feature branch for the renamed worktree (overrides branch-prefix configuration)")
	command.Flags().StringVar(&branchPrefix, "branch-prefix", "", "derive <prefix><new-task>; an explicit empty value disables configured prefixes")
	command.Flags().StringVar(&base, "base", "main", "canonical and remote base branch")
	command.Flags().BoolVar(&force, "force", false, "explicitly discard an old branch not integrated into origin/base; recycle always deletes the old local branch")
	command.Flags().BoolVar(&deleteRemote, "remote", false, "retire an exact unchanged old remote source branch; required with --apply")
	command.Flags().StringSliceVar(&preserveCachePaths, "preserve-cache", nil, "repository-relative ignored/untracked cache path allowed to survive recycle (repeatable)")
	command.Flags().StringVar(&effortID, "effort", "", "new stable Synchestra/WB effort id (default new task)")
	command.Flags().StringVar(&runID, "run", "", "new agent run id (default generated locally)")
	command.Flags().StringVar(&initiator, "initiator", "", "human or agent that started the new effort")
	command.Flags().StringVar(&agentID, "agent", "", "agent identity for the new effort")
	command.Flags().StringVar(&agentRuntime, "agent-runtime", "", "agent runtime, e.g. codex or claude")
	command.Flags().StringVar(&model, "model", "", "agent model identifier")
	command.Flags().StringVar(&originalPrompt, "original-prompt-file", "", "required with --apply: readable non-empty file containing the new exact prompt; archived under WB_HOME only")
	command.Flags().BoolVar(&apply, "apply", false, "perform the rename; the default is a dry-run plan")
	command.Flags().StringVar(&reportDir, "report-dir", "", "rename audit directory (default <wb-home>/reports/worktree-rename/<timestamp>)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func validateWorktreeBranchFlags(command *cobra.Command, branch string) error {
	branchChosen := command.Flags().Changed("branch")
	prefixChosen := command.Flags().Changed("branch-prefix")
	if branchChosen && prefixChosen {
		return fmt.Errorf("--branch and --branch-prefix cannot be used together")
	}
	if branchChosen && strings.TrimSpace(branch) == "" {
		return fmt.Errorf("--branch must not be empty when explicitly provided")
	}
	return nil
}

func requireOutputFormat(value string, allowed ...string) error {
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return fmt.Errorf("unsupported format %q; use %s", value, strings.Join(allowed, " or "))
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
