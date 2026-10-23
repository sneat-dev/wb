package cmdworktree

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/continuationinput"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"io"
	"os"
	"strings"
)

type JournalAdmission func(*cobra.Command, bool) (worktrees.AgentIdentity, func(), error)

type JournalOperations struct {
	Load       func(context.Context, worktrees.LoadWorkLogOptions) (worktrees.WorkLogView, error)
	Show       func(context.Context, string, string) (worktrees.WorkLogView, worktrees.LocalWorkLogProjection, error)
	Init       func(context.Context, worktrees.LogInitOptions) (worktrees.LogVerbResult, error)
	Steer      func(context.Context, worktrees.LogSteerOptions) (worktrees.LogVerbResult, error)
	Checkpoint func(context.Context, worktrees.LogCheckpointOptions) (worktrees.LogVerbResult, error)
	Refresh    func(context.Context, worktrees.LogRefreshOptions) (worktrees.LogVerbResult, error)
	Integrate  func(context.Context, worktrees.LogIntegrateOptions) (worktrees.LogVerbResult, error)
	Handoff    func(context.Context, worktrees.LogHandoffOptions) (worktrees.LogVerbResult, error)
	Recover    func(context.Context, worktrees.LogRecoverOptions) (worktrees.LogVerbResult, error)
	Finalize   func(context.Context, worktrees.LogFinalizeOptions) (worktrees.LogVerbResult, error)
	Sync       func(context.Context, worktrees.LogSyncOptions) (worktrees.LogVerbResult, error)
	Archive    func(context.Context, worktrees.LogArchiveOptions) (worktrees.LogVerbResult, error)
}

