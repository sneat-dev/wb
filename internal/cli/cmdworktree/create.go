package cmdworktree

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktreerun"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"io"
	"strings"
)

type CreateDependencies struct {
	Run                func(context.Context, []string, worktrees.CreateOptions) ([]worktrees.CreateResult, error)
	OriginSlug         func(context.Context, string) (string, error)
	PrepareLog         func(string, string, worktrees.WorkLogOptions) (worktrees.WorkLogOptions, error)
	RegisteredIdentity func() (worktrees.AgentIdentity, bool)
	BeforeCreate       func(string, []string) error
	Claim              func(*cobra.Command, bool, string, string) worktreerun.RemoteClaimOutcome
	AfterCreate        func(*cobra.Command, string, []worktrees.CreateResult)
}

// NewCreate constructs the managed worktree creation command.
func NewCreate(environment shared.Runtime, dependencies CreateDependencies) *cobra.Command {
	var branch, branchPrefix, base, format string
	var mode string
	var resume, noClaim bool
	var effortID, runID, initiator, agentID, agentRuntime, model, cli, provider, taskSummary, originalPrompt string
	command := &cobra.Command{
		Use:   "create <task> [owner/repository...]",
		Short: "Create isolated feature branches in the selected checkout root",
		Long: `Create one isolated feature worktree per repository.

WB reads even a dirty or off-base canonical clone without switching or updating any local branch, index, or working tree. It fetches the exact requested base
from origin, then creates each worktree from that verified commit. By default the
checkout is created in the central store at:

  <root>/.worktrees/<task>/<host>/<org>/<repository>

where <root> is the projects root, so every checkout of a multi-repository task
stays together. <host> is the canonical clone's literal forge hostname: its own
on-disk host level when it has one, otherwise the host named by its origin
remote, so a clone still at the legacy <root>/<org>/<repository> path is placed
below its forge without moving. A clone whose origin names no forge (a local
remote) keeps the <org>/<repository> suffix. To place the checkout inside its own
canonical clone instead — for a deployment whose sandbox grants only a single
repository directory — select the repository-local store mode in
~/.config/wb/worktrees.yaml:

  version: 1
  worktrees:
    store: repository-local

That creates <canonical-repository>/.worktrees/<task>. The store mode and, in
central mode, the store root are machine-local user policy: a repository-tracked
.wb/worktrees.yaml may configure branch naming but may not select or override
either, and an attempt to do so is rejected. In central mode worktrees.root
optionally overrides the store root:

  version: 1
  worktrees:
    root: /absolute/shared/root

which creates <root>/<task>/<host>/<org>/<repository>. The private WB state
directory at <root>/.wb remains the authority for Work Logs, locks, and
receipts; WB_HOME is retired, so setting it changes nothing. Existing legacy
worktrees remain guardable, listable, and cleanable during migration, and a
configuration change never moves, hides, or re-selects them.

If no repository is supplied, WB derives owner/repository from the current
checkout's origin remote. Existing branches or worktrees are never reused
unless --resume is explicit.

Resume first recovers its registered branch and active Work Log claim. Current
naming policy cannot replace that recovered identity; WB consults it only when
it creates a new checkout. An exact --branch may assert the recovered branch.
Changing run or agent provenance requires an audited handoff instead of
silently replacing the claim.

--original-prompt-file is mandatory. WB snapshots its exact non-empty bytes
into the private Work Log under <root>/.wb before any worktree is created;
prompt text never enters the worktree projection, source Git, or normal output.

Optionally pass --summary with one short, non-sensitive line that describes
the task for cross-machine overlap checks. It is stored immutably with the
claim and may be published in a machine snapshot; never copy prompt text,
credentials, or customer data into it.

Pass --original-prompt-file - to supply the prompt on stdin instead of a path.
WB reads stdin once, in memory, and writes the private 0600 archive itself
under <root>/.wb; no caller-owned staging file ever exists, so two concurrent
invocations cannot archive each other's prompt by racing on a shared path.
Empty or whitespace-only stdin is refused, and the bytes are never echoed
back to stdout, stderr, or argv.

When the worktree is ready to merge and no conflict or behavioral judgment is
needed, use 'wb worktree merge <worktree...> --route auto --cleanup'. It is the
normal completion counterpart to create: WB selects a permitted direct or PR
route, waits for exact checks, verifies the remote target, synchronizes an
eligible canonical checkout, and cleans the finished branch/worktree.`,
		Example: `# Start one isolated agent task; prompt bytes stay off argv
printf '%s\n' 'the exact task request' | \
  wb worktree create improve-login owner/repository \
  --agent agent-1 --agent-runtime codex --model unknown \
  --original-prompt-file -

# Resume only the exact registered task and branch
wb worktree create improve-login owner/repository --resume \
  --original-prompt-file /private/path/to/original-prompt`,
		Args: func(command *cobra.Command, args []string) error {
			if err := cobra.MinimumNArgs(1)(command, args); err != nil {
				return err
			}
			return shared.ValidateBranchChoice(branch, command.Flags().Changed("branch"), command.Flags().Changed("branch-prefix"))
		},
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			if mode != "" && mode != "auto" && mode != "agent" && mode != "manual" {
				return fmt.Errorf("unsupported execution mode %q; use auto, agent, or manual", mode)
			}
			agentMode := mode == "agent" || (mode == "auto" && (strings.TrimSpace(agentID) != "" || strings.TrimSpace(agentRuntime) != ""))
			if mode == "manual" && strings.TrimSpace(initiator) == "" {
				return fmt.Errorf("manual execution mode requires --initiator so the non-agent mutation is auditable")
			}
			if agentMode {
				if _, ok := dependencies.RegisteredIdentity(); !ok {
					return fmt.Errorf("agent-mode worktree creation requires a live registered session; register before the first mutation with `wb session register --pid $PPID --runtime <harness> --model <model>`, or select --mode manual --initiator <human>")
				}
			}
			workLog := worktrees.WorkLogOptions{
				EffortID: effortID, RunID: runID, Initiator: initiator, AgentID: agentID,
				AgentRuntime: agentRuntime, Model: model, CLI: cli, Provider: provider, OriginalPrompt: originalPrompt,
				TaskSummary:           taskSummary,
				RequireOriginalPrompt: true,
			}
			if originalPrompt == "-" {
				stdinBytes, readErr := io.ReadAll(command.InOrStdin())
				if readErr != nil {
					return fmt.Errorf("read --original-prompt-file - from stdin: %w", readErr)
				}
				var stdinErr error
				workLog, stdinErr = workLog.WithOriginalPromptFromStdin(stdinBytes)
				if stdinErr != nil {
					return stdinErr
				}
			}
			repositories := args[1:]
			if len(repositories) == 0 {
				repository, err := dependencies.OriginSlug(command.Context(), ".")
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
			workLog, err = dependencies.PrepareLog(environment.Flags().ProjectsRoot, args[0], workLog)
			if err != nil {
				return err
			}
			// Prepare snapshots the prompt before hook mutation and supplies a
			// generated fallback identity. On resume, preserve whether effort/run
			// were actually supplied so Create can recover the existing active
			// claim instead of mistaking the local fallback for a handoff.
			if resume && !command.Flags().Changed("effort") {
				workLog.EffortID = ""
			}
			if resume && !command.Flags().Changed("run") {
				workLog.RunID = ""
			}
			if err := dependencies.BeforeCreate(environment.Flags().ProjectsRoot, repositories); err != nil {
				return err
			}
			// In text mode the hook's own printed line on stdout is the
			// record (see the RunE's "text" branch below). The note goes to
			// stderr in every format: it is a diagnostic about a side
			// channel rather than this command's result, so it must not
			// land in the json document or in text stdout. In json mode the
			// outcome also travels structurally in the remote_claim field.
			claimResult := dependencies.Claim(command, noClaim, environment.Flags().ProjectsRoot, args[0])
			results, err := dependencies.Run(command.Context(), repositories, worktrees.CreateOptions{
				ProjectsRoot:       environment.Flags().ProjectsRoot,
				Operation:          args[0],
				Branch:             branch,
				BranchChosen:       command.Flags().Changed("branch"),
				BranchPrefix:       branchPrefix,
				BranchPrefixChosen: command.Flags().Changed("branch-prefix"),
				Base:               base,
				Resume:             resume,
				SessionRequired:    agentMode,
				WorkLog:            workLog,
			})
			if err != nil {
				return err
			}
			// A fresh worktree gets its .worktree.md before the agent that
			// asked for it is told where to go, so the first thing readable at
			// that path already says the checkout is writable and which task
			// it carries. The canonical clone it was cut from is marked in the
			// same pass, because that is the checkout the agent has to be
			// steered away from. Marking is best-effort: a checkout WB just
			// created is not made unusable by a marker it could not write, and
			// the reason goes to stderr rather than into the result document.
			dependencies.AfterCreate(command, base, results)
			return renderCreated(command.OutOrStdout(), format, claimResult, results)
		},
	}
	command.Flags().StringVar(&branch, "branch", "", "exact feature branch (overrides branch-prefix configuration)")
	command.Flags().StringVar(&branchPrefix, "branch-prefix", "", "derive <prefix><task>; an explicit empty value disables configured prefixes")
	command.Flags().StringVar(&base, "base", "main", "canonical and remote base branch")
	command.Flags().StringVar(&mode, "mode", "auto", "execution mode: auto, agent (requires a live registered session), or manual (requires --initiator)")
	command.Flags().BoolVar(&resume, "resume", false, "reuse only the exact expected branch and worktree")
	command.Flags().BoolVar(&noClaim, "no-claim", false, "skip the best-effort fleet-wide remote claim for this task")
	command.Flags().StringVar(&effortID, "effort", "", "stable Synchestra/WB effort id (default task)")
	command.Flags().StringVar(&runID, "run", "", "agent run id (default generated locally)")
	command.Flags().StringVar(&initiator, "initiator", "", "human or agent that started the effort")
	command.Flags().StringVar(&agentID, "agent", "", "agent identity")
	command.Flags().StringVar(&agentRuntime, "agent-runtime", "", "agent runtime, e.g. codex or claude")
	command.Flags().StringVar(&model, "model", "", "required exact child model identifier, or explicit unknown; WB never guesses")
	command.Flags().StringVar(&cli, "cli", "", "optional invoking CLI/client identifier, supplied only when known")
	command.Flags().StringVar(&provider, "provider", "", "optional routing/billing provider identifier, never a credential")
	command.Flags().StringVar(&taskSummary, "summary", "", "optional short non-sensitive task summary for overlap checks; never prompt text or credentials")
	command.Flags().StringVar(&originalPrompt, "original-prompt-file", "", "required readable non-empty file containing the exact original prompt, or - to read it from stdin; archived under the WB state directory only")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}
