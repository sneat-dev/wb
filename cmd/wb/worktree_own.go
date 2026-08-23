package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func newWorktreeOwnCmd() *cobra.Command {
	var identity worktrees.AgentIdentity
	command := &cobra.Command{
		Use:   "own [worktree-path]",
		Short: "Declare which agent session is working in this worktree",
		Long: `Declare which agent session is working in this worktree.

WB cannot tell on its own whether a worktree is still being worked on. It is a
short-lived command, so its own process id is dead moments after it runs, and a
recycled id would later report an abandoned worktree as active. Only the
session driving the work knows its own identity, so it records it here.

Once declared, 'wb worktree info' reports the owner's liveness, and WB stops
warning on writes. The record is append-only: declaring again adds a new entry
rather than overwriting the previous owner, so a worktree handed between
sessions keeps its full chain of custody.

A whole session can declare itself once through the environment instead, which
every later WB command picks up:

  export WB_AGENT_PID=$$ WB_AGENT_RUNTIME=claude-code
  export WB_AGENT_MODEL=<model> WB_AGENT_ID=<session-id>

Flags override the environment. Defaults to the current directory.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			// The local work log refuses a relative path, so resolve before
			// recording; "." is the documented default and must work.
			path, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			// Environment first, flags on top: a session exports once, and a
			// single command can still correct one field without restating
			// the rest.
			declared := worktrees.IdentityFromEnv()
			if identity.Runtime != "" {
				declared.Runtime = identity.Runtime
			}
			if identity.AgentID != "" {
				declared.AgentID = identity.AgentID
			}
			if identity.Model != "" {
				declared.Model = identity.Model
			}
			if identity.PID > 0 {
				declared.PID = identity.PID
			}
			if !declared.Declared() {
				return fmt.Errorf("nothing to declare: pass --pid/--runtime/--model or set %s/%s/%s",
					worktrees.EnvAgentPID, worktrees.EnvAgentRuntime, worktrees.EnvAgentModel)
			}

			if err := worktrees.RecordCustody(path, "", "worktree own", declared); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(command.OutOrStdout(), "%s: owner recorded as %s (pid %d)\n",
				path, declared.Agent(), declared.PID)
			return nil
		},
	}
	command.Flags().IntVar(&identity.PID, "pid", 0, "process id of the agent session doing the work")
	command.Flags().StringVar(&identity.Runtime, "runtime", "", "harness running the agent, e.g. claude-code, copilot-cli, codex")
	command.Flags().StringVar(&identity.Model, "model", "", "model identifier driving the session")
	command.Flags().StringVar(&identity.AgentID, "agent-id", "", "session id, when the harness exposes one")
	return command
}