// NewCheckpointFetch constructs the worktree journal command.
// NewCheckpointFetch constructs the checkpoint retrieval command.
func NewCheckpointFetch(fetch func(context.Context, worktrees.FetchRemoteCheckpointOptions) (worktrees.RemoteCheckpointFetchResult, error)) *cobra.Command {
	var task, format string
	command := &cobra.Command{
		Use:   "checkpoint-fetch [worktree-path]",
		Short: "Fetch another machine's remote checkpoint (refs/wb/checkpoints/<task>)",
		Long: `Fetch origin's refs/wb/checkpoints/<task> into the same-named local ref.

This is the cross-machine retrieval side of 'wb worktree log checkpoint
--skip-remote=false' (the default): another machine's checkpoint arrives as a
local refs/wb/checkpoints/<task> ref, never as a branch and never checked out
automatically. Deciding what to do with it -- inspect it, branch from it,
build a worktree on it -- is left to the caller.

A fetched checkpoint is NOT a landing receipt: it proves a commit reached the
remote, never that it merged anywhere. Land work only by merging it to its
target branch.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			if strings.TrimSpace(task) == "" {
				return fmt.Errorf("--task is required")
			}
			result, err := fetch(command.Context(), worktrees.FetchRemoteCheckpointOptions{
				Root: journalPath(args), Task: task,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(result)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "fetched %s at %s into local ref %s\n%s\n",
				result.Ref, result.SHA, result.LocalRef, result.Notice)
			return err
		},
	}
	command.Flags().StringVar(&task, "task", "", "task slug whose checkpoint ref to fetch (refs/wb/checkpoints/<task>)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

// NewWorkLog constructs the worktree journal command.
func NewWorkLog(environment shared.Runtime, operations JournalOperations, admit JournalAdmission) *cobra.Command {
	var format string
	command := &cobra.Command{
		Use:     "log [worktree-path]",
		Aliases: []string{"worklog", "work-log"},
		Short:   "Dump or mutate the local work log for one worktree",
		Long: `Bare 'wb worktree log' dumps the private local journal an agent needs to resume.

Mutating verbs live under this command:
  init, steer, show, checkpoint, refresh, integrate, handoff, recover,
  finalize, sync, archive

Prompt bodies are private local data. The bare dump includes the exact original prompt
and later steering bodies deliberately for agent bootstrap; 'log show' stays
redacted. Do not pipe private dumps into source Git, public reports, or
Synchestra envelopes.

Mutation verbs require no extra flag in auto mode for compatibility. Select
--mode agent only with a live registered session; select --mode manual with
--initiator <human> for an explicit audited operator action. Recover and
archive remain read-only until --apply.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			view, err := operations.Load(command.Context(), worktrees.LoadWorkLogOptions{
				ProjectsRoot:        environment.Flags().ProjectsRoot,
				Worktree:            path,
				IncludePromptBodies: true,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(view)
			}
			_, err = io.WriteString(command.OutOrStdout(), worktrees.FormatWorkLogViewText(view))
			return err
		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.PersistentFlags().String("mode", "auto", "execution mode for mutation verbs: auto, agent (requires a live registered session), or manual (requires --initiator)")
	command.PersistentFlags().String("initiator", "", "human or agent that authorized a manual Work Log mutation")
	command.AddCommand(
		newJournalInit(environment, operations, admit),
		newJournalSteer(environment, operations, admit),
		newJournalShow(environment, operations, admit),
		newJournalCheckpoint(environment, operations, admit),
		newJournalRefresh(environment, operations, admit),
		newJournalIntegrate(environment, operations, admit),
		newJournalHandoff(environment, operations, admit),
		newJournalRecover(environment, operations, admit),
		newJournalFinalize(environment, operations, admit),
		newJournalSync(environment, operations, admit),
		newJournalArchive(environment, operations, admit),
	)
	return command
}

func newJournalInit(environment shared.Runtime, operations JournalOperations, admit JournalAdmission) *cobra.Command {
	var prompt, promptFile, source, runtime, agentID, model, cli, provider, format string
	command := &cobra.Command{
		Use:   "init [worktree-path]",
		Short: "Initialize or attach the local work-log journal",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := admit(command, true)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			var body []byte
			if prompt != "" || promptFile != "" {
				var err error
				body, err = readPromptBody(prompt, promptFile)
				if err != nil {
					return err
				}
			}
			result, err := operations.Init(command.Context(), worktrees.LogInitOptions{
				ProjectsRoot: environment.Flags().ProjectsRoot, Worktree: journalPath(args),
				Prompt: body, Source: source, Runtime: runtime, Model: model, CLI: cli, Provider: provider,
				AgentID: agentID,
			})
			if err != nil {
				return err
			}
			return writeJournalResult(command.OutOrStdout(), format, result)
		},
	}
	command.Flags().StringVar(&prompt, "prompt", "", "optional initial instruction when none exists")
	command.Flags().StringVar(&promptFile, "prompt-file", "", "read optional initial instruction from a file")
	command.Flags().StringVar(&source, "source", "", "prompt source when recording --prompt")
	command.Flags().StringVar(&runtime, "agent-runtime", "", "recording runtime, when known")
	command.Flags().StringVar(&agentID, "agent", "", "agent identity when known")
	command.Flags().StringVar(&model, "model", "", "recording model, when known")
	command.Flags().StringVar(&cli, "cli", "", "recording CLI identifier, when known")
	command.Flags().StringVar(&provider, "provider", "", "routing/billing provider identifier, never a credential")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newJournalSteer(environment shared.Runtime, operations JournalOperations, admit JournalAdmission) *cobra.Command {
	var prompt, promptFile, source, runtime, model, cli, provider, format string
	command := &cobra.Command{
		Use:   "steer [worktree-path]",
		Short: "Append the next steering prompt to the local journal",
		Long: `Agent-facing alias of recording the next prompt ordinal.

Defaults source to agent_declared. Humans should prefer 'wb worktree set --prompt',
which records human_declared.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := admit(command, true)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			body, err := readPromptBody(prompt, promptFile)
			if err != nil {
				return err
			}
			if source == "" {
				source = worktrees.PromptSourceAgent
			}
			result, err := operations.Steer(command.Context(), worktrees.LogSteerOptions{
				ProjectsRoot: environment.Flags().ProjectsRoot, Worktree: journalPath(args),
				Body: body, Source: source, Runtime: runtime, Model: model, CLI: cli, Provider: provider,
			})
			if err != nil {
				return err
			}
			return writeJournalResult(command.OutOrStdout(), format, result)
		},
	}
	command.Flags().StringVar(&prompt, "prompt", "", "exact steering instruction")
	command.Flags().StringVar(&promptFile, "prompt-file", "", "read the exact instruction from a file")
	command.Flags().StringVar(&source, "source", "", "harness_observed, agent_declared, or human_declared")
	command.Flags().StringVar(&runtime, "agent-runtime", "", "recording runtime, when known")
	command.Flags().StringVar(&model, "model", "", "recording model, when known")
	command.Flags().StringVar(&cli, "cli", "", "recording CLI identifier, when known")
	command.Flags().StringVar(&provider, "provider", "", "routing/billing provider identifier, never a credential")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newJournalShow(environment shared.Runtime, operations JournalOperations, admit JournalAdmission) *cobra.Command {
	var format string
	command := &cobra.Command{
		Use:   "show [worktree-path]",
		Short: "Show the redacted local work log without prompt bodies",
		Long: `Show redacted journal state and live Git evidence.

Prompt bodies are omitted. Use bare 'wb worktree log' when an agent needs the
exact private original prompt.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			view, projection, err := operations.Show(command.Context(), environment.Flags().ProjectsRoot, journalPath(args))
			if err != nil {
				return err
			}
			if format == "json" {
				return json.NewEncoder(command.OutOrStdout()).Encode(map[string]any{
					"view": view, "projection": projection,
				})
			}
			_, err = io.WriteString(command.OutOrStdout(), worktrees.FormatWorktreeInfoText(view))
			return err
		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newJournalCheckpoint(environment shared.Runtime, operations JournalOperations, admit JournalAdmission) *cobra.Command {
	var message, nextAction, usageDisc, currency, providerRef, format string
	var inputTokens, outputTokens int64
	var estimatedCost float64
	var haveInput, haveOutput, haveCost, skipRemote bool
	command := &cobra.Command{
		Use:   "checkpoint [worktree-path]",
		Short: "Append a progress checkpoint with observed Git evidence",
		Long: `Append a progress checkpoint with observed Git evidence to the local Work Log.

Unless --skip-remote is given, this also force-pushes the exact current HEAD
to refs/wb/checkpoints/<task> at origin: a fast, Tier-0-only persistence path
that never runs lint or test and never triggers CI. A remote checkpoint is
NOT a landing receipt -- it proves a commit reached the remote, never that it
merged anywhere. Work is landed only when it is merged and pushed to its
target branch on origin. Retrieve a checkpoint from another machine with
'wb worktree checkpoint-fetch --task <task>'.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := admit(command, true)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			var inPtr, outPtr *int64
			var costPtr *float64
			if haveInput {
				inPtr = &inputTokens
			}
			if haveOutput {
				outPtr = &outputTokens
			}
			if haveCost {
				costPtr = &estimatedCost
			}
			result, err := operations.Checkpoint(command.Context(), worktrees.LogCheckpointOptions{
				ProjectsRoot: environment.Flags().ProjectsRoot, Worktree: journalPath(args),
				Message: message, NextAction: nextAction, UsageDisc: usageDisc,
				InputTokens: inPtr, OutputTokens: outPtr, EstimatedCost: costPtr,
				Currency: currency, ProviderRef: providerRef, SkipRemote: skipRemote,
			})
			if err != nil {
				return err
			}
			return writeJournalResult(command.OutOrStdout(), format, result)
		},
	}
	command.Flags().StringVar(&message, "message", "", "checkpoint progress message")
	command.Flags().StringVar(&nextAction, "next-action", "", "bounded next action")
	command.Flags().StringVar(&usageDisc, "usage-discriminator", "", "provider_reported, estimated, or unavailable")
	command.Flags().Int64Var(&inputTokens, "input-tokens", 0, "optional input token count")
	command.Flags().Int64Var(&outputTokens, "output-tokens", 0, "optional output token count")
	command.Flags().Float64Var(&estimatedCost, "estimated-cost", 0, "optional estimated cost")
	command.Flags().StringVar(&currency, "currency", "", "optional currency for estimated cost")
	command.Flags().StringVar(&providerRef, "provider-ref", "", "optional provider usage reference")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&skipRemote, "skip-remote", false, "record the local checkpoint only; do not push refs/wb/checkpoints/<task>")
	command.PreRun = func(cmd *cobra.Command, _ []string) {
		haveInput = cmd.Flags().Changed("input-tokens")
		haveOutput = cmd.Flags().Changed("output-tokens")
		haveCost = cmd.Flags().Changed("estimated-cost")
	}
	return command
}

func newJournalRefresh(environment shared.Runtime, operations JournalOperations, admit JournalAdmission) *cobra.Command {
	var base, format string
	command := &cobra.Command{
		Use:   "refresh [worktree-path]",
		Short: "Fetch and measure target divergence without changing the worktree",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := admit(command, true)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			result, err := operations.Refresh(command.Context(), worktrees.LogRefreshOptions{
				ProjectsRoot: environment.Flags().ProjectsRoot, Worktree: journalPath(args), Base: base,
			})
			if err != nil {
				return err
			}
			return writeJournalResult(command.OutOrStdout(), format, result)
		},
	}
	command.Flags().StringVar(&base, "base", "", "target base branch (default: manifest base or main)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newJournalIntegrate(environment shared.Runtime, operations JournalOperations, admit JournalAdmission) *cobra.Command {
	var base, strategy, format string
	command := &cobra.Command{
		Use:   "integrate [worktree-path]",
		Short: "Integrate the fetched target at a clean checkpoint",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := admit(command, true)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			result, err := operations.Integrate(command.Context(), worktrees.LogIntegrateOptions{
				ProjectsRoot: environment.Flags().ProjectsRoot, Worktree: journalPath(args), Base: base, Strategy: strategy,
			})
			if err != nil {
				return err
			}
			return writeJournalResult(command.OutOrStdout(), format, result)
		},
	}
	command.Flags().StringVar(&base, "base", "", "target base branch (default: manifest base or main)")
	command.Flags().StringVar(&strategy, "strategy", "auto", "rebase, merge, or auto")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newJournalHandoff(environment shared.Runtime, operations JournalOperations, admit JournalAdmission) *cobra.Command {
	var summary, nextAction, successor, model, cli, provider, format string
	var apply bool
	command := &cobra.Command{
		Use:   "handoff [worktree-path]",
		Short: "Record a durable handoff offer and optionally transfer the claim",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := admit(command, true)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			result, err := operations.Handoff(command.Context(), worktrees.LogHandoffOptions{
				ProjectsRoot: environment.Flags().ProjectsRoot, Worktree: journalPath(args),
				Summary: summary, NextAction: nextAction, Successor: successor,
				Model: model, CLI: cli, Provider: provider, Apply: apply,
			})
			if err != nil {
				return err
			}
			return writeJournalResult(command.OutOrStdout(), format, result)
		},
	}
	command.Flags().StringVar(&summary, "summary", "", "required bounded handoff summary")
	command.Flags().StringVar(&nextAction, "next-action", "", "bounded next action for the successor")
	command.Flags().StringVar(&successor, "successor", "", "required successor agent/session ID")
	command.Flags().StringVar(&model, "model", "", "required with --apply: successor model or unknown")
	command.Flags().StringVar(&cli, "cli", "", "optional successor CLI")
	command.Flags().StringVar(&provider, "provider", "", "optional successor provider, never a credential")
	command.Flags().BoolVar(&apply, "apply", false, "transfer the hybrid claim after recording the offer")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newJournalRecover(environment shared.Runtime, operations JournalOperations, admit JournalAdmission) *cobra.Command {
	var actor, format, reconcileBranch, expectedHead, reason, eventID string
	var apply, establishClaim, takeover, remote bool
	command := &cobra.Command{
		Use:   "recover [worktree-path]",
		Short: "Diagnose and rebuild derived work-log state",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := admit(command, apply)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			result, err := operations.Recover(command.Context(), worktrees.LogRecoverOptions{
				ProjectsRoot: environment.Flags().ProjectsRoot, Worktree: journalPath(args),
				Apply: apply, EstablishClaim: establishClaim, Takeover: takeover, Actor: actor,
				ReconcileBranch: reconcileBranch, ExpectedHead: expectedHead, Remote: remote,
				Reason: reason, EventID: eventID,
			})
			if err != nil {
				return err
			}
			return writeJournalResult(command.OutOrStdout(), format, result)
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "rewrite projection.json from journal evidence")
	command.Flags().BoolVar(&establishClaim, "establish-claim", false, "with --apply, recover a missing private claim from a blank-ClaimID immutable campaign manifest")
	command.Flags().BoolVar(&takeover, "takeover", false, "with --apply, append an explicit takeover event")
	command.Flags().StringVar(&actor, "actor", "", "required with --takeover")
	command.Flags().StringVar(&reconcileBranch, "reconcile-branch", "", "live branch to reconcile back to the immutable Work Log claim")
	command.Flags().StringVar(&expectedHead, "expected-head", "", "exact live HEAD required for branch reconciliation")
	command.Flags().BoolVar(&remote, "remote", false, "require and retire the exact remote claim branch during reconciliation")
	command.Flags().StringVar(&reason, "reason", "", "auditable reason required for branch reconciliation")
	command.Flags().StringVar(&eventID, "event-id", "", "stable idempotency key required for branch reconciliation")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newJournalFinalize(environment shared.Runtime, operations JournalOperations, admit JournalAdmission) *cobra.Command {
	var resultValue, message, format, reportFile string
	var apply, reportStdin bool
	command := &cobra.Command{
		Use:   "finalize [worktree-path]",
		Short: "Record a terminal result and optionally seal the claim",
		Long: `Record a terminal result and, with --apply, seal the Hybrid claim.

--report <path> or --report-stdin attaches an agent's full completion report
(Markdown, typically) to the sealed terminal. WB copies the body into the
private Work Log store under the WB state directory -- never into source Git --
at a deterministic path, and records report_path, terminal_result,
terminal_message, and finalized_at on the terminal and its outbox receipt.
'wb worktree list'/'summary'/'log show' then let a lead session read that a
lane finished and where its report lives without agreeing on an arbitrary
path. The report body itself stays private local data, read back only by the
bare 'wb worktree log' dump, exactly like an original prompt body. A report
over 1 MiB is refused. Storing the report requires --apply; without it the
report is accepted but not persisted.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			if reportFile != "" && reportStdin {
				return environment.ExitError(shared.ExitUsage, "supply at most one of --report or --report-stdin")
			}
			_, releaseAdmission, err := admit(command, true)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			var report []byte
			switch {
			case reportStdin:
				report, err = continuationinput.ReadBounded(command.InOrStdin(), worktrees.MaxFinalizeReportBytes, "finalize report")
				if err != nil {
					return err
				}
			case reportFile != "":
				report, err = os.ReadFile(reportFile)
				if err != nil {
					return fmt.Errorf("read report file: %w", err)
				}
				if len(report) > worktrees.MaxFinalizeReportBytes {
					return fmt.Errorf("finalize report exceeds %d bytes (%d MiB cap)", worktrees.MaxFinalizeReportBytes, worktrees.MaxFinalizeReportBytes/(1<<20))
				}
			}
			result, err := operations.Finalize(command.Context(), worktrees.LogFinalizeOptions{
				ProjectsRoot: environment.Flags().ProjectsRoot, Worktree: journalPath(args),
				Result: resultValue, Message: message, Apply: apply, Report: report,
			})
			if err != nil {
				return err
			}
			return writeJournalResult(command.OutOrStdout(), format, result)
		},
	}
	command.Flags().StringVar(&resultValue, "result", "success", "success or failure")
	command.Flags().StringVar(&message, "message", "", "terminal message")
	command.Flags().BoolVar(&apply, "apply", false, "seal the hybrid claim terminal")
	command.Flags().StringVar(&reportFile, "report", "", "path to a Markdown completion report to attach (requires --apply to persist)")
	command.Flags().BoolVar(&reportStdin, "report-stdin", false, "read the completion report from stdin (requires --apply to persist)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newJournalSync(environment shared.Runtime, operations JournalOperations, admit JournalAdmission) *cobra.Command {
	var format string
	var apply bool
	command := &cobra.Command{
		Use:   "sync [worktree-path]",
		Short: "Drain local outbox to Synchestra when configured",
		Long: `Attempt authoritative sync. Without a configured Synchestra endpoint the
command stays offline, retains the local outbox, and records a sync_attempt.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := admit(command, true)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			result, err := operations.Sync(command.Context(), worktrees.LogSyncOptions{
				ProjectsRoot: environment.Flags().ProjectsRoot, Worktree: journalPath(args), Apply: apply,
			})
			if err != nil {
				return err
			}
			return writeJournalResult(command.OutOrStdout(), format, result)
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "attempt drain (still offline without Synchestra)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newJournalArchive(environment shared.Runtime, operations JournalOperations, admit JournalAdmission) *cobra.Command {
	var format string
	var apply, force bool
	command := &cobra.Command{
		Use:   "archive [worktree-path]",
		Short: "Archive a finalized local journal into the WB state directory",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := admit(command, apply)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			result, err := operations.Archive(command.Context(), worktrees.LogArchiveOptions{
				ProjectsRoot: environment.Flags().ProjectsRoot, Worktree: journalPath(args), Apply: apply, Force: force,
			})
			if err != nil {
				return err
			}
			return writeJournalResult(command.OutOrStdout(), format, result)
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "copy .wb/local into <root>/.wb/worklogs")
	command.Flags().BoolVar(&force, "force", false, "override terminal/seven-day gates")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

// NewCorrectIdentity constructs the worktree journal command.
func NewCorrectIdentity(environment shared.Runtime, correct func(worktrees.CorrectExecutionIdentityOptions) (worktrees.ExecutionIdentityCorrectionResult, error), admit JournalAdmission, initiator func(*cobra.Command) string) *cobra.Command {
	var model, cli, provider, actor, reason, eventID, format string
	command := &cobra.Command{
		Use:   "correct-identity <effort> <run> <claim-id>",
		Short: "Append an audited execution-identity correction to one Work Log claim",
		Long: `Correct execution identity only through an immutable, append-only Work Log event.
Address the durable effort/run/claim identity, not a live worktree, so a correction
also works after terminal cleanup. Pass a stable --event-id so retries are
idempotent. Select each field to replace: --model accepts an exact model or
explicit unknown; --cli= and --provider= clear their optional values. WB never
guesses model, CLI, or provider. Provider is a routing/billing identifier only,
never a token, credential, or secret. This Work Log mutation requires a live
registered session in --mode agent, or --mode manual with --initiator <human>.`,
		Args: cobra.ExactArgs(3),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := admit(command, true)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			var modelValue, cliValue, providerValue *string
			if command.Flags().Changed("model") {
				modelValue = &model
			}
			if command.Flags().Changed("cli") {
				cliValue = &cli
			}
			if command.Flags().Changed("provider") {
				providerValue = &provider
			}
			result, err := correct(worktrees.CorrectExecutionIdentityOptions{
				ProjectsRoot: environment.Flags().ProjectsRoot, EffortID: args[0], RunID: args[1], ClaimID: args[2], EventID: eventID,
				Actor: actor, Reason: reason, Initiator: initiator(command), Model: modelValue, CLI: cliValue, Provider: providerValue,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				return json.NewEncoder(command.OutOrStdout()).Encode(result)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "corrected execution identity for claim %s with event %s\n", result.ClaimID, result.CorrectionID)
			return err
		},
	}
	command.Flags().StringVar(&model, "model", "", "replacement exact child model or explicit unknown")
	command.Flags().StringVar(&cli, "cli", "", "replacement invoking CLI; use --cli= to clear")
	command.Flags().StringVar(&provider, "provider", "", "replacement routing/billing provider; use --provider= to clear")
	command.Flags().StringVar(&actor, "actor", "", "required person or agent making the correction")
	command.Flags().StringVar(&reason, "reason", "", "required audit reason")
	command.Flags().StringVar(&eventID, "event-id", "", "required stable correction event ID for idempotent retry")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

// NewSet constructs the worktree journal command.
func NewSet(environment shared.Runtime, steer func(context.Context, worktrees.LogSteerOptions) (worktrees.LogVerbResult, error)) *cobra.Command {
	var prompt, promptFile, runtime, model, cli, provider string
	command := &cobra.Command{
		Use:   "set [worktree-path]",
		Short: "Record an instruction that directed this worktree's effort",
		Long: `Append one instruction to this worktree's ordered prompt sequence.

This is the human-facing alias of 'wb worktree log steer'. It records a
human_declared prompt at the next ordinal, which is what the commit admission
gate asks for when it refuses a commit in a worktree with no recorded
instruction.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			body, err := readPromptBody(prompt, promptFile)
			if err != nil {
				return err
			}
			result, err := steer(command.Context(), worktrees.LogSteerOptions{
				ProjectsRoot: environment.Flags().ProjectsRoot, Worktree: path, Body: body,
				Source: worktrees.PromptSourceHuman, Runtime: runtime, Model: model, CLI: cli, Provider: provider,
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "recorded %s\n", result.Prompt)
			return err
		},
	}
	command.Flags().StringVar(&prompt, "prompt", "", "the exact instruction to record")
	command.Flags().StringVar(&promptFile, "prompt-file", "", "read the exact instruction from a file instead")
	command.Flags().StringVar(&runtime, "agent-runtime", "", "recording runtime, when known")
	command.Flags().StringVar(&model, "model", "", "recording model, when known")
	command.Flags().StringVar(&cli, "cli", "", "recording CLI identifier, when known")
	command.Flags().StringVar(&provider, "provider", "", "routing/billing provider identifier, never a credential")
	return command
}
