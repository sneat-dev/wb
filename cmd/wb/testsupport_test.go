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

// runPreparedNpmPublish drives the production entry point
// (runNpmPublishWithPreflight: lock, preflight, operation check, dispatch)
// with a preflight that answers a prepared fleet, so tests exercise the
// campaign-lock contract without a live GitHub or npm call.
func runPreparedNpmPublish(command *cobra.Command, options npmPublishOptions, prepared npmPublishPrepared, inv *invocation) error {
	return runNpmPublishWithPreflight(command, options, func(*invocation, npmPublishOptions) (npmPublishPrepared, error) {
		return prepared, nil
	}, inv)
}
