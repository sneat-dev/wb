package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

var branchArchiveTargetPreflight = func(ctx context.Context, repository string) (worktrees.RetiredArchivePlan, error) {
	return worktrees.PlanRetiredArchivePreflight(ctx, repository, nil)
}

func newBranchArchiveTargetCmd() *cobra.Command {
	var repository, format string
	command := &cobra.Command{
		Use:   "archive-target",
		Short: "Show the configured private retirement archive target",
		Long: `Resolve the user-only retirement archive target for one source repository and inspect
its current GitHub visibility. This is read-only: it does not rename remote refs,
remove worktrees, export Work Logs, create reports, or accept --apply.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json", "yaml"); err != nil {
				return err
			}
			if repository == "" {
				return fmt.Errorf("--repo is required; use owner/repository")
			}
			plan, err := branchArchiveTargetPreflight(command.Context(), repository)
			if err != nil {
				return err
			}
			switch format {
			case "text":
				if _, err := fmt.Fprintf(command.OutOrStdout(), "source: %s\narchive-target: %s\nstatus: %s\n", plan.SourceRepository, plan.ArchiveRepository, plan.Outcome); err != nil {
					return err
				}
				if plan.Refusal != "" {
					_, err := fmt.Fprintf(command.OutOrStdout(), "refusal: %s\n", plan.Refusal)
					return err
				}
				return nil
			case "json":
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(plan)
			case "yaml":
				raw, err := yamlCompatibleJSON(plan)
				if err != nil {
					return err
				}
				_, err = command.OutOrStdout().Write(raw)
				return err
			default:
				return fmt.Errorf("unsupported format %q; use text, json, or yaml", format)
			}
		},
	}
	command.Flags().StringVar(&repository, "repo", "", "exact source owner/repository")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text, json, or yaml")
	return command
}
