package cmdworktree

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"io"
)

type RenameDependencies struct {
	Run        func(context.Context, worktrees.RenameOptions) (worktrees.RenameOutcome, error)
	Admit      func(*cobra.Command, bool) (worktrees.AgentIdentity, func(), error)
	AfterApply func(*cobra.Command, string, []worktrees.RenameResult)
}
type RelocateDependencies struct {
	Run           func(context.Context, worktrees.RelocateOptions) (worktrees.RelocateOutcome, error)
	Admit         func(*cobra.Command, bool) (worktrees.AgentIdentity, func(), error)
	AfterApply    func(*cobra.Command, []worktrees.RelocateResult)
	StartProgress func(io.Writer, string) func()
}

// NewRename constructs a managed checkout recycle command.
func NewRename(environment shared.Runtime, dependencies RenameDependencies) *cobra.Command {
	var branch, branchPrefix, base, reportDir, format string
	var force, apply, deleteRemote bool
	var preserveCachePaths []string
	var effortID, runID, initiator, agentID, agentRuntime, model, cli, provider, originalPrompt string
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
for the new private Work Log. Applying recycle is a mutation: --mode agent
requires a live registered session, while --mode manual requires --initiator
for an explicit audit record.`,
		Args: func(command *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(2)(command, args); err != nil {
				return err
			}
			return shared.ValidateBranchChoice(branch, command.Flags().Changed("branch"), command.Flags().Changed("branch-prefix"))
		},
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := dependencies.Admit(command, apply)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			flags := environment.Flags()
			outcome, err := dependencies.Run(command.Context(), worktrees.RenameOptions{
				ProjectsRoot:       flags.ProjectsRoot,
				OldTask:            args[0],
				NewTask:            args[1],
				Filter:             flags.Filter,
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
					AgentRuntime: agentRuntime, Model: model, CLI: cli, Provider: provider, OriginalPrompt: originalPrompt,
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
			// A recycled worktree carries a new path, task, and branch, so the
			// marker it inherited from the old effort is now wrong about all
			// three. Rewrite it before anything reads it.
			if apply {
				dependencies.AfterApply(command, base, outcome.Results)
			}
			if format == "text" {
				if err := writeRename(command.OutOrStdout(), outcome.Results, apply); err != nil {
					return err
				}
				if outcome.ReportPath != "" {
					if _, err := fmt.Fprintf(command.OutOrStdout(), "report: %s\n", outcome.ReportPath); err != nil {
						return err
					}
				}
			} else {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(outcome); err != nil {
					return err
				}
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
	command.Flags().StringVar(&model, "model", "", "required exact child model identifier, or explicit unknown; WB never guesses")
	command.Flags().StringVar(&cli, "cli", "", "optional invoking CLI/client identifier, supplied only when known")
	command.Flags().StringVar(&provider, "provider", "", "optional routing/billing provider identifier, never a credential")
	command.Flags().StringVar(&originalPrompt, "original-prompt-file", "", "required with --apply: readable non-empty file containing the new exact prompt; archived under the WB state directory only")
	command.Flags().BoolVar(&apply, "apply", false, "perform the rename; the default is a dry-run plan")
	command.Flags().StringVar(&reportDir, "report-dir", "", "rename audit directory (default <projects-root>/.wb/reports/worktree-rename/<timestamp>)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

// NewRelocate constructs a managed checkout relocation command.
func NewRelocate(environment shared.Runtime, dependencies RelocateDependencies) *cobra.Command {
	var to, format string
	var apply, jsonShortcut bool
	command := &cobra.Command{
		Use:   "relocate <task>",
		Short: "Move managed worktrees between repository-local and configured shared layouts",
		Long: `Plan or apply a physical layout move while preserving each task's branch and
immutable Work Log claim. The default is a dry run. Pass --apply only after
reviewing the exact source and destination paths.

--to=local moves a managed checkout to <canonical>/.worktrees/<task>.
--to=shared moves it to the central checkout store — <root>/.worktrees when no
worktrees.root is configured, which is the default — and refuses when the
machine-local store mode is repository-local, because that mode has no shared
root to move into. The destination embeds the canonical clone's literal host
level. Changing the store configuration never moves or hides an existing
checkout; the relocation receipt records the destination relative to the root
that produced it, so it still names the checkout after a later reconfigure.
WB inventories every managed layout through the Git worktree registry and
active claims, then rechecks the clean state, ownership lock, branch/head,
source, and destination under the task lock immediately before the
descriptor-anchored no-replace move. Git registration is repaired and verified
before an append-only relocation receipt is recorded.

Adopted external worktrees are reported but are never moved by this command.
Use --filter to select repositories within a coordinated task. --format=json
keeps stdout machine-readable; progress and diagnostics use stderr.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if jsonShortcut {
				if command.Flags().Changed("format") && format != "json" {
					return fmt.Errorf("--json cannot be combined with --format=%s", format)
				}
				format = "json"
			}
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := dependencies.Admit(command, apply)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			stopProgress := dependencies.StartProgress(command.ErrOrStderr(), args[0])
			defer stopProgress()
			flags := environment.Flags()
			outcome, err := dependencies.Run(command.Context(), worktrees.RelocateOptions{
				ProjectsRoot: flags.ProjectsRoot, Task: args[0], Filter: flags.Filter, To: to, Apply: apply,
			})
			if err != nil {
				return err
			}
			if apply {
				dependencies.AfterApply(command, outcome.Results)
			}
			for _, diagnostic := range outcome.Diagnostics {
				_, _ = fmt.Fprintf(command.ErrOrStderr(), "warning: relocate skipped malformed candidate in task %s: %s: %s\n", diagnostic.Task, diagnostic.Path, diagnostic.Message)
			}
			if format == "json" {
				return json.NewEncoder(command.OutOrStdout()).Encode(outcome)
			}
			return writeRelocation(command.OutOrStdout(), outcome.Results, apply)
		},
	}
	command.Flags().StringVar(&to, "to", "", "destination layout: local or shared")
	command.Flags().BoolVar(&apply, "apply", false, "perform the relocation; the default is a dry-run plan")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonShortcut, "json", false, "shortcut for --format=json")
	return command
}
