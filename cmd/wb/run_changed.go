package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/quality"
)

// expandChangedRunArgs implements `wb run --changed -- <command>` (issue
// #570, spec/plans/coverage-to-100/README.md task-19): it appends the Go
// packages a local diff touches, against target (or the repository's
// detected default branch when target is empty), to args. A nil, nil
// return means nothing changed — the caller must print nothing further and
// exit 0 without running the command; expandChangedRunArgsResult itself
// already printed the explanatory line.
//
// Only tracked changes are ever considered: `git diff` never reports
// untracked files, staged, unstaged, or committed, so a brand-new untracked
// package, or a new untracked file in an otherwise unchanged package, is
// never part of what "changed" here.
func expandChangedRunArgs(cmd *cobra.Command, args []string, target string) ([]string, error) {
	for _, argument := range args {
		// cmd/go treats "--args" identically to "-args" (both start the test
		// binary's own argument list), so both must be rejected the same way
		// (review-726 finding L3).
		if argument == "-args" || argument == "--args" || argument == "--" {
			return nil, usageError("--changed cannot be combined with -args/--args or a nested -- in the command: WB appends the changed package patterns immediately after the given command and its own flags, and a -args/--args/-- boundary would instead send them to the test binary or beyond")
		}
	}
	repoRoot, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(target) == "" {
		target = hooks.DetectDefaultBranch(repoRoot)
		if strings.TrimSpace(target) == "" {
			return nil, usageError("--changed could not detect this repository's default branch; pass --target <branch or ref>")
		}
	}
	result, err := quality.ChangedPackages(cmd.Context(), repoRoot, target)
	if err != nil {
		return nil, fmt.Errorf("wb run --changed: %w", err)
	}
	if len(result.Packages) == 0 {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "wb run --changed: no changed Go packages against %s (merge base %s); nothing to run\n", result.Target, result.MergeBase); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return append(append([]string(nil), args...), result.Packages...), nil
}
