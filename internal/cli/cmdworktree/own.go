package cmdworktree

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/checkoutsetup"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

type OwnOperations struct {
	Admission JournalAdmission
	Record    func(checkoutsetup.OwnershipRequest) (checkoutsetup.OwnershipResult, error)
}

func NewOwn(operations OwnOperations) *cobra.Command {
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

Flags override the environment. Defaults to the current directory. Agent mode
is fail-closed on the live registry; manual mode requires --initiator for an
auditable non-agent ownership declaration.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			admitted, releaseAdmission, err := operations.Admission(command, true)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			result, err := operations.Record(checkoutsetup.OwnershipRequest{Path: path, Admitted: admitted, Overrides: identity})
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(command.OutOrStdout(), "%s: owner recorded as %s (pid %d)\n",
				result.Path, result.Identity.Agent(), result.Identity.PID)
			return nil
		},
	}
	command.Flags().IntVar(&identity.PID, "pid", 0, "process id of the agent session doing the work")
	command.Flags().StringVar(&identity.Runtime, "runtime", "", "harness running the agent, e.g. claude-code, copilot-cli, codex")
	command.Flags().StringVar(&identity.Model, "model", "", "model identifier driving the session")
	command.Flags().StringVar(&identity.AgentID, "agent-id", "", "session id, when the harness exposes one")
	return command
}
