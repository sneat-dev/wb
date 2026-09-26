package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/quality"
)

// newCoverageSummaryCmd implements `wb coverage summary`: it parses an
// already-measured Go coverage profile into the standardized coverage summary
// JSON artifact published by CI coverage jobs on every push to main.
func newCoverageSummaryCmd() *cobra.Command {
	var (
		module         string
		repo           string
		sha            string
		ref            string
		workflowRunID  int64
		workflowRunURL string
		out            string
	)
	command := &cobra.Command{
		Use:   "summary <coverage-profile>",
		Short: "Write the standardized coverage summary JSON from a measured coverage profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			modulePath, err := quality.ReadModulePath(module)
			if err != nil {
				return err
			}
			blocks, err := quality.ParseCoverageProfile(args[0])
			if err != nil {
				return err
			}
			summary := quality.SummaryFromProfile(blocks, modulePath, quality.CoverageSummaryMeta{
				Repository:     repo,
				SHA:            sha,
				Ref:            ref,
				WorkflowRunID:  workflowRunID,
				WorkflowRunURL: workflowRunURL,
			})
			if err := quality.WriteCoverageSummary(out, summary); err != nil {
				return fmt.Errorf("write coverage summary %s: %w", out, err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&module, "module", ".", "path to the Go module root (its go.mod names the module path coverage profiles use)")
	command.Flags().StringVar(&repo, "repo", "", "repository full name (e.g. sneat-dev/wb)")
	command.Flags().StringVar(&sha, "sha", "", "commit SHA the profile was measured at, recorded in the summary for traceability")
	command.Flags().StringVar(&ref, "ref", "", "git ref the profile was measured for (e.g. refs/heads/main)")
	command.Flags().Int64Var(&workflowRunID, "workflow-run-id", 0, "GitHub Actions workflow run database ID")
	command.Flags().StringVar(&workflowRunURL, "workflow-run-url", "", "URL to GitHub Actions workflow run")
	command.Flags().StringVar(&out, "out", "coverage-summary.json", "output path for the summary JSON")
	return command
}
