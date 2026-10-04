package main

import (
	"github.com/spf13/cobra"
)

// Helpers kept for tests only: no production caller remains.

// newRootCmd builds the command tree for callers that only inspect it (help
// text, subcommand paths, flag matrices) rather than execute it through
// runWithStdin. It is the ~50 existing test call sites' entry point, and
// stays a zero-argument constructor: it hands newRootCmdFor a throwaway
// invocation that is never read back.
func newRootCmd() *cobra.Command {
	return newRootCmdFor(&invocation{})
}
