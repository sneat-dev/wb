package cmdbranch

import (
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

func newArchiveTarget(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var repository, format string
	command := &cobra.Command{
		Use:   "archive-target",
		Short: "Show the configured private retirement archive target",
		Long: `Resolve the user-only retirement archive target for one source repository and inspect
its current GitHub visibility. This is read-only: it does not rename remote refs,
remove worktrees, export Work Logs, create reports, or accept --apply.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json", "yaml"); err != nil {
				return err
			}
			if repository == "" {
				return fmt.Errorf("--repo is required; use owner/repository")
			}
			plan, err := deps.ArchiveTarget(command.Context(), repository)
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
				err = encoder.Encode(plan)
			case "yaml":
				raw, err := yamlCompatibleJSON(plan)
				if err != nil {
					return err
				}
				_, err = command.OutOrStdout().Write(raw)
				return err
			}
			return err
		},
	}
	command.Flags().StringVar(&repository, "repo", "", "exact source owner/repository")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text, json, or yaml")
	return command
}
