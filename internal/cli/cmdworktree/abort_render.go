package cmdworktree

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func printAbort(command *cobra.Command, results []worktrees.AbortResult, remaining int, task string) error {
	for _, result := range results {
		if result.Excluded {
			reason := result.Reason
			if reason == "" {
				reason = "not evaluated"
			}
			if _, err := fmt.Fprintf(command.OutOrStdout(), "excluded by --filter %s %s: %s\n", result.Repository, result.Disposition, reason); err != nil {
				return err
			}
			continue
		}
		state := "would seal"
		if !result.Eligible {
			// A dry run must not promise a seal the apply will refuse.
			if _, err := fmt.Fprintf(command.OutOrStdout(), "cannot seal %s %s: %s\n", result.Repository, result.Disposition, result.Reason); err != nil {
				return err
			}
			continue
		}
		if result.Applied && result.Disposition == worktrees.AbortOrphaned {
			state = "sealed absent claim"
		} else if result.Applied && result.WorktreeGone {
			state = "sealed and removed"
		} else if result.Applied {
			state = "sealed and resumable"
		}
		if _, err := fmt.Fprintf(command.OutOrStdout(), "%s %s %s\n", state, result.Repository, result.Disposition); err != nil {
			return err
		}
		if closed := result.ClosedPullRequest; closed != nil {
			if _, err := fmt.Fprintf(command.OutOrStdout(), "  closed pull request %s#%d head %s (%s)%s\n",
				closed.Repository, closed.Number, closed.HeadSHA, closed.Reason, closedAuditSuffix(closed.AuditPath)); err != nil {
				return err
			}
		}
	}
	if remaining > 0 {
		if _, err := fmt.Fprintf(command.OutOrStdout(), "%d repositories excluded by --filter remain unresolved for task %q\n", remaining, task); err != nil {
			return err
		}
	}
	return nil
}

func closedAuditSuffix(path string) string {
	if path == "" {
		return ""
	}
	return "; audit " + path
}
