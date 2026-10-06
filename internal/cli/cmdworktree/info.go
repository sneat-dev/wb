package cmdworktree

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/worktreecollab"
	"github.com/sneat-dev/wb/internal/worktreerun"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"io"
	"strings"
)

// NewInfo constructs the redacted inspection command.
func NewInfo(runtime shared.Runtime, inspect func(context.Context, worktreerun.InfoRequest) (worktreerun.InfoDocument, error)) *cobra.Command {
	var format string
	command := &cobra.Command{
		Use:   "info [worktree-path]",
		Short: "Show redacted identity and Git state for one worktree",
		Long: `Print a safe, redacted summary of one worktree's journal and live Git state.

Includes manifest, claim identity, prompt ordinals and digests, and dirty/head
evidence. Prompt bodies are never printed — use 'wb worktree log' when an agent
needs the exact original prompt and steering instructions.

Default text is human-readable. --format json emits the same redacted payload
as one JSON document on stdout.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			document, err := inspect(command.Context(), worktreerun.InfoRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, Worktree: path})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(document)
			}
			text := worktrees.FormatWorktreeInfoText(document.WorkLogView) + formatMergerLaneClaimText(document.MergerLaneClaim)
			if document.Collaboration != nil {
				text += formatCollaborationInfo(*document.Collaboration)
			}
			_, err = io.WriteString(command.OutOrStdout(), text)
			return err
		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func formatMergerLaneClaimText(claim *orchestrate.MergeLaneClaim) string {
	if claim == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Merger lane\n")
	fmt.Fprintf(&b, "claimed: true\n")
	fmt.Fprintf(&b, "lane: %s\n", claim.Lane)
	fmt.Fprintf(&b, "target: %s\n", claim.Target)
	fmt.Fprintf(&b, "status: %s\n", claim.Status)
	fmt.Fprintf(&b, "receipt: %s\n", claim.ReceiptPath)
	b.WriteString("A merger lane already selected this branch for a batch it may land at any\n")
	b.WriteString("time. Hand any change (a revert included) to the lane instead of pushing\n")
	b.WriteString("directly to this branch.\n\n")
	return b.String()
}

func formatCollaborationInfo(view worktreecollab.View) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Current owner: %s (%s)\n", view.Owner, view.OwnerStatus)
	fmt.Fprintf(&b, "Worktree ID: %s\n", view.Checkout.ID)
	for _, participant := range view.Joined {
		fmt.Fprintf(&b, "Joined session: %s (live=%t)\n", participant.SessionID, participant.Live)
	}
	return b.String()
}
