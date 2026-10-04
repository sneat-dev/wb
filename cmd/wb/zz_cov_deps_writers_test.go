package main

import (
	"errors"

	"github.com/spf13/cobra"
)

// cwDepsFailingWriter fails every write, so the writers' error branches are
// reachable without a real broken pipe.
type cwDepsFailingWriter struct{}

func (cwDepsFailingWriter) Write([]byte) (int, error) { return 0, errors.New("cwDeps: write refused") }

// cwDepsNewOutCommand returns a command whose stdout is the given writer.
func cwDepsNewOutCommand(out interface{ Write([]byte) (int, error) }) *cobra.Command {
	command := &cobra.Command{Use: "cw-deps-fixture"}
	command.SetOut(out)
	command.SetErr(out)
	return command
}
