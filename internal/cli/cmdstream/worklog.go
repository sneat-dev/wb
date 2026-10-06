// Package cmdstream constructs all stream verbs with isolated operation bindings.
package cmdstream

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

type workLogFlags struct {
	mode, effortID, runID, initiator, agentID, agentRuntime, model, cli, provider, originalPrompt string
}

func addStreamWorkLogFlags(command *cobra.Command, mode, effortID, runID, initiator, agentID, agentRuntime, model, cli, provider, originalPrompt *string) {
	command.Flags().StringVar(mode, "mode", "auto", "execution mode: auto, agent, or manual")
	command.Flags().StringVar(effortID, "effort", "", "effort identifier recorded in the Work Log")
	command.Flags().StringVar(runID, "run", "", "run identifier recorded in the Work Log")
	command.Flags().StringVar(initiator, "initiator", "", "human who asked for this work (required by --mode manual)")
	command.Flags().StringVar(agentID, "agent", "", "agent identifier recorded in the Work Log")
	command.Flags().StringVar(agentRuntime, "agent-runtime", "", "agent harness recorded in the Work Log")
	command.Flags().StringVar(model, "model", "", "exact child model, or the explicit value unknown")
	command.Flags().StringVar(cli, "cli", "", "CLI recorded in the Work Log")
	command.Flags().StringVar(provider, "provider", "", "routing or billing provider identity, never a credential")
	command.Flags().StringVar(originalPrompt, "original-prompt-file", "", "file holding the exact task request, or - for stdin (required)")
}

// streamWorkLog prepares the same Work Log options `wb worktree create`
// requires, including the mandatory private prompt archive. A stream is a
// task, so it carries a task's provenance; nothing here is stream-specific.
func streamWorkLog(runtime shared.Runtime, deps Dependencies, command *cobra.Command, task string, flags workLogFlags) (worktrees.WorkLogOptions, bool, error) {
	if flags.mode != "" && flags.mode != "auto" && flags.mode != "agent" && flags.mode != "manual" {
		return worktrees.WorkLogOptions{}, false, fmt.Errorf("unsupported execution mode %q; use auto, agent, or manual", flags.mode)
	}
	agentMode := flags.mode == "agent" || (flags.mode == "auto" && (strings.TrimSpace(flags.agentID) != "" || strings.TrimSpace(flags.agentRuntime) != ""))
	if flags.mode == "manual" && strings.TrimSpace(flags.initiator) == "" {
		return worktrees.WorkLogOptions{}, false, errors.New("manual execution mode requires --initiator so the non-agent mutation is auditable")
	}
	if agentMode {
		if !deps.RegisteredSession() {
			return worktrees.WorkLogOptions{}, false, errors.New("agent-mode stream creation requires a live registered session; register before the first mutation with `wb session register --pid $PPID --runtime <harness> --model <model>`, or select --mode manual --initiator <human>")
		}
	}
	workLog := worktrees.WorkLogOptions{
		EffortID: flags.effortID, RunID: flags.runID, Initiator: flags.initiator,
		AgentID: flags.agentID, AgentRuntime: flags.agentRuntime, Model: flags.model,
		CLI: flags.cli, Provider: flags.provider, OriginalPrompt: flags.originalPrompt,
		RequireOriginalPrompt: true,
	}
	if flags.originalPrompt == "-" {
		stdinBytes, err := io.ReadAll(command.InOrStdin())
		if err != nil {
			return worktrees.WorkLogOptions{}, false, fmt.Errorf("read --original-prompt-file - from stdin: %w", err)
		}
		workLog, err = workLog.WithOriginalPromptFromStdin(stdinBytes)
		if err != nil {
			return worktrees.WorkLogOptions{}, false, err
		}
	}
	prepared, err := deps.PrepareWorkLog(runtime.Flags().ProjectsRoot, task, workLog)
	if err != nil {
		return worktrees.WorkLogOptions{}, false, err
	}
	return prepared, agentMode, nil
}
