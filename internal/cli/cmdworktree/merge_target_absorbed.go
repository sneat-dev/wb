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
	var actor, reason, format string
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
Dry-run is the default; --apply requires --actor and --reason.`,
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, releaseAdmission, err := inv.bindings.Admission(command, apply)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			sweep, err := inv.operations.SweepTargetAbsorbedWorktreeMergeReceipts(command.Context(), orchestrate.WorktreeMergeTargetAbsorbedSweepOptions{
				ProjectsRoot: inv.runtime.Flags().ProjectsRoot, Receipts: args, Apply: apply, Actor: actor, Reason: reason,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(sweep)
			}
			counts := map[string]int{}
			for _, result := range sweep.Results {
				counts[result.Outcome]++
				line := fmt.Sprintf("%s\t%s\t%s", result.Outcome, result.Status, result.Receipt)
				if result.AcknowledgementPath != "" {
					line += "\tacknowledgement: " + result.AcknowledgementPath
				}
				if result.Detail != "" {
					line += "\t" + result.Detail
				}
				if _, err := fmt.Fprintln(command.OutOrStdout(), line); err != nil {
					return err
				}
			}
			summary := fmt.Sprintf("released: %d, would_release: %d, already_released: %d, not_absorbed: %d, ineligible: %d",
				counts[orchestrate.WorktreeMergeTargetAbsorbedReleased], counts[orchestrate.WorktreeMergeTargetAbsorbedWouldRelease],
				counts[orchestrate.WorktreeMergeTargetAbsorbedAlreadyReleased], counts[orchestrate.WorktreeMergeTargetAbsorbedNotAbsorbed],
				counts[orchestrate.WorktreeMergeTargetAbsorbedIneligible])
			if !apply {
				summary += "\ndry-run only, pass --apply to write"
			}
			_, err = fmt.Fprintln(command.OutOrStdout(), summary)
			return err
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "write the separate audited target-absorbed acknowledgements")
	command.Flags().StringVar(&actor, "actor", "", "required with --apply: trusted operator or agent identity")
	command.Flags().StringVar(&reason, "reason", "", "required with --apply: bounded audited release reason")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	addMergeAdmissionFlags(command)
	return command
}
