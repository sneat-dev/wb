package cmdsession

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/sessionview"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/secretscan"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/spf13/cobra"
	"strings"
)

const secretOverrideFlagName = "override-secret"
const secretOverrideFlagHelp = "acknowledge one exact secret scan finding as printed in a prior refusal (<rule-id>:<fingerprint>); repeatable, logged, never a blanket bypass"

func NewMove(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var targetMachine, via, configPath, handoverFile, harness, model, resume string
	var summary, validation, remaining, format string
	var overrideSecrets []string
	command := &cobra.Command{
		Use:   "move [worktree-path]",
		Short: "Move this registered session through an exact receipt-gated handoff",
		Long: `Create the source-owned checkpoint for a portable session move.

WB requires a live registered session, an active managed Work Log, a clean
named branch, and a non-empty handover supplied from a file or stdin. The
handover never touches the repo under work: it travels inline in WB's own
private handoff request and is later materialized as a private file for the
successor. WB performs a normal non-force push of the exact source commit,
verifies that exact commit as the remote branch tip, and records an offer
without transferring source custody. It then delivers the exact request
through the selected immutable courier route and starts the successor in a
named tmux session. Omit --to, or pass this machine's validated
remote.machine, to deliver in-process via the loopback courier rather than
SSH. Only a durable target receipt lets WB publish the stable successor
address and seal predecessor custody. If delivery or acknowledgement is
ambiguous, retry the same handoff with --resume; WB repairs the exact
aggregate and never creates a second checkpoint or successor for that retry. For
a whole-session transfer that may be dirty, park then pickup instead.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			resume = strings.TrimSpace(resume)
			if resume != "" && (len(args) != 0 || command.Flags().Changed("handover-file") || command.Flags().Changed("harness") || command.Flags().Changed("model") || command.Flags().Changed("summary") || command.Flags().Changed("validation") || command.Flags().Changed("remaining") || command.Flags().Changed("to") || command.Flags().Changed(secretOverrideFlagName)) {
				return fmt.Errorf("--resume accepts only an existing handoff ID plus optional --via, --config, and --format")
			}
			worktree := "."
			if len(args) != 0 {
				worktree = args[0]
			}
			result, err := deps.Move(command.Context(), sessionrun.MoveRequest{
				ProjectsRoot: runtime.Flags().ProjectsRoot, Worktree: worktree, Target: targetMachine, Via: via, ConfigPath: configPath, Harness: harness, Model: model, Summary: summary, Validation: validation, Remaining: remaining, HandoverFile: handoverFile, Input: command.InOrStdin(), OverrideSecrets: overrideSecrets, ResumeID: resume,
			}, func(warnings []secretscan.Finding) { sessionview.Advisories(command.ErrOrStderr(), warnings) })
			if err != nil {
				return err
			}
			return sessionview.Move(command.OutOrStdout(), format, result)

		},
	}
	command.Flags().StringVar(&targetMachine, "to", "", "target WB machine (default: this machine's remote.machine)")
	command.Flags().StringVar(&via, "via", "", "configured courier: ssh, synchestra, or loopback for this machine")
	command.Flags().StringVar(&configPath, "config", "", "path to wb.yaml (default: ~/.config/wb/wb.yaml)")
	command.Flags().StringVar(&handoverFile, "handover-file", "", "agent-authored handover file, or - for stdin (required)")
	command.Flags().StringVar(&summary, "summary", "", "handover summary recorded in the tracked document and Work Log")
	command.Flags().StringVar(&validation, "validation", "", "validation evidence recorded in the tracked document")
	command.Flags().StringVar(&remaining, "remaining", "", "remaining work and next action recorded in the tracked document")
	command.Flags().StringVar(&harness, "harness", "", "requested successor harness (claude or codex; default: source runtime)")
	command.Flags().StringVar(&model, "model", "", "requested successor model (always passed when set)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().StringVar(&resume, "resume", "", "retry the exact immutable courier route for an existing handoff ID")
	command.Flags().StringArrayVar(&overrideSecrets, secretOverrideFlagName, nil, secretOverrideFlagHelp)
	return command
}
