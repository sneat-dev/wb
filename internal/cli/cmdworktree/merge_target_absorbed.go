package cmdworktree

import (
	"encoding/json"
	"fmt"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/spf13/cobra"
)

func newWorktreeMergeAcknowledgeTargetAbsorbedCmd(inv *mergeInvocation) *cobra.Command {
	var apply bool
	var format string
	command := &cobra.Command{
		Use:   "acknowledge-target-absorbed [merge-receipt...]",
		Short: "Release merger lanes held by receipts whose work already reached the target",
		Long: `Prove, for each named receipt (or every non-complete receipt when none is
named), that all of its recorded source, candidate, and landing commits are
ancestors of the freshly fetched remote target. Such a receipt has nothing
left to land, so it no longer owns its merger lane. With --apply WB writes a
separate immutable acknowledgement bound to the receipt's exact bytes; it
never rewrites the receipt, a worktree, or a branch. The lane scan in
merge prepare and land writes the same acknowledgement on demand, so this
verb only clears lanes ahead of their next lander. Live candidate branch
heads must be on the target too; a landed receipt also needs passed
post-target CI for its exact landing and 15 minutes without updates.
preparing and landed_post_target_ci_failed receipts are never released.
Dry-run is the default.`,
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := inv.bindings.Admission(command, apply)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			results, err := inv.operations.SweepTargetAbsorbedWorktreeMergeReceipts(command.Context(), orchestrate.WorktreeMergeTargetAbsorbedSweepOptions{
				ProjectsRoot: inv.runtime.Flags().ProjectsRoot, Receipts: args, Apply: apply,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(results)
			}
			for _, result := range results {
				line := fmt.Sprintf("%s\t%s\t%s", result.Outcome, result.Status, result.Receipt)
				if result.AcknowledgementPath != "" {
					line += "\tacknowledgement: " + result.AcknowledgementPath
				}
				if result.Detail != "" {
					line += "\t" + result.Detail
				}
				_, _ = fmt.Fprintln(command.OutOrStdout(), line)
			}
			if !apply {
				_, _ = fmt.Fprintln(command.OutOrStdout(), "dry-run only, pass --apply to write")
			}
			return nil
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "write the separate audited target-absorbed acknowledgements")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	addMergeAdmissionFlags(command)
	return command
}
