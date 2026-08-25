package main

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func newSessionCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "session",
		Short: "Record and inspect the agent sessions running on this machine",
		Long: `Record and inspect the agent sessions running on this machine.

WB is a short-lived command with no daemon, so it cannot observe a session
starting. A session announces itself once — from a harness start-up hook, or by
hand — and everything WB writes afterwards can be attributed to it without each
command being told again.

A record is a claim, not an observation: WB stores what it was told, adds only
what it can see for itself (its own version and binary path), and evaluates
liveness from the declared PID when the record is read.`,
	}
	command.AddCommand(newSessionRegisterCmd())
	command.AddCommand(newSessionListCmd())
	command.AddCommand(newSessionPruneCmd())
	command.AddCommand(newSessionMoveCmd())
	return command
}

// sessionDir resolves where session records live, creating WB's home if it is
// not there yet so registering works on a fresh machine. Only the registering
// commands use it.
func sessionDir() (string, error) {
	home, err := wbhome.EnsureRoot(projectsRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, session.DirName), nil
}

// sessionDirForRead resolves the same location without creating anything.
// Attribution happens on the write path of unrelated commands, and a command
// that merely records who is working must not bring WB's home into existence
// as a side effect.
func sessionDirForRead() (string, error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, session.DirName), nil
}

// installSessionResolver lets the worktree layer attribute a write to a
// registered session when the environment carries no declaration. Resolution
// walks this process's ancestors and matches only PIDs that registered
// themselves, so it confirms a declaration rather than guessing an owner.
func installSessionResolver() {
	worktrees.SetSessionResolver(func() (worktrees.AgentIdentity, bool) {
		directory, err := sessionDirForRead()
		if err != nil {
			return worktrees.AgentIdentity{}, false
		}
		record, ok := session.ResolveForProcess(directory, os.Getpid())
		if !ok {
			return worktrees.AgentIdentity{}, false
		}
		nativeID := record.NativeHarnessID
		if nativeID == "" {
			nativeID = record.AgentID
		}
		return worktrees.AgentIdentity{
			Runtime: record.Runtime,
			AgentID: nativeID,
			Model:   record.Model,
			PID:     record.PID,
		}, true
	})
}
