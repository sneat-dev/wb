package main

import (
	"github.com/spf13/cobra"
)

// cwDepsNewOutCommand returns a command whose stdout is the given writer.
func cwDepsNewOutCommand(out interface{ Write([]byte) (int, error) }) *cobra.Command {
	command := &cobra.Command{Use: "cw-deps-fixture"}
	command.SetOut(out)
	command.SetErr(out)
	return command
}
