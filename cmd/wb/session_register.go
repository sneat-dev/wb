package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/session"
)

func newSessionRegisterCmd() *cobra.Command {
	var record session.Record
	command := &cobra.Command{
		Use:   "register",
		Short: "Announce this agent session so later WB writes can be attributed to it",
		Long: `Announce this agent session so later WB writes can be attributed to it.

Run it once when a session starts. WB records the declared identity along with
its own version and binary path, and evaluates liveness from the PID whenever
the record is read.

The PID must be the agent process, not the shell that runs this command. From a
harness tool call that is the shell's parent:

  wb session register --pid $PPID --runtime claude-code --model <model>

A start-up hook cannot supply it: hooks run in an isolated subprocess whose
parent is an intermediate shell rather than the agent, so a hook should prompt
the agent to register rather than guess a PID on its behalf.

Registering again for the same PID replaces the record, so a session that
corrects its model does not have to clean up after itself.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			directory, err := sessionDir()
			if err != nil {
				return err
			}
			written, err := session.Register(directory, record)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(command.OutOrStdout(),
				"registered %s (pid %d) using wb %s\n",
				sessionLabel(written), written.PID, written.WBVersion)
			return nil
		},
	}
	command.Flags().IntVar(&record.PID, "pid", 0, "process id of the agent session, e.g. $PPID from a tool call")
	command.Flags().StringVar(&record.Runtime, "runtime", "", "harness running the agent, e.g. claude-code, copilot-cli, codex")
	command.Flags().StringVar(&record.Model, "model", "", "model identifier driving the session")
	command.Flags().StringVar(&record.AgentID, "agent-id", "", "session id, when the harness exposes one")
	return command
}

func sessionLabel(record session.Record) string {
	switch {
	case record.Runtime != "" && record.AgentID != "":
		return record.Runtime + "/" + record.AgentID
	case record.Runtime != "":
		return record.Runtime
	case record.AgentID != "":
		return record.AgentID
	default:
		return "an unnamed session"
	}
}
