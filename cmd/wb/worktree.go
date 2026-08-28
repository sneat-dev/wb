package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// autoReleaseWriter selects the output stream for auto-release lines based on
// format. For json output, the release line is informational and belongs on
// stderr to keep stdout as pure JSON (matching worktree create's pattern of
// routing auto-claim output to io.Discard in json mode). For text output,
// stdout is correct.
// remoteClaimWriter returns the stream remote-claim notes belong on.
//
// They are diagnostics about a side channel, not the result of the command the
// user asked for, and wb's contract puts diagnostics on stderr. Keeping them
// off stdout means a json document is never corrupted, a text result is never
// padded with unrelated lines, and a command that fails writes nothing to
// stdout at all.
func remoteClaimWriter(cmd *cobra.Command) io.Writer {
	return cmd.ErrOrStderr()
}

func newWorktreeCmd() *cobra.Command {
	command := &cobra.Command{
		Use:     "worktree",
		Aliases: []string{"worktrees", "wt"},
		Short:   "Create, inspect, merge, and safely retire isolated agent work",
	}
	command.AddGroup(
		&cobra.Group{ID: "start", Title: "Start work"},
		&cobra.Group{ID: "finish", Title: "Finish work"},
		&cobra.Group{ID: "inspect", Title: "Inspect progress"},
		&cobra.Group{ID: "recover", Title: "Recover and coordinate"},
		&cobra.Group{ID: "admin", Title: "Administration"},
	)
	children := []struct {
		command *cobra.Command
		group   string
	}{
		{newWorktreeCreateCmd(), "start"},
		{newWorktreeAdoptCmd(), "start"},
		{newWorktreeMergeCmd(), "finish"},
		{newWorktreeCleanupCmd(), "finish"},
		{newWorktreeAbortCmd(), "finish"},
		{newWorktreeSummaryCmd(), "inspect"},
		{newWorktreeInfoCmd(), "inspect"},
		{newWorktreeListCmd(), "inspect"},
		{newWorktreeGuardCmd(), "recover"},
		{newWorktreeRescueCmd(), "recover"},
		{newWorktreeWorkLogCmd(), "recover"},
		{newWorktreeCheckpointFetchCmd(), "recover"},
		{newWorktreeOwnCmd(), "recover"},
		{newWorktreeMarkerCmd(), "admin"},
		{newWorktreeRenameCmd(), "admin"},
		{newWorktreeCorrectIdentityCmd(), "admin"},
		{newWorktreeSetCmd(), "admin"},
		{newWorktreeOrphansCmd(), "admin"},
		{newWorktreeBackfillCmd(), "admin"},
	}
	for _, child := range children {
		child.command.GroupID = child.group
		command.AddCommand(child.command)
	}
	return command
}

