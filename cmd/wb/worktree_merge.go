package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/progress"
)

type worktreeMergeFlags struct {
	target, route, onFailure, format       string
	model, runtime, agentID, cli, provider string
	cleanup                                bool
	progress                               bool
	timeout                                time.Duration
	retry                                  int
	interval                               time.Duration
}

func newWorktreeMergeCmd() *cobra.Command {
	var flags worktreeMergeFlags
	command := &cobra.Command{
		Use:   "merge <source-worktree...>",
		Short: "Prepare and mechanically land compatible WB worktrees",
		Long: `Prepare an isolated integration candidate, then land it through an
authoritatively permitted direct push or pull request. Phase 1 is prepared locally, not landed,
and its immutable SHA can unblock dependent agents. WB will never force-push. Landing is
proved against the exact remote target before optional receipt-gated cleanup. Clean target
drift rebases an unpublished candidate and reruns validation; a conflict or already-published
candidate stops without rewriting it. A landed failure retains before/after evidence for a
forward revert. If exact post-target CI fails and the same source advances with a clean
forward repair, rerunning merge advances the retained candidate onto the landed target,
records the failed landing, and opens a new repair PR without rewriting history.`,
		Example: `# Finish one compatible worktree end to end
wb worktree merge . --route auto --cleanup

# Split preparation from landing for a resumable handoff
wb worktree merge prepare /path/to/worktree --format json
wb worktree merge land /path/to/landing-receipt --cleanup`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := validateWorktreeMergeFlags(flags); err != nil {
				return err
			}
			campaign := newWorktreeMergeProgress(command, flags)
			receipt, err := orchestrate.RunWorktreeMerge(command.Context(), prepareMergeOptions(flags, args, campaign.reporter()), landMergeOptions(flags, "", campaign.reporter()))
			finishWorktreeMergeProgress(campaign, receipt, err)
			if writeErr := writeWorktreeMergeReceipt(command.OutOrStdout(), flags.format, receipt); writeErr != nil && err == nil {
				return writeErr
			}
			return err
		},
	}
	setDiscoveryTerms(command, "finish work merge land deliver ship integrate complete cleanup agent worktree branch pull request main")
	bindWorktreeMergeFlags(command, &flags, true, true)
	command.AddCommand(newWorktreeMergePrepareCmd(), newWorktreeMergeLandCmd("land"), newWorktreeMergeLandCmd("resume"), newWorktreeMergeRevertCmd())
	return command
}

func newWorktreeMergePrepareCmd() *cobra.Command {
	var flags worktreeMergeFlags
	command := &cobra.Command{
		Use: "prepare <source-worktree...>", Short: "Create and validate an isolated local integration candidate",
		Args: cobra.MinimumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := validateWorktreeMergeFlags(flags); err != nil {
				return err
			}
			campaign := newWorktreeMergeProgress(command, flags)
			receipt, err := orchestrate.PrepareWorktreeMerge(command.Context(), prepareMergeOptions(flags, args, campaign.reporter()))
			finishWorktreeMergeProgress(campaign, receipt, err)
			if writeErr := writeWorktreeMergeReceipt(command.OutOrStdout(), flags.format, receipt); writeErr != nil && err == nil {
				return writeErr
			}
			return err
		},
	}
	bindWorktreeMergeFlags(command, &flags, true, false)
	return command
}

func newWorktreeMergeLandCmd(name string) *cobra.Command {
	var flags worktreeMergeFlags
	command := &cobra.Command{
		Use: name + " <candidate-worktree-or-receipt>", Short: "Resume a receipt and land its exact integration candidate",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := validateWorktreeMergeFlags(flags); err != nil {
				return err
			}
			campaign := newWorktreeMergeProgress(command, flags)
			receipt, err := orchestrate.ResumeWorktreeMerge(command.Context(), landMergeOptions(flags, args[0], campaign.reporter()))
			finishWorktreeMergeProgress(campaign, receipt, err)
			if writeErr := writeWorktreeMergeReceipt(command.OutOrStdout(), flags.format, receipt); writeErr != nil && err == nil {
				return writeErr
			}
			return err
		},
	}
	bindWorktreeMergeFlags(command, &flags, false, true)
	return command
}

func newWorktreeMergeRevertCmd() *cobra.Command {
	var flags worktreeMergeFlags
	command := &cobra.Command{
		Use: "revert <landing-receipt>", Short: "Prepare and land a forward revert without rewriting history",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := validateWorktreeMergeFlags(flags); err != nil {
				return err
			}
			campaign := newWorktreeMergeProgress(command, flags)
			progress.Report(campaign.reporter(), progress.Event{Operation: "worktree_merge", Phase: "prepare_revert", State: progress.Started, Detail: args[0]})
			receipt, err := orchestrate.PrepareWorktreeMergeRevert(command.Context(), projectsRoot, args[0], flags.timeout, flags.retry)
			if err == nil {
				receipt, err = orchestrate.LandWorktreeMerge(command.Context(), landMergeOptions(flags, receipt.ReceiptPath, campaign.reporter()))
			}
			finishWorktreeMergeProgress(campaign, receipt, err)
			if writeErr := writeWorktreeMergeReceipt(command.OutOrStdout(), flags.format, receipt); writeErr != nil && err == nil {
				return writeErr
			}
			return err
		},
	}
	bindWorktreeMergeFlags(command, &flags, false, true)
	return command
}

