package cmdrun

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/runexec"
	"github.com/spf13/cobra"
)

func expandChanged(cmd *cobra.Command, args []string, target string, operation func(context.Context, runexec.ChangedRequest) (runexec.ChangedResult, error), usage func(string) error) ([]string, error) {
	for _, arg := range args {
		if arg == "-args" || arg == "--args" || arg == "--" {
			return nil, usage("--changed cannot be combined with -args/--args or a nested -- in the command: WB appends the changed package patterns immediately after the given command and its own flags, and a -args/--args/-- boundary would instead send them to the test binary or beyond")
		}
	}
	result, err := operation(cmd.Context(), runexec.ChangedRequest{Argv: append([]string(nil), args...), Target: target})
	if err != nil {
		return nil, err
	}
	if result.MissingTarget {
		return nil, usage("--changed could not detect this repository's default branch; pass --target <branch or ref>")
	}
	if result.NoWork {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "wb run --changed: no changed Go packages against %s (merge base %s); nothing to run\n", result.Target, result.MergeBase)
		return nil, err
	}
	return result.Argv, nil
}
