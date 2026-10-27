package cmdpr

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/spf13/cobra"
)

func newLegacyAdopt(runtime shared.Runtime, deps IntegrationDependencies) *cobra.Command {
	var receipt, to, worktree, format string
	var apply bool
	command := &cobra.Command{Use: "adopt-legacy <owner/repository> <integration>", Short: "Archive exact completed legacy merge provenance without approving its code", Long: "Validate a completed direct WB worktree-merge receipt and its exact remotely published parent chain. Dry-run by default. --apply archives only a sanitized portable commit comment; no code, branch or receipt is modified. Unsigned receipt hashes are audit identity, not authorship or approval. Full legacy diffs remain in promotion review, which must bind the current head and exact provenance inventory.", Args: cobra.ExactArgs(2), RunE: func(command *cobra.Command, args []string) error {
		if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
			return err
		}
		if worktree == "" {
			worktree, _ = os.Getwd()
		}
		run := deps.AdoptLegacy
		if run == nil {
			run = orchestrate.AdoptLegacyIntegration
		}
		result, err := run(command.Context(), orchestrate.LegacyAdoptOptions{PromoteOptions: orchestrate.PromoteOptions{Repository: args[0], Integration: args[1], To: to, Worktree: worktree}, Receipt: receipt, Apply: apply})
		if err != nil {
			return err
		}
		if format == "json" {
			raw, _ := json.MarshalIndent(result, "", "  ")
			_, err = fmt.Fprintln(command.OutOrStdout(), string(raw))
			return err
		}
		if _, err := fmt.Fprintf(command.OutOrStdout(), "Legacy provenance only (not approval): %s\n", result.Record.CandidateSHA); err != nil {
			return err
		}
		if result.Binding != nil {
			_, err := fmt.Fprintf(command.OutOrStdout(), "--legacy-record %s=%s\n", result.Binding.URL, result.Binding.Digest)
			return err
		}
		return nil
	}}
	command.Flags().StringVar(&receipt, "receipt", "", "original completed WB merge receipt (required)")
	_ = command.MarkFlagRequired("receipt")
	command.Flags().StringVar(&to, "to", "", "promotion target (default: repository default)")
	command.Flags().StringVar(&worktree, "worktree", "", "managed integration checkout (default: current directory)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&apply, "apply", false, "archive the validated safe provenance projection; default is read-only")
	discovery(command, "legacy provenance receipt adoption archived integration promotion")
	return command
}
