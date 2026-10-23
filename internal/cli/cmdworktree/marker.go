package cmdworktree

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/checkoutmarker"
	"github.com/sneat-dev/wb/internal/checkoutsetup"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
	"io"
)

type MarkerOperations struct {
	Run     func(context.Context, checkoutsetup.MarkerRequest) ([]checkoutsetup.MarkerOutcome, error)
	Version func() string
}

func NewMarker(runtime shared.Runtime, operations MarkerOperations) *cobra.Command {
	var fleet, dryRun bool
	var format, base string
	command := &cobra.Command{
		Use:   "marker [checkout-path]",
		Short: "Write the per-checkout .worktree.md marker and its ignore rule",
		Long: `Write ` + checkoutmarker.FileName + ` into a checkout, saying what that checkout is.

Every checkout gets one — a canonical clone and a linked worktree alike. The
marker states whether this checkout may be written to, which repository it
belongs to, and, for a worktree, which task and branch it carries. An agent
reads one file and knows where it is.

That universality is the point. A warning file present only in canonical
clones would make a MISSING file read as "nothing objects here", which is the
wrong default for a checkout WB has not reached yet. With a marker everywhere,
absence means "unknown, verify" instead.

The marker is never committed. WB writes it alongside an anchored rule in the
repository's Git exclude file, so ` + "`git status`" + ` stays clean and WB's own hooks
— which refuse an untracked path — are never tripped by it. One rule in the
common Git directory covers the canonical clone and every worktree cut from
it.

Re-running changes nothing once a marker is current: only a marker whose
content would differ by more than its timestamp is rewritten.

--fleet refreshes every canonical clone under --projects-root and every linked
worktree registered to one.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			if fleet && len(args) == 1 {
				return fmt.Errorf("--fleet refreshes every checkout; do not also name one")
			}
			outcomes, err := operations.Run(cmd.Context(), checkoutsetup.MarkerRequest{
				Options: checkoutmarker.DescribeOptions{ProjectsRoot: runtime.Flags().ProjectsRoot, BaseBranch: base, Version: "wb " + operations.Version()},
				Filter:  runtime.Flags().Filter, Fleet: fleet, DryRun: dryRun, Paths: args,
			})
			if err != nil {
				return err
			}
			failures := 0
			for _, outcome := range outcomes {
				if outcome.Error != "" {
					failures++
				}
			}

			if err := renderMarkerOutcomes(cmd.OutOrStdout(), format, dryRun, outcomes); err != nil {
				return err
			}
			if failures > 0 {
				return runtime.ExitError(shared.ExitFindings, fmt.Sprintf("%d checkout(s) could not be described", failures))
			}
			return nil
		},
	}
	command.Flags().BoolVar(&fleet, "fleet", false, "refresh every canonical clone under --projects-root and every worktree registered to one")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "report what would change without writing anything")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().StringVar(&base, "base", "main", "protected canonical base branch named in the marker")
	return command
}
func renderMarkerOutcomes(out io.Writer, format string, dryRun bool, outcomes []checkoutsetup.MarkerOutcome) error {
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(outcomes)
	}
	changed := 0
	for _, outcome := range outcomes {
		if outcome.Error != "" {
			if err := shared.WriteFormat(out, "✗ %s: %s\n", outcome.Path, outcome.Error); err != nil {
				return err
			}
			continue
		}
		state := "current"
		switch {
		case outcome.MarkerWritten && outcome.ExcludeWritten:
			state = "marker + ignore rule"
		case outcome.MarkerWritten:
			state = "marker"
		case outcome.ExcludeWritten:
			state = "ignore rule"
		}
		if state != "current" {
			changed++
			if dryRun {
				state = "would write " + state
			} else {
				state = "wrote " + state
			}
		}
		if err := shared.WriteFormat(out, "%s %s: %s\n", markerSymbol(outcome), outcome.Path, state); err != nil {
			return err
		}
	}
	if len(outcomes) > 1 {
		return shared.WriteFormat(out, "\n%d checkout(s), %d changed\n", len(outcomes), changed)
	}
	return nil
}
func markerSymbol(outcome checkoutsetup.MarkerOutcome) string {
	if outcome.Kind == string(checkoutmarker.KindCanonical) {
		return "🔒"
	}
	return "✎"
}