func bindWorktreeMergeFlags(command *cobra.Command, flags *worktreeMergeFlags, prepare, land bool) {
	if prepare {
		command.Flags().StringVar(&flags.target, "target", "", "target branch; defaults to the remote default branch")
		command.Flags().StringVar(&flags.model, "model", "unknown", "model identity recorded in the candidate Work Log")
		command.Flags().StringVar(&flags.runtime, "agent-runtime", "wb", "agent runtime recorded in the candidate Work Log")
		command.Flags().StringVar(&flags.agentID, "agent-id", "", "agent identity recorded in the candidate Work Log")
		command.Flags().StringVar(&flags.cli, "cli", "wb", "CLI identity recorded in the candidate Work Log")
		command.Flags().StringVar(&flags.provider, "provider", "", "routing or billing provider identity, never a credential")
	}
	if land {
		command.Flags().StringVar(&flags.route, "route", "auto", "landing route: auto, direct, or pr")
		command.Flags().BoolVar(&flags.cleanup, "cleanup", false, "after remote receipt and canonical synchronization, retire absorbed managed assets")
		command.Flags().StringVar(&flags.onFailure, "on-failure", "stop", "post-landing failure action: stop or prepare a forward revert")
		command.Flags().DurationVar(&flags.interval, "check-interval", orchestrate.DefaultCheckPollInterval, "foreground interval between exact GitHub check observations")
	}
	command.Flags().DurationVar(&flags.timeout, "timeout", 8*time.Minute, "bounded command and check wait duration")
	command.Flags().IntVar(&flags.retry, "retry", 0, "retry transient command failures")
	command.Flags().StringVar(&flags.format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&flags.progress, "progress", false, "show progress on stderr even when it is not a terminal")
}

func validateWorktreeMergeFlags(flags worktreeMergeFlags) error {
	if err := requireOutputFormat(flags.format, "text", "json"); err != nil {
		return err
	}
	switch orchestrate.WorktreeMergeRoute(flags.route) {
	case "", orchestrate.WorktreeMergeRouteAuto, orchestrate.WorktreeMergeRouteDirect, orchestrate.WorktreeMergeRoutePullRequest:
	default:
		return fmt.Errorf("unsupported --route %q; use auto, direct, or pr", flags.route)
	}
	if flags.onFailure != "" && flags.onFailure != "stop" && flags.onFailure != "revert" {
		return fmt.Errorf("unsupported --on-failure %q; use stop or revert", flags.onFailure)
	}
	if flags.retry < 0 || flags.timeout <= 0 {
		return fmt.Errorf("--timeout must be positive and --retry must not be negative")
	}
	return nil
}

func prepareMergeOptions(flags worktreeMergeFlags, sources []string, reporter progress.Reporter) orchestrate.WorktreeMergePrepareOptions {
	return orchestrate.WorktreeMergePrepareOptions{ProjectsRoot: projectsRoot, Sources: sources, Target: flags.target,
		Model: flags.model, AgentRuntime: flags.runtime, AgentID: flags.agentID, CLI: flags.cli, Provider: flags.provider,
		Timeout: flags.timeout, Retry: flags.retry, Progress: reporter, ProgressRequested: flags.progress}
}

func landMergeOptions(flags worktreeMergeFlags, receipt string, reporter progress.Reporter) orchestrate.WorktreeMergeLandOptions {
	return orchestrate.WorktreeMergeLandOptions{ProjectsRoot: projectsRoot, Receipt: receipt,
		Route: orchestrate.WorktreeMergeRoute(flags.route), Cleanup: flags.cleanup, OnFailure: flags.onFailure,
		Timeout: flags.timeout, Retry: flags.retry, CheckPollInterval: flags.interval, Progress: reporter, ProgressRequested: flags.progress}
}

func newWorktreeMergeProgress(command *cobra.Command, flags worktreeMergeFlags) *campaignProgress {
	interactive := console.Interactive(command.ErrOrStderr(), nonInteractive)
	out := command.ErrOrStderr()
	heartbeat := time.Second
	if flags.progress && !interactive {
		out = &worktreeMergeLineWriter{out: out}
		heartbeat = 30 * time.Second
	}
	return newCampaignProgressWithHeartbeat(out, flags.progress || interactive, "worktree merge", heartbeat)
}

// worktreeMergeLineWriter turns the live renderer's carriage-return updates
// into newline-delimited diagnostics for non-terminal agent tools. Those tools
// can otherwise buffer a healthy stage until the final newline and recreate
// the silence --progress is meant to remove.
type worktreeMergeLineWriter struct {
	out     io.Writer
	mu      sync.Mutex
	started bool
}

func (writer *worktreeMergeLineWriter) Write(payload []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	text := string(payload)
	if strings.HasPrefix(text, "\r") {
		text = strings.TrimPrefix(text, "\r")
		if writer.started {
			text = "\n" + text
		}
		writer.started = true
	}
	if text == "\n" {
		writer.started = false
	}
	if _, err := io.WriteString(writer.out, text); err != nil {
		return 0, err
	}
	return len(payload), nil
}

func finishWorktreeMergeProgress(campaign *campaignProgress, receipt orchestrate.WorktreeMergeReceipt, err error) {
	if err != nil {
		status := strings.TrimSpace(string(receipt.Status))
		if status == "" {
			status = "failed"
		}
		campaign.finish(status)
		return
	}
	status := strings.TrimSpace(string(receipt.Status))
	if status == "" {
		status = "completed"
	}
	campaign.finish(status)
}

func writeWorktreeMergeReceipt(writer io.Writer, format string, receipt orchestrate.WorktreeMergeReceipt) error {
	if format == "json" {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(receipt)
	}
	_, err := fmt.Fprintf(writer, "status: %s\nrepository: %s\ntarget: %s\ncandidate: %s\nreceipt: %s\nresume: wb %s\n",
		receipt.Status, receipt.Repository, receipt.Target, receipt.Candidate.SHA, receipt.ReceiptPath, strings.Join(receipt.ResumeArgs, " "))
	return err
}
