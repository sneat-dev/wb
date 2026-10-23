package cmdworktree

import (
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/canonicalrescue"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

func renderRescueReport(runtime shared.Runtime, cmd *cobra.Command, format string, apply bool, report canonicalrescue.Report) error {
	if format == "json" {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	out := cmd.OutOrStdout()
	if !report.Dirty() {
		_, err := fmt.Fprintf(out, "%s is clean\n", report.Path)
		return err
	}
	if _, err := fmt.Fprintf(out, "%s holds %d uncommitted change(s), %d untracked:\n",
		report.Path, len(report.Changes), report.UntrackedCount); err != nil {
		return err
	}
	for index, change := range report.Changes {
		if index == 20 {
			if _, err := fmt.Fprintf(out, "  … and %d more\n", len(report.Changes)-index); err != nil {
				return err
			}
			break
		}
		if _, err := fmt.Fprintf(out, "  %s %s\n", change.Status, change.Path); err != nil {
			return err
		}
	}
	if !apply {
		if _, err := fmt.Fprintf(out, "\nNothing has been changed. To preserve this onto a branch:\n  wb worktree rescue %s --apply --push\n", report.Path); err != nil {
			return err
		}
		return runtime.ExitError(shared.ExitFindings, fmt.Sprintf("%s holds uncommitted work", report.Path))
	}
	if _, err := fmt.Fprintf(out, "\ncaptured onto %s (%s)\n", report.RescueBranch, report.RescueCommit); err != nil {
		return err
	}
	if report.Pushed {
		if _, err := fmt.Fprintln(out, "pushed to the remote"); err != nil {
			return err
		}
	}
	if report.Restored {
		_, err := fmt.Fprintf(out, "%s is now clean\n", report.Path)
		return err
	}
	_, err := fmt.Fprintf(out,
		"%s is still dirty on purpose. Review the branch, then clean the clone:\n  wb worktree rescue %s --apply --branch %s --restore\n\nThat second run recognises the branch it already created and reuses it, so\nnothing is captured twice.\n",
		report.Path, report.Path, report.RescueBranch)
	return err
}