func newWorktreeCheckpointFetchCmd() *cobra.Command {
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
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			if strings.TrimSpace(task) == "" {
				return fmt.Errorf("--task is required")
			}
			result, err := worktrees.FetchRemoteCheckpoint(command.Context(), worktrees.FetchRemoteCheckpointOptions{
				Root: worktreeLogPath(args), Task: task,
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

func newWorktreeInfoCmd() *cobra.Command {
	var format string
	command := &cobra.Command{
		Use:   "info [worktree-path]",
		Short: "Show redacted identity and Git state for one worktree",
		Long: `Print a safe, redacted summary of one worktree's journal and live Git state.

Includes manifest, claim identity, prompt ordinals and digests, and dirty/head
evidence. Prompt bodies are never printed — use 'wb worktree log' when an agent
needs the exact original prompt and steering instructions.

Default text is human-readable. --format json emits the same redacted payload
as one JSON document on stdout.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			view, err := worktrees.LoadWorkLogView(command.Context(), worktrees.LoadWorkLogOptions{
				ProjectsRoot:        projectsRoot,
				Worktree:            path,
				IncludePromptBodies: false,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(view)
			}
			_, err = io.WriteString(command.OutOrStdout(), worktrees.FormatWorktreeInfoText(view))
			return err
		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newWorktreeWorkLogCmd() *cobra.Command {
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
Synchestra envelopes.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			view, err := worktrees.LoadWorkLogView(command.Context(), worktrees.LoadWorkLogOptions{
				ProjectsRoot:        projectsRoot,
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
	command.AddCommand(
		newWorktreeLogInitCmd(),
		newWorktreeLogSteerCmd(),
		newWorktreeLogShowCmd(),
		newWorktreeLogCheckpointCmd(),
		newWorktreeLogRefreshCmd(),
		newWorktreeLogIntegrateCmd(),
		newWorktreeLogHandoffCmd(),
		newWorktreeLogRecoverCmd(),
		newWorktreeLogFinalizeCmd(),
		newWorktreeLogSyncCmd(),
		newWorktreeLogArchiveCmd(),
	)
	return command
}

func encodeLogVerbResult(command *cobra.Command, format string, result worktrees.LogVerbResult) error {
	if format == "json" {
		encoder := json.NewEncoder(command.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	_, err := fmt.Fprintf(command.OutOrStdout(), "%s %s applied=%t", result.Verb, result.Worktree, result.Applied)
	if err != nil {
		return err
	}
	if result.Prompt != "" {
		if _, err := fmt.Fprintf(command.OutOrStdout(), " prompt=%s", result.Prompt); err != nil {
			return err
		}
	}
	if result.Event != nil {
		if _, err := fmt.Fprintf(command.OutOrStdout(), " event=%s#%d", result.Event.Type, result.Event.Seq); err != nil {
			return err
		}
	}
	if result.Offline {
		if _, err := fmt.Fprintf(command.OutOrStdout(), " offline outbox=%d", result.Outbox); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(command.OutOrStdout()); err != nil {
		return err
	}
	for _, note := range result.Notes {
		if _, err := fmt.Fprintf(command.OutOrStdout(), "- %s\n", note); err != nil {
			return err
		}
	}
	for _, line := range result.Diagnosis {
		if _, err := fmt.Fprintf(command.OutOrStdout(), "diagnosis: %s\n", line); err != nil {
			return err
		}
	}
	return nil
}

func worktreeLogPath(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	return "."
}

func newWorktreeLogInitCmd() *cobra.Command {
	var prompt, promptFile, source, runtime, agentID, model, cli, provider, format string
	command := &cobra.Command{
		Use:   "init [worktree-path]",
		Short: "Initialize or attach the local work-log journal",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			var body []byte
			if prompt != "" || promptFile != "" {
				var err error
				body, err = readPromptBody(prompt, promptFile)
				if err != nil {
					return err
				}
			}
			result, err := worktrees.LogInit(command.Context(), worktrees.LogInitOptions{
				ProjectsRoot: projectsRoot, Worktree: worktreeLogPath(args),
				Prompt: body, Source: source, Runtime: runtime, Model: model, CLI: cli, Provider: provider,
				AgentID: agentID,
			})
			if err != nil {
				return err
			}
			return encodeLogVerbResult(command, format, result)
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

func newWorktreeLogSteerCmd() *cobra.Command {
	var prompt, promptFile, source, runtime, model, cli, provider, format string
	command := &cobra.Command{
		Use:   "steer [worktree-path]",
		Short: "Append the next steering prompt to the local journal",
		Long: `Agent-facing alias of recording the next prompt ordinal.

Defaults source to agent_declared. Humans should prefer 'wb worktree set --prompt',
which records human_declared.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			body, err := readPromptBody(prompt, promptFile)
			if err != nil {
				return err
			}
			if source == "" {
				source = worktrees.PromptSourceAgent
			}
			result, err := worktrees.LogSteer(command.Context(), worktrees.LogSteerOptions{
				ProjectsRoot: projectsRoot, Worktree: worktreeLogPath(args),
				Body: body, Source: source, Runtime: runtime, Model: model, CLI: cli, Provider: provider,
			})
			if err != nil {
				return err
			}
			return encodeLogVerbResult(command, format, result)
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

func newWorktreeLogShowCmd() *cobra.Command {
	var format string
	command := &cobra.Command{
		Use:   "show [worktree-path]",
		Short: "Show the redacted local work log without prompt bodies",
		Long: `Show redacted journal state and live Git evidence.

Prompt bodies are omitted. Use bare 'wb worktree log' when an agent needs the
exact private original prompt.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			view, projection, err := worktrees.LogShow(command.Context(), projectsRoot, worktreeLogPath(args))
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

func newWorktreeLogCheckpointCmd() *cobra.Command {
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
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
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
			result, err := worktrees.LogCheckpoint(command.Context(), worktrees.LogCheckpointOptions{
				ProjectsRoot: projectsRoot, Worktree: worktreeLogPath(args),
				Message: message, NextAction: nextAction, UsageDisc: usageDisc,
				InputTokens: inPtr, OutputTokens: outPtr, EstimatedCost: costPtr,
				Currency: currency, ProviderRef: providerRef, SkipRemote: skipRemote,
			})
			if err != nil {
				return err
			}
			return encodeLogVerbResult(command, format, result)
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

func newWorktreeLogRefreshCmd() *cobra.Command {
	var base, format string
	command := &cobra.Command{
		Use:   "refresh [worktree-path]",
		Short: "Fetch and measure target divergence without changing the worktree",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			result, err := worktrees.LogRefresh(command.Context(), worktrees.LogRefreshOptions{
				ProjectsRoot: projectsRoot, Worktree: worktreeLogPath(args), Base: base,
			})
			if err != nil {
				return err
			}
			return encodeLogVerbResult(command, format, result)
		},
	}
	command.Flags().StringVar(&base, "base", "", "target base branch (default: manifest base or main)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newWorktreeLogIntegrateCmd() *cobra.Command {
	var base, strategy, format string
	command := &cobra.Command{
		Use:   "integrate [worktree-path]",
		Short: "Integrate the fetched target at a clean checkpoint",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			result, err := worktrees.LogIntegrate(command.Context(), worktrees.LogIntegrateOptions{
				ProjectsRoot: projectsRoot, Worktree: worktreeLogPath(args), Base: base, Strategy: strategy,
			})
			if err != nil {
				return err
			}
			return encodeLogVerbResult(command, format, result)
		},
	}
	command.Flags().StringVar(&base, "base", "", "target base branch (default: manifest base or main)")
	command.Flags().StringVar(&strategy, "strategy", "auto", "rebase, merge, or auto")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newWorktreeLogHandoffCmd() *cobra.Command {
	var summary, nextAction, successor, model, cli, provider, format string
	var apply bool
	command := &cobra.Command{
		Use:   "handoff [worktree-path]",
		Short: "Record a durable handoff offer and optionally transfer the claim",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			result, err := worktrees.LogHandoff(command.Context(), worktrees.LogHandoffOptions{
				ProjectsRoot: projectsRoot, Worktree: worktreeLogPath(args),
				Summary: summary, NextAction: nextAction, Successor: successor,
				Model: model, CLI: cli, Provider: provider, Apply: apply,
			})
			if err != nil {
				return err
			}
			return encodeLogVerbResult(command, format, result)
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

func newWorktreeLogRecoverCmd() *cobra.Command {
	var actor, format string
	var apply, takeover bool
	command := &cobra.Command{
		Use:   "recover [worktree-path]",
		Short: "Diagnose and rebuild derived work-log state",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			result, err := worktrees.LogRecover(command.Context(), worktrees.LogRecoverOptions{
				ProjectsRoot: projectsRoot, Worktree: worktreeLogPath(args),
				Apply: apply, Takeover: takeover, Actor: actor,
			})
			if err != nil {
				return err
			}
			return encodeLogVerbResult(command, format, result)
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "rewrite projection.json from journal evidence")
	command.Flags().BoolVar(&takeover, "takeover", false, "with --apply, append an explicit takeover event")
	command.Flags().StringVar(&actor, "actor", "", "required with --takeover")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newWorktreeLogFinalizeCmd() *cobra.Command {
	var resultValue, message, format string
	var apply bool
	command := &cobra.Command{
		Use:   "finalize [worktree-path]",
		Short: "Record a terminal result and optionally seal the claim",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			result, err := worktrees.LogFinalize(command.Context(), worktrees.LogFinalizeOptions{
				ProjectsRoot: projectsRoot, Worktree: worktreeLogPath(args),
				Result: resultValue, Message: message, Apply: apply,
			})
			if err != nil {
				return err
			}
			return encodeLogVerbResult(command, format, result)
		},
	}
	command.Flags().StringVar(&resultValue, "result", "success", "success or failure")
	command.Flags().StringVar(&message, "message", "", "terminal message")
	command.Flags().BoolVar(&apply, "apply", false, "seal the hybrid claim terminal")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newWorktreeLogSyncCmd() *cobra.Command {
	var format string
	var apply bool
	command := &cobra.Command{
		Use:   "sync [worktree-path]",
		Short: "Drain local outbox to Synchestra when configured",
		Long: `Attempt authoritative sync. Without a configured Synchestra endpoint the
command stays offline, retains the local outbox, and records a sync_attempt.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			result, err := worktrees.LogSync(command.Context(), worktrees.LogSyncOptions{
				ProjectsRoot: projectsRoot, Worktree: worktreeLogPath(args), Apply: apply,
			})
			if err != nil {
				return err
			}
			return encodeLogVerbResult(command, format, result)
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "attempt drain (still offline without Synchestra)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newWorktreeLogArchiveCmd() *cobra.Command {
	var format string
	var apply, force bool
	command := &cobra.Command{
		Use:   "archive [worktree-path]",
		Short: "Archive a finalized local journal into WB_HOME",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			result, err := worktrees.LogArchive(command.Context(), worktrees.LogArchiveOptions{
				ProjectsRoot: projectsRoot, Worktree: worktreeLogPath(args), Apply: apply, Force: force,
			})
			if err != nil {
				return err
			}
			return encodeLogVerbResult(command, format, result)
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "copy .wb/local into WB_HOME/worklogs")
	command.Flags().BoolVar(&force, "force", false, "override terminal/seven-day gates")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newWorktreeCorrectIdentityCmd() *cobra.Command {
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
never a token, credential, or secret.`,
		Args: cobra.ExactArgs(3),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
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
			result, err := worktrees.CorrectExecutionIdentity(worktrees.CorrectExecutionIdentityOptions{
				ProjectsRoot: projectsRoot, EffortID: args[0], RunID: args[1], ClaimID: args[2], EventID: eventID,
				Actor: actor, Reason: reason, Model: modelValue, CLI: cliValue, Provider: providerValue,
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

func newWorktreeAbortCmd() *cobra.Command {
	var base, disposition, successor, format string
	var model, cli, provider string
	var apply, deleteRemote bool
	command := &cobra.Command{
		Use:   "abort <task>",
		Short: "Seal an interrupted task so it can be resumed or explicitly discarded",
		Long: `Abort is the audited alternative to manually deleting an unfinished worktree.
It seals each local Work Log and queues an offline-safe outbox event. --apply
with handoff or not_landed transfers one active claim to the required
--successor while leaving the branch/worktree resumable. The creator must
declare the successor's exact --model or explicit unknown; --cli and
--provider independently record the invoking client and commercial route when
known, never credentials. --apply with
discarded removes only clean, unlocked worktrees and their exact local branch
refs after their archive has been sealed. A discarded apply requires --remote;
an exact matching remote source branch is then retired with force-with-lease.
If interruption happens after worktree removal, the same command inspects and
resumes the durable exact local-branch cleanup backlog.

--filter (see the root flag) narrows which repositories in the task are
touched, the same "owner/repository slug contains this substring" semantics
as wb branch cleanup --filter. A repository --filter excludes is left
completely untouched and still reported, so one repository blocked on
something abort cannot fix no longer makes the rest of the task un-abortable.
The task remains non-terminal — and the remote claim, if any, is not
released — until every repository is eventually resolved.

The default is a dry-run plan.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			results, err := worktrees.Abort(command.Context(), worktrees.AbortOptions{
				ProjectsRoot: projectsRoot, Task: args[0], Base: base, Filter: filterFlag,
				Disposition: worktrees.AbortDisposition(disposition), Successor: successor,
				SuccessorIdentity: worktrees.ClaimExecutionIdentity{Model: model, CLI: cli, Provider: provider},
				DeleteRemote:      deleteRemote, Apply: apply,
			})
			if err != nil {
				return err
			}
			remaining := 0
			for _, result := range results {
				if result.Excluded {
					remaining++
				}
			}
			// Release only follows a disposition that actually ends the
			// task on this machine: discarded drops it, handoff moves it to
			// a successor. not_landed leaves the task expected to resume
			// here, so the claim must stay put. A --filter that left any
			// repository untouched means the task is not actually finished,
			// so the remote claim must not be released either.
			disp := worktrees.AbortDisposition(disposition)
			releasable := disp == worktrees.AbortDiscarded || disp == worktrees.AbortHandoff
			switch {
			case apply && releasable && remaining == 0:
				tryAutoRelease(defaultRemoteDeps(), projectsRoot, args[0], remoteClaimWriter(command))
			case apply && releasable && remaining > 0:
				skippedAutoRelease(remoteClaimWriter(command), fmt.Sprintf("%d repositories excluded by --filter still remain", remaining))
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(results)
			}
			for _, result := range results {
				if result.Excluded {
					reason := result.Reason
					if reason == "" {
						reason = "not evaluated"
					}
					if _, err := fmt.Fprintf(command.OutOrStdout(), "excluded by --filter %s %s: %s\n", result.Repository, result.Disposition, reason); err != nil {
						return err
					}
					continue
				}
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
			if remaining > 0 {
				if _, err := fmt.Fprintf(command.OutOrStdout(), "%d repositories excluded by --filter remain unresolved for task %q\n", remaining, args[0]); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&base, "base", "main", "base branch for managed-worktree validation")
	command.Flags().StringVar(&disposition, "disposition", "", "required: handoff, not_landed, or discarded")
	command.Flags().StringVar(&successor, "successor", "", "one successor agent/session ID (required for handoff or not_landed)")
	command.Flags().StringVar(&model, "model", "", "required with applied handoff/not_landed: exact successor model or explicit unknown; WB never guesses")
	command.Flags().StringVar(&cli, "cli", "", "optional invoking CLI/client identifier, supplied only when known")
	command.Flags().StringVar(&provider, "provider", "", "optional routing/billing provider identifier, never a credential")
	command.Flags().BoolVar(&apply, "apply", false, "seal Work Logs and apply the selected disposition")
	command.Flags().BoolVar(&deleteRemote, "remote", false, "retire an exact unchanged remote source branch when applying discarded")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newWorktreeCreateCmd() *cobra.Command {
	var branch, branchPrefix, base, format string
	var resume, noClaim bool
	var effortID, runID, initiator, agentID, agentRuntime, model, cli, provider, originalPrompt string
	command := &cobra.Command{
		Use:   "create <task> [owner/repository...]",
		Short: "Create feature branches below WB's home worktrees directory",
		Long: `Create one isolated feature worktree per repository.

WB reads even a dirty or off-base canonical clone without switching or updating any local branch, index, or working tree. It fetches the exact requested base
from origin, then creates each
worktree from that verified commit at:

  <wb-home>/worktrees/<task>/<owner>/<repository>

<wb-home> is ~/.wb by default. Set $WB_HOME to use a different directory.
New work never silently falls back to <projects-root>/.wb; when WB_HOME is not
explicit, existing legacy worktrees there remain guardable, listable, and
cleanable during migration.

If no repository is supplied, WB derives owner/repository from the current
checkout's origin remote. Existing branches or worktrees are never reused
unless --resume is explicit.

Resume first recovers its registered branch and active Work Log claim. Current
naming policy cannot replace that recovered identity; WB consults it only when
it creates a new checkout. An exact --branch may assert the recovered branch.
Changing run or agent provenance requires an audited handoff instead of
silently replacing the claim.

--original-prompt-file is mandatory. WB snapshots its exact non-empty bytes
into the private Work Log under WB_HOME before any worktree is created; prompt
text never enters the worktree projection, source Git, or normal output.

Pass --original-prompt-file - to supply the prompt on stdin instead of a path.
WB reads stdin once, in memory, and writes the private 0600 archive itself
under WB_HOME; no caller-owned staging file ever exists, so two concurrent
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
			return validateWorktreeBranchFlags(command, branch)
		},
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			workLog := worktrees.WorkLogOptions{
				EffortID: effortID, RunID: runID, Initiator: initiator, AgentID: agentID,
				AgentRuntime: agentRuntime, Model: model, CLI: cli, Provider: provider, OriginalPrompt: originalPrompt,
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
			if err := refreshManagedHooksBeforeWorktreeCreate(repositories); err != nil {
				return err
			}
			// In text mode the hook's own printed line on stdout is the
			// record (see the RunE's "text" branch below). The note goes to
			// stderr in every format: it is a diagnostic about a side
			// channel rather than this command's result, so it must not
			// land in the json document or in text stdout. In json mode the
			// outcome also travels structurally in the remote_claim field.
			claimResult := worktreeCreateAutoClaim(defaultRemoteDeps(), noClaim, projectsRoot, args[0], remoteClaimWriter(command))
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
			// A fresh worktree gets its .worktree.md before the agent that
			// asked for it is told where to go, so the first thing readable at
			// that path already says the checkout is writable and which task
			// it carries. The canonical clone it was cut from is marked in the
			// same pass, because that is the checkout the agent has to be
			// steered away from. Marking is best-effort: a checkout WB just
			// created is not made unusable by a marker it could not write, and
			// the reason goes to stderr rather than into the result document.
			markCreatedCheckouts(command, base, results)
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
				if err := encoder.Encode(worktreeCreateJSON(claimResult, results)); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported format %q; use text or json", format)
			}
			return nil
		},
	}
	setDiscoveryTerms(command, "start begin create new work task agent isolated worktree branch implement edit code save tokens")
	command.Flags().StringVar(&branch, "branch", "", "exact feature branch (overrides branch-prefix configuration)")
	command.Flags().StringVar(&branchPrefix, "branch-prefix", "", "derive <prefix><task>; an explicit empty value disables configured prefixes")
	command.Flags().StringVar(&base, "base", "main", "canonical and remote base branch")
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
	command.Flags().StringVar(&originalPrompt, "original-prompt-file", "", "required readable non-empty file containing the exact original prompt, or - to read it from stdin; archived under WB_HOME only")
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
	var base, format, admission string
	var quiet bool
	command := &cobra.Command{
		Use:   "guard [repository-path]",
		Short: "Reject unsafe canonical clones and misplaced worktrees",
		Long: `Validate the checkout policy used by agents and WB Git hooks.

A canonical clone is valid only when it is clean and on the base branch. A
linked checkout is valid only when it uses a non-base branch and either lives
at <wb-home>/worktrees/<task>/<owner>/<repository> (see 'wb worktree create
--help' for how <wb-home> is resolved) or carries an active Work Log claim
from 'wb worktree adopt --apply' — adoption's whole point is never relocating
the checkout, so that one case is resolved from its claim instead of its path.

--admission additionally requires a managed worktree to carry its own record: a
manifest and at least one recorded instruction. It binds on the worktree's
location and never tries to tell an agent from a human by environment markers,
which can be absent exactly when they matter. Use warn while a fleet adopts the
journal and enforce once it has.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			if err := requireOutputFormat(admission, "off", "warn", "enforce"); err != nil {
				return fmt.Errorf("unsupported admission mode %q; use off, warn, or enforce", admission)
			}
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			result, err := worktrees.Guard(command.Context(), path, worktrees.GuardOptions{
				ProjectsRoot: projectsRoot,
				Base:         base,
				Admission:    worktrees.AdmissionMode(admission),
			})
			if err != nil {
				return err
			}
			// A warning prints even under --quiet: warn mode exists precisely
			// to be seen before enforcement starts refusing the same commit.
			if result.Admission != nil && result.Admission.Reason != "" {
				_, _ = fmt.Fprintf(command.ErrOrStderr(), "warning: %s\n  %s\n", result.Admission.Reason, result.Admission.Remedy)
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
				kind := result.Kind
				if result.External {
					kind += " (adopted)"
				}
				_, err = fmt.Fprintf(command.OutOrStdout(), "ok: %s checkout %s on %s\n", kind, result.Path, checkout)
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
	command.Flags().StringVar(&admission, "admission", "off", "require a worktree record before committing: off, warn, or enforce (managed hooks default to enforce)")
	return command
}

// newWorktreeSetCmd is the human-facing remedy the admission gate names. It
// deliberately records a prompt rather than setting a bypass flag: the act of
// unblocking a commit is itself the record of who directed it.
func newWorktreeSetCmd() *cobra.Command {
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
			result, err := worktrees.LogSteer(command.Context(), worktrees.LogSteerOptions{
				ProjectsRoot: projectsRoot, Worktree: path, Body: body,
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

func readPromptBody(prompt, promptFile string) ([]byte, error) {
	if (prompt == "") == (promptFile == "") {
		return nil, fmt.Errorf("supply exactly one of --prompt or --prompt-file")
	}
	if promptFile != "" {
		content, err := os.ReadFile(promptFile)
		if err != nil {
			return nil, fmt.Errorf("read prompt file: %w", err)
		}
		if len(bytes.TrimSpace(content)) == 0 {
			return nil, fmt.Errorf("prompt file %s is empty", promptFile)
		}
		return content, nil
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("--prompt must not be empty")
	}
	return []byte(prompt), nil
}

func newWorktreeBackfillCmd() *cobra.Command {
	var base, format string
	var apply bool
	command := &cobra.Command{
		Use:   "backfill",
		Short: "Give existing worktrees a reconstructed manifest so the fleet becomes explicable",
		Long: `Write a reconstructed manifest into every reachable worktree that lacks one.

Adoption never requires stopping agents. This is additive by construction:
.wb/local/ is a new path, nothing moves, and no working tree is touched, so a
worktree holding uncommitted changes is unaffected. Re-running is safe, which
matters because a sweep over hundreds of worktrees will be interrupted.

A reconstructed manifest records which fields were inferred and from what
evidence, so triage never mistakes an inference for a creation record. It never
fabricates a prompt: a worktree whose instructions were never recorded has
none, and 'wb worktree set --prompt' is how it gets its first real one.

The default is a dry run.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			results, err := worktrees.Backfill(command.Context(), worktrees.BackfillOptions{
				ProjectsRoot: projectsRoot, Base: base, Apply: apply,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(results)
			}
			counts := map[string]int{}
			for _, result := range results {
				counts[result.Action]++
				if result.Action == worktrees.BackfillSkipped {
					if _, err := fmt.Fprintf(command.OutOrStdout(), "skipped %s: %s\n", result.Path, result.Reason); err != nil {
						return err
					}
				}
			}
			actions := make([]string, 0, len(counts))
			for action := range counts {
				actions = append(actions, action)
			}
			sort.Strings(actions)
			for _, action := range actions {
				if _, err := fmt.Fprintf(command.OutOrStdout(), "%-15s %d\n", action, counts[action]); err != nil {
					return err
				}
			}
			if !apply {
				_, err = fmt.Fprintln(command.OutOrStdout(), "dry-run only, pass --apply to write")
			}
			return err
		},
	}
	command.Flags().StringVar(&base, "base", "main", "remote target used while reconstructing a base ref")
	command.Flags().BoolVar(&apply, "apply", false, "write the reconstructed manifests")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newWorktreeAdoptCmd() *cobra.Command {
	var base, format string
	var allExternal, apply bool
	command := &cobra.Command{
		Use:   "adopt [path]",
		Short: "Bring an external (pre-WB) worktree under WB management",
		Long: `Give wb worktree cleanup and abort a task to act on for a worktree they
currently refuse with "task ... was not found".

wb worktree orphans already discovers and classifies every worktree with
layout "external" — a linked worktree Git knows about that was never created
by wb. wb worktree backfill already gives one a manifest, but that is only
the identity half of adoption: no task directory or Work Log claim exists for
cleanup/abort's own preflight to resolve. Adopt writes that task directory,
manifest, and claim, reusing backfill's reconstruction (see wb worktree
backfill) rather than duplicating it, and it never fabricates a prompt.

It is additive, exactly like backfill: the working tree, its index, and its
checked-out branch are never moved, modified, or otherwise touched. Only a
small registration entry is created under the WB home. A worktree that
already belongs to a WB task — adopted before, or created directly by
wb worktree create — is a no-op, so re-running a sweep after an interruption
is safe.

After adoption, wb worktree cleanup <task> and wb worktree abort <task> apply
their full existing safety checks unchanged: dirty, unlanded, an open pull
request, a held lock, and awaiting_push are all still refused exactly as they
are for a worktree wb created directly.

Pass a worktree path to adopt exactly one, or --all-external to sweep every
external worktree (narrow it with --filter). The default is a dry run.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			if (path == "") == !allExternal {
				return fmt.Errorf("supply exactly one of a worktree path or --all-external")
			}
			results, err := worktrees.Adopt(command.Context(), worktrees.AdoptOptions{
				ProjectsRoot: projectsRoot, Base: base, Path: path,
				AllExternal: allExternal, Filter: filterFlag, Apply: apply,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(results)
			}
			return renderAdopt(command.OutOrStdout(), results, apply)
		},
	}
	command.Flags().StringVar(&base, "base", "main", "remote target used while reconstructing a base ref")
	command.Flags().BoolVar(&allExternal, "all-external", false, "adopt every worktree wb worktree orphans classifies as external")
	command.Flags().BoolVar(&apply, "apply", false, "write the task directory, manifest, and Work Log claim")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func renderAdopt(out io.Writer, results []worktrees.AdoptResult, apply bool) error {
	counts := map[string]int{}
	for _, result := range results {
		counts[result.Action]++
		switch result.Action {
		case worktrees.AdoptSkipped:
			if _, err := fmt.Fprintf(out, "skipped %s: %s\n", result.Path, result.Reason); err != nil {
				return err
			}
		case worktrees.AdoptAdopted, worktrees.AdoptWouldAdopt:
			if _, err := fmt.Fprintf(out, "%-13s %-30s %s\n", result.Action, result.Task, result.Path); err != nil {
				return err
			}
		}
	}
	actions := make([]string, 0, len(counts))
	for action := range counts {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	for _, action := range actions {
		if _, err := fmt.Fprintf(out, "%-15s %d\n", action, counts[action]); err != nil {
			return err
		}
	}
	if !apply {
		_, err := fmt.Fprintln(out, "dry-run only, pass --apply to write")
		return err
	}
	return nil
}

func newWorktreeOrphansCmd() *cobra.Command {
	var base, format, only string
	var staleDays int
	command := &cobra.Command{
		Use:   "orphans",
		Short: "Explain every linked worktree and recommend what to do with it",
		Long: `Read-only triage of every linked worktree reachable from the projects root.

Discovery goes through each canonical clone's own Git worktree registry, so it
sees all three layout generations at once: WB's current home, the legacy
<projects-root>/.wb hierarchy, and pre-WB checkouts living anywhere else.

Identity comes from a worktree's own manifest where one exists. Where none
does, WB reconstructs what it can from path, branch, and commit evidence and
labels the row as reconstructed rather than presenting it as a creation record.

Rows group by root effort so a family of sub-agent worktrees is one subject. A
family is recommended for removal only when every worktree in it has landed.
This command never mutates anything.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			report, err := worktrees.Orphans(command.Context(), worktrees.OrphanOptions{
				ProjectsRoot: projectsRoot,
				Base:         base,
				StaleAfter:   time.Duration(staleDays) * 24 * time.Hour,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(report)
			}
			return renderOrphans(command.OutOrStdout(), report, only)
		},
	}
	command.Flags().StringVar(&base, "base", "main", "remote target a branch must be contained in to count as landed")
	command.Flags().IntVar(&staleDays, "stale-days", 14, "days without a commit before unmerged work needs a decision")
	command.Flags().StringVar(&only, "only", "", "show only families with this disposition: active, remove, review, or decide")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func renderOrphans(out io.Writer, report worktrees.OrphanReport, only string) error {
	shown := 0
	for _, family := range report.Families {
		if only != "" && family.Disposition != only {
			continue
		}
		shown++
		if _, err := fmt.Fprintf(out, "\n%s [%s] %s\n", family.RootEffort, family.Disposition, family.Reason); err != nil {
			return err
		}
		for _, worktree := range family.Worktrees {
			marks := []string{worktree.Layout}
			if !worktree.HasManifest {
				marks = append(marks, "no-manifest")
			} else if worktree.Provenance != "" {
				marks = append(marks, worktree.Provenance)
			}
			if worktree.Dirty {
				marks = append(marks, "dirty")
			}
			if worktree.Missing {
				marks = append(marks, "missing")
			}
			// Owner state is the difference between proof and a guess, so it
			// belongs on the row rather than only in the evidence lines.
			switch worktree.OwnerState {
			case worktrees.OwnerLive:
				marks = append(marks, "owner live")
			case worktrees.OwnerGone:
				marks = append(marks, "owner gone")
			default:
				marks = append(marks, "owner unstated")
			}
			if _, err := fmt.Fprintf(out, "  %-8s %s %s (%s)\n",
				worktree.Disposition, worktree.Repository, worktree.Branch, strings.Join(marks, ", ")); err != nil {
				return err
			}
			for _, evidence := range worktree.Evidence {
				if _, err := fmt.Fprintf(out, "           - %s\n", evidence); err != nil {
					return err
				}
			}
		}
	}
	// Residue is not a family: nothing registers it, so it has no branch, no
	// effort, and no disposition to group by. Print it as its own section so a
	// sweep that finds one cannot be read as a clean sweep.
	if len(report.Residue) > 0 && (only == "" || only == worktrees.DispositionReview) {
		if _, err := fmt.Fprintf(out, "\nunregistered checkouts (%d)\n", len(report.Residue)); err != nil {
			return err
		}
		for _, residue := range report.Residue {
			if _, err := fmt.Fprintf(out, "  %s %s (%s)\n", residue.Task, residue.Repository, residue.Layout); err != nil {
				return err
			}
			for _, evidence := range residue.Evidence {
				if _, err := fmt.Fprintf(out, "           - %s\n", evidence); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(out, "           > %s\n", residue.Remedy); err != nil {
				return err
			}
		}
	}
	totals := report.Totals
	if _, err := fmt.Fprintf(out,
		"\n%d worktrees in %d efforts (%d shown): %d current, %d legacy, %d external; %d without a manifest, %d dirty\n",
		totals.Worktrees, totals.Families, shown,
		totals.ByLayout[worktrees.LayoutCurrent], totals.ByLayout[worktrees.LayoutLegacy],
		totals.ByLayout[worktrees.LayoutExternal], totals.NoManifest, totals.Dirty); err != nil {
		return err
	}
	dispositions := make([]string, 0, len(totals.ByDispositn))
	for disposition := range totals.ByDispositn {
		dispositions = append(dispositions, disposition)
	}
	sort.Strings(dispositions)
	for _, disposition := range dispositions {
		if _, err := fmt.Fprintf(out, "  %-8s %d\n", disposition, totals.ByDispositn[disposition]); err != nil {
			return err
		}
	}
	for _, unscanned := range report.Unscanned {
		if _, err := fmt.Fprintf(out, "unscanned: %s\n", unscanned); err != nil {
			return err
		}
	}
	return nil
}

func newWorktreeListCmd() *cobra.Command {
	var base, format, absorbedBy, ownerState string
	var github bool
	var parallel int
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
				OwnerState:   ownerState,
				AbsorbedBy:   absorbedBy,
				GitHub:       github,
				Workers:      parallel,
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
	command.Flags().IntVar(&parallel, "parallel", worktrees.DefaultInspectWorkers, "maximum repositories to inspect concurrently")
	command.Flags().BoolVar(&github, "github", false, "include pull request state from GitHub")
	command.Flags().StringVar(&absorbedBy, "absorbed-by", "", "with --github, verify work landed inside this merged pull request number or exact landing commit")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().StringVar(&ownerState, "only", "", "only worktrees with owner PID state: active or orphaned")
	return command
}

func newWorktreeSummaryCmd() *cobra.Command {
	var base, format string
	var github bool
	command := &cobra.Command{
		Use:   "summary <task>",
		Short: "Brief overview of every worktree, branch, and optional PR for one task",
		Long: `Summarize one WB task/effort across all of its live linked worktrees.

Requires the task name. Reports each repository's worktree path, branch, short
head, clean/dirty/locked state, and whether the head is integrated at the
exact origin target. Pass --github to attach open or merged pull-request
evidence. Unlike 'wb worktree list', this command always scopes to one named
task and formats a brief human overview rather than a flat inventory row.

JSON reuses the same versioned list envelope (results, diagnostics, artifacts).
Prompt bodies are never included; use 'wb worktree info' or 'wb worktree log'
for per-checkout journal detail.`,
		Example: `wb worktree summary improve-login
wb worktree summary improve-login --github --format json`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			outcome, err := worktrees.ListWithDiagnostics(command.Context(), worktrees.ListOptions{
				ProjectsRoot: projectsRoot,
				Task:         args[0],
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
				return printWorktreeSummary(command, args[0], outcome.Results, github)
			case "json":
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(outcome)
			default:
				return fmt.Errorf("unsupported format %q; use text or json", format)
			}
		},
	}
	setDiscoveryTerms(command, "inspect progress status next action task work worktree branch pull request ready merge")
	command.Flags().StringVar(&base, "base", "main", "base branch used to assess local merge state")
	command.Flags().BoolVar(&github, "github", false, "include pull request state from GitHub")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newWorktreeCleanupCmd() *cobra.Command {
	var base, format, reportDir, absorbedBy string
	var allMerged, apply, deleteRemote, resumeInterrupted, retireShells, verbose bool
	var olderThan time.Duration
	var workers int
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

A branch whose exact head never reaches the target because a merger batched it
onto a differently named integration branch and landed that branch once is
still eligible, on evidence. WB reads GitHub's own commit-to-pull-request
index for the immutable source commit; when that names a merged pull request
into this exact base whose merge commit is contained in the fetched target, WB
then proves containment locally: merging the branch into that merge commit
must add nothing to it, and merging it into the target must add nothing there
either. Work that landed and was later reverted therefore stays blocked.

--absorbed-by <pr|commit> covers a landing GitHub cannot associate, such as
content cherry-picked rather than merged into the integration branch. It only
selects which receipt to verify; every proof above still runs, and the named
commit must additionally be exactly where the work entered the target, so the
flag can never make unlanded work eligible.

Reserved .wb-stage-* and .wb-retired-stage-* entries are first-class cleanup
backlog. Apply descriptor-safely archives an exact recognized empty stage
outside the active task. A non-empty, symlinked, replaced, or invalid stage
blocks the task and is preserved for audited recovery.

For a specifically named task, the implicit age window is zero and --apply
requires --remote: definition of done includes retirement of the source remote
branch as well as the local worktree/branch. --all-merged fleet sweeping keeps
the 24-hour merged-PR grace window unless --older-than overrides it.

--parallel bounds both phases. The inventory walk inspects that many
candidates at once; the apply phase removes that many worktrees at once, one
task per canonical repository. Git allows a single writer per clone, so two
tasks in one repository still take turns and a task spanning several
repositories holds all of them for its whole transaction. The achievable gain
is therefore the size of the largest per-repository group, not the worker
count. Remote branch deletions are bounded more tightly still, against
GitHub's per-account secondary rate limit. --parallel 1 restores the fully
serial apply. Whatever order tasks finish in, the report reads in walk order.

--filter (see the root flag) and a named [task] both narrow which candidates
are inspected at all, before any of the above safety checks run. A malformed
candidate outside that selection is invisible to the run. One inside it is
never fatal: it is reported as a warning and blocks eligibility only for its
own coordinated task, exactly like an unclean or locked sibling would.

--resume-interrupted is a named-task-only recovery authority. It validates the
exact retained .lock as operation=<task> and a positive PID that is
conclusively dead, then holds that descriptor-safe lock through normal cleanup.
Without --apply it is a non-mutating recovery plan; with --apply --remote it
quarantines only the exact held inode and completes the usual terminal flow.

--retire-shells sweeps every task directory instead of inspecting worktrees: a
task whose worktree(s) were already removed by a cleanup that predates the
terminal-namespace-residue fix can be left with an empty owner-namespace
directory and/or a retired operation lock and nothing else. It only ever
retires a task directory it can prove is exactly that — no live checkout
anywhere under it, no live or interrupted lock, no reserved stage entry.
Anything else is left untouched and reported. It cannot be combined with a
task argument or --all-merged, and is itself dry-run by default; --apply is
required to remove anything.`,
		Args: func(command *cobra.Command, args []string) error {
			if err := cobra.MaximumNArgs(1)(command, args); err != nil {
				return err
			}
			if retireShells {
				if len(args) != 0 {
					return fmt.Errorf("--retire-shells sweeps every task; it cannot be combined with a task argument")
				}
				if allMerged {
					return fmt.Errorf("--retire-shells and --all-merged cannot be combined")
				}
				return nil
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
			if retireShells {
				outcome, err := worktrees.RetireTaskShells(command.Context(), worktrees.RetireShellsOptions{
					ProjectsRoot: projectsRoot,
					Filter:       filterFlag,
					Apply:        apply,
				})
				if err != nil {
					return err
				}
				switch format {
				case "text":
					return printRetireTaskShells(command, outcome)
				case "json":
					encoder := json.NewEncoder(command.OutOrStdout())
					encoder.SetIndent("", "  ")
					return encoder.Encode(outcome)
				default:
					return fmt.Errorf("unsupported format %q; use text or json", format)
				}
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
			if resumeInterrupted && task == "" {
				return fmt.Errorf("--resume-interrupted requires one explicit task")
			}
			now := time.Now()
			progress := newInventoryProgress(command.ErrOrStderr(), verbose)
			defer progress.finish()
			outcome, err := worktrees.Cleanup(command.Context(), worktrees.CleanupOptions{
				ProjectsRoot:      projectsRoot,
				Task:              task,
				Base:              base,
				Filter:            filterFlag,
				AbsorbedBy:        absorbedBy,
				AllMerged:         allMerged,
				Apply:             apply,
				ResumeInterrupted: resumeInterrupted,
				DeleteRemote:      deleteRemote,
				OlderThan:         olderThan,
				ReportDir:         reportDir,
				Progress:          progress.report,
				Workers:           workers,
				Now:               func() time.Time { return now },
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
						tryAutoRelease(defaultRemoteDeps(), projectsRoot, task, remoteClaimWriter(command))
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
	command.Flags().BoolVar(&resumeInterrupted, "resume-interrupted", false, "recover only this named task's proven-dead interrupted lock before cleanup")
	command.Flags().BoolVar(&deleteRemote, "remote", false, "also delete an unchanged remote branch when applying")
	command.Flags().DurationVar(&olderThan, "older-than", 24*time.Hour, "minimum age of a merged pull request (0 disables)")
	command.Flags().StringVar(&reportDir, "report-dir", "", "cleanup audit directory (default <wb-home>/reports/worktree-cleanup/<timestamp>)")
	command.Flags().StringVar(&absorbedBy, "absorbed-by", "", "verify work landed inside this merged pull request number or exact landing commit")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&retireShells, "retire-shells", false, "sweep every task for an empty pre-existing shell (owner directory and/or retired lock, no live checkout) and retire it")
	// --parallel is the fleet-wide name for this ceiling: six other commands
	// already spell it that way, and "workers" reads as a second noun beside
	// WB's own tasks. --workers/-j stays as a hidden deprecated alias so
	// existing scripts and muscle memory keep working.
	command.Flags().IntVar(&workers, "parallel", worktrees.DefaultInspectWorkers, "maximum repositories to inspect, and to apply, concurrently")
	command.Flags().IntVarP(&workers, "workers", "j", worktrees.DefaultInspectWorkers, "maximum repositories to inspect, and to apply, concurrently")
	_ = command.Flags().MarkDeprecated("workers", "use --parallel instead")
	command.Flags().BoolVarP(&verbose, "verbose", "v", false, "stream per-candidate inspection progress to stderr, even when not on a terminal")
	return command
}

func printRetireTaskShells(command *cobra.Command, outcome worktrees.RetireShellsOutcome) error {
	out := command.OutOrStdout()
	if len(outcome.Results) == 0 {
		_, err := fmt.Fprintln(out, "no WB task directories found")
		return err
	}
	for _, result := range outcome.Results {
		switch {
		case result.Applied:
			if _, err := fmt.Fprintf(out, "retired      %s %s\n", result.Task, result.Path); err != nil {
				return err
			}
		case result.Eligible:
			if _, err := fmt.Fprintf(out, "would retire %s %s\n", result.Task, result.Path); err != nil {
				return err
			}
		case result.Error != "":
			if _, err := fmt.Fprintf(out, "failed       %s %s: %s\n", result.Task, result.Path, result.Error); err != nil {
				return err
			}
		default:
			if _, err := fmt.Fprintf(out, "skip         %s %s: %s\n", result.Task, result.Path, result.Reason); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	if outcome.Apply {
		_, err := fmt.Fprintf(out, "%d retired\n", outcome.Totals["retired"])
		return err
	}
	_, err := fmt.Fprintf(out, "%d would retire\n", outcome.Totals["would_retire"])
	return err
}

func newWorktreeRenameCmd() *cobra.Command {
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
				markRenamedCheckouts(command, base, outcome.Results)
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
	command.Flags().StringVar(&model, "model", "", "required exact child model identifier, or explicit unknown; WB never guesses")
	command.Flags().StringVar(&cli, "cli", "", "optional invoking CLI/client identifier, supplied only when known")
	command.Flags().StringVar(&provider, "provider", "", "optional routing/billing provider identifier, never a credential")
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
		case result.AbsorbedAtOrigin:
			state = "absorbed"
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
			"%s  %s  %s  %s  owners=%s  %s\n",
			result.Task, result.Repository, result.Branch, state, result.OwnerState, pr,
		); err != nil {
			return err
		}
	}
	return nil
}

func printWorktreeSummary(command *cobra.Command, task string, results []worktrees.ListResult, withGitHub bool) error {
	out := command.OutOrStdout()
	if _, err := fmt.Fprintf(out, "# WB worktree summary: %s\n\n", task); err != nil {
		return err
	}
	if len(results) == 0 {
		_, err := fmt.Fprintln(out, "no live worktrees for this task")
		return err
	}
	if _, err := fmt.Fprintf(out, "%d worktree(s)\n\n", len(results)); err != nil {
		return err
	}
	for index, result := range results {
		state := worktreeSummaryState(result)
		head := result.HeadSHA
		if len(head) > 12 {
			head = head[:12]
		}
		if _, err := fmt.Fprintf(out, "## %s\n", result.Repository); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "worktree: %s\n", result.WorktreeDir); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "branch:   %s\n", result.Branch); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "head:     %s\n", head); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "state:    %s\n", state); err != nil {
			return err
		}
		integration := "not integrated at origin/" + result.Base
		switch {
		case result.IntegratedAtOrigin:
			integration = "integrated at origin/" + result.Base
		case result.AbsorbedAtOrigin:
			integration = "absorbed at origin/" + result.Base
		case result.RebaseMergedAtOrigin:
			integration = "rebase-merged at origin/" + result.Base
		case result.LocallyMerged:
			integration = "locally merged; awaiting push"
		}
		if _, err := fmt.Fprintf(out, "target:   %s\n", integration); err != nil {
			return err
		}
		switch {
		case result.OpenPullRequest != nil:
			if _, err := fmt.Fprintf(out, "pr:       open #%d %s\n", result.OpenPullRequest.Number, result.OpenPullRequest.URL); err != nil {
				return err
			}
		case result.MergedPullRequest != nil:
			if _, err := fmt.Fprintf(out, "pr:       merged #%d %s\n", result.MergedPullRequest.Number, result.MergedPullRequest.URL); err != nil {
				return err
			}
		case withGitHub:
			if _, err := fmt.Fprintln(out, "pr:       none"); err != nil {
				return err
			}
		}
		if index+1 < len(results) {
			if _, err := fmt.Fprintln(out); err != nil {
				return err
			}
		}
	}
	return nil
}

func worktreeSummaryState(result worktrees.ListResult) string {
	parts := make([]string, 0, 3)
	if !result.Clean {
		parts = append(parts, "dirty")
	} else {
		parts = append(parts, "clean")
	}
	if result.Locked {
		parts = append(parts, "locked")
	}
	switch {
	case result.OpenPullRequest != nil:
		parts = append(parts, "open-pr")
	case result.AbsorbedAtOrigin:
		parts = append(parts, "absorbed")
	case result.MergedPullRequest != nil:
		parts = append(parts, "merged")
	case result.LocallyMerged:
		parts = append(parts, "locally-merged")
	default:
		parts = append(parts, "active")
	}
	return strings.Join(parts, ",")
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
			// Say when WB, not Git, deleted the checkout. The task finished
			// either way, and an operator reading this line is the person who
			// would otherwise have found the directory still on disk.
			residue := ""
			if result.WorktreeResidueRemoved {
				residue = " (WB removed the checkout Git unregistered but could not delete)"
			}
			if _, err := fmt.Fprintf(command.OutOrStdout(), "removed %s %s%s%s\n", result.Task, result.Repository, remote, residue); err != nil {
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
