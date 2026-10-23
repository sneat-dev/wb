package cmdworktree

import (
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/worktreeend"
	"github.com/spf13/cobra"
	"strings"
)

func printWorktreeEnd(command *cobra.Command, format string, result worktreeend.Result) error {
	if format == "json" {
		encoder := json.NewEncoder(command.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	out := command.OutOrStdout()
	verb := "would end"
	if result.Applied {
		verb = "ended"
	}
	if _, err := fmt.Fprintf(out, "%s task %s\n", verb, result.Task); err != nil {
		return err
	}
	for _, member := range result.Members {
		if _, err := fmt.Fprintf(out, "  %-28s %-10s %s\n", member.Repository, member.Action, member.Worktree); err != nil {
			return err
		}
		if len(member.Dirty) > 0 {
			if _, err := fmt.Fprintf(out, "    uncommitted: %s\n", strings.Join(member.Dirty, ", ")); err != nil {
				return err
			}
		}
		if member.CaptureRef != "" {
			if _, err := fmt.Fprintf(out, "    captured at %s — recover with `git stash apply %s`\n", member.CaptureRef, member.CaptureRef); err != nil {
				return err
			}
		}
		if member.Detail != "" {
			if _, err := fmt.Fprintf(out, "    ! %s\n", member.Detail); err != nil {
				return err
			}
		}
	}
	if result.ClaimOutcome != "" {
		if _, err := fmt.Fprintf(out, "  claim: %s\n", result.ClaimOutcome); err != nil {
			return err
		}
	}
	if !result.Applied {
		if _, err := fmt.Fprintln(out, "nothing was changed; re-run with --apply"); err != nil {
			return err
		}
	}
	return nil
}
