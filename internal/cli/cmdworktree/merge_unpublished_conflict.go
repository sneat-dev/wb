package cmdworktree

import (
	"encoding/json"
	"fmt"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/spf13/cobra"
)

func newWorktreeMergeAcknowledgeUnpublishedConflictCmd(inv *mergeInvocation) *cobra.Command {
	var options orchestrate.WorktreeMergeUnpublishedConflictOptions
	var format string
	command := &cobra.Command{
		Use:   "acknowledge-unpublished-conflict <merge-receipt>",
		Short: "Release a historical unpublished conflict lane while preserving all work",
		Long:  `Inspect one exact land/conflict receipt with a recorded candidate SHA and no recorded publication or landing. Every source, candidate and rebatched predecessor must remain clean, unchanged, correctly claimed, and without a live owner. WB authenticates predecessor receipt and rebatch sidecar hashes. Unpublished candidates need remote-ref and PR-history absence; a published predecessor needs recorded supersession, a fresh exact CLOSED/unmerged PR identity and exact remote ref. WB freshly checks the target and pins receipt identity across lane-lock acquisition. This is a dry-run by default. Preview with --actor and --reason, inspect the returned proof-id, then pass it as --expected-proof-id with --apply. Any changed receipt, claim, checkout or target proof refuses application. An immutable acknowledgement releases only the historical receipt's lane claim; it grants no landing or cleanup authority and preserves all receipts, Work Logs, branches and worktrees. Prepare a new independent landing afterward.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			_, release, err := inv.bindings.Admission(command, options.Apply)
			if err != nil {
				return err
			}
			defer release()
			options.ProjectsRoot, options.Receipt = inv.runtime.Flags().ProjectsRoot, args[0]
			ack, err := inv.operations.AcknowledgeUnpublishedConflict(command.Context(), options)
			if err != nil {
				return err
			}
			if format == "json" {
				return json.NewEncoder(command.OutOrStdout()).Encode(ack)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "status: %s\nreceipt: %s\nproof-id: %s\ncurrent-target: %s\nacknowledgement: %s\n", ack.Status, ack.ReceiptPath, ack.ID, ack.CurrentTargetSHA, ack.AcknowledgementPath)
			if !options.Apply {
				_, _ = fmt.Fprintln(command.OutOrStdout(), "dry-run only; inspect proof and repeat with --apply --expected-proof-id <proof-id>")
			}
			return err
		},
	}
	command.Flags().BoolVar(&options.Apply, "apply", false, "write only the immutable preservation acknowledgement")
	command.Flags().StringVar(&options.Actor, "actor", "", "audited operator identity; required with --apply")
	command.Flags().StringVar(&options.Reason, "reason", "", "audited recovery reason; required with --apply")
	command.Flags().StringVar(&options.ExpectedProofID, "expected-proof-id", "", "exact proof-id from a prior dry-run with the same actor and reason")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	addMergeAdmissionFlags(command)
	return command
}
