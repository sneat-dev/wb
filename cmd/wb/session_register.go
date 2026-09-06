package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/session"
)

var (
	sessionRegisterCurrentPID     = os.Getpid
	sessionRegisterParentPID      = os.Getppid
	sessionRegisterRuntimeProcess = session.IsRuntimeProcess
)

func newSessionRegisterCmd() *cobra.Command {
	var record session.Record
	command := &cobra.Command{
		Use:   "register",
		Short: "Announce this agent session so later WB writes can be attributed to it",
		Long: `Announce this agent session so later WB writes can be attributed to it.

Run it once when a session starts. WB assigns a stable WB session ID, records
the declared identity along with its own version and binary path, and evaluates
liveness from the PID whenever the record is read. Re-registering the same PID
without --wb-session-id preserves its WB identity.

The PID must be the agent process, not the shell that runs this command. From a
harness tool call that is the shell's parent:

  wb session register --pid $PPID --runtime claude-code --model <model>

A start-up hook cannot supply it: hooks run in an isolated subprocess whose
parent is an intermediate shell rather than the agent, so a hook should prompt
the agent to register rather than guess a PID on its behalf.

Codex/live-harness setup: make registration the first WB command issued by the
agent, before any mutating command:

  wb session register --pid $PPID --runtime codex --model <exact-model>

On systems where the shell tail-execs its final command, WB may see the live
Codex app-server as its direct parent. WB accepts that parent only when its
kernel-reported executable is codex and its process role is app-server. For a
shell-safe form that also works with older WB builds, keep the shell alive:

  wb session register --pid "$PPID" --runtime codex --model <exact-model>; status=$?; exit "$status"

Do not substitute $$ (the intermediate shell) or register WB's own PID. An
agent-mode create requires this live registration; for an intentional human
operation use --mode manual --initiator <human> instead.

Registering again for the same PID replaces the record, so a session that
corrects its model does not have to clean up after itself.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			// Never register WB itself: a tail-exec'd shell makes $$ become the
			// WB PID. A live shell remains rejected, but a tail-exec'd command
			// may make the harness the direct parent, so accept that case only
			// when kernel process evidence identifies the declared runtime.
			if record.PID == sessionRegisterCurrentPID() {
				return fmt.Errorf("session PID %d is WB itself; register the live harness with --pid $PPID from its tool-call shell", record.PID)
			}
			if record.PID == sessionRegisterParentPID() && !sessionRegisterRuntimeProcess(record.PID, record.Runtime) {
				return fmt.Errorf("session PID %d is the intermediate shell; register the live harness with --pid $PPID from its tool-call shell", record.PID)
			}
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
	command.Flags().StringVar(&record.WBSessionID, "wb-session-id", "", "preallocated stable WB session ID (normally generated automatically)")
	command.Flags().StringVar(&record.Machine, "machine", "", "canonical WB machine name (default: local hostname)")
	command.Flags().StringVar(&record.Runtime, "runtime", "", "harness running the agent, e.g. claude-code, copilot-cli, codex")
	command.Flags().StringVar(&record.Model, "model", "", "model identifier driving the session")
	command.Flags().StringVar(&record.NativeHarnessID, "native-harness-id", "", "native session ID, when the harness exposes one")
	command.Flags().StringVar(&record.AgentID, "agent-id", "", "legacy alias for --native-harness-id")
	command.Flags().StringVar(&record.TmuxName, "tmux-name", "", "tmux session containing this agent")
	command.Flags().StringVar(&record.PredecessorWBSessionID, "predecessor-wb-session-id", "", "WB session ID that handed off to this session")
	command.Flags().StringVar(&record.HandoffID, "handoff-id", "", "handoff that created this session")
	return command
}

func sessionLabel(record session.Record) string {
	nativeID := record.NativeHarnessID
	if nativeID == "" {
		nativeID = record.AgentID
	}
	switch {
	case record.Runtime != "" && nativeID != "":
		return record.Runtime + "/" + nativeID
	case record.Runtime != "":
		return record.Runtime
	case nativeID != "":
		return nativeID
	default:
		return "an unnamed session"
	}
}
