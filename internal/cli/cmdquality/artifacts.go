package cmdquality

import (
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/qualityrun"
	"github.com/spf13/cobra"
	"io"
)

func newCoverageBaselineCmd(deps Dependencies) *cobra.Command {
	var (
		module     string
		sha        string
		out        string
		includeE2E bool
	)
	command := &cobra.Command{
		Use:   "baseline <coverage-profile>",
		Short: "Write the per-package uncovered-count baseline from a measured coverage profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return deps.Baseline(cmd.Context(), qualityrun.BaselineRequest{Profile: args[0], Module: module, SHA: sha, Out: out, IncludeE2E: includeE2E})
		},
	}
	command.Flags().StringVar(&module, "module", ".", "path to the Go module root (its go.mod names the module path coverage profiles use)")
	command.Flags().StringVar(&sha, "sha", "", "commit SHA the profile was measured at, recorded in the baseline for traceability")
	command.Flags().BoolVar(&includeE2E, "include-e2e", false, "profile combines default and native E2E/contract coverage")
	command.Flags().StringVar(&out, "out", "coverage-baseline.json", "output path for the baseline JSON")
	return command
}

func newCoverageSummaryCmd(deps Dependencies) *cobra.Command {
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
			return deps.Summary(cmd.Context(), qualityrun.SummaryRequest{Profile: args[0], Module: module, Out: out, Meta: quality.CoverageSummaryMeta{Repository: repo, SHA: sha, Ref: ref, WorkflowRunID: workflowRunID, WorkflowRunURL: workflowRunURL}})
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

func newCoverageWorklistCmd(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var (
		module   string
		unitSize int
		format   string
	)
	command := &cobra.Command{
		Use:   "worklist <coverage-profile>",
		Short: "Group a coverage profile's uncovered blocks into sized, function-whole worklist units",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "text" && format != "json" {
				return runtime.ExitError(shared.ExitUsage, fmt.Sprintf("--format must be text or json, not %q", format))
			}
			worklist, err := deps.Worklist(cmd.Context(), qualityrun.WorklistRequest{Profile: args[0], Module: module, UnitSize: unitSize})
			if err != nil {
				return err
			}
			return writeWorklistOutput(cmd.OutOrStdout(), worklist, format)
		},
	}
	command.Flags().StringVar(&module, "module", ".", "path to the Go module root (its go.mod names the module path the coverage profile uses, and its source maps blocks to functions)")
	command.Flags().IntVar(&unitSize, "unit-size", 300, "target statement count per worklist unit")
	command.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return command
}

func writeWorklistOutput(out io.Writer, worklist quality.Worklist, format string) error {
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(worklist)
	}
	returnValue, err := io.WriteString(out, qualityrun.WorklistText(worklist))
	_ = returnValue
	return err
}
