package main

import (
	"github.com/spf13/cobra"
)

func remoteCommandForTest(inv *invocation, name string) *cobra.Command {
	root := newRemoteCmd(inv)
	command, _, err := root.Find([]string{name})
	if err != nil {
		panic(err)
	}
	// Existing harnesses execute a standalone child; preserve its actual
	// constructor and bindings while avoiding Cobra walking back to the parent.
	root.RemoveCommand(command)
	return command
}
