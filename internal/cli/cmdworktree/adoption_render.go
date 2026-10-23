package cmdworktree

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/worktrees"
	"io"
	"sort"
	"strings"
)

func writeAdoption(out io.Writer, results []worktrees.AdoptResult, apply bool) error {
	counts := map[string]int{}
	for _, result := range results {
		counts[result.Action]++
		switch result.Action {
		case worktrees.AdoptSkipped:
			if _, err := fmt.Fprintf(out, "skipped %s: %s\n", result.Path, result.Reason); err != nil {
				return err
			}
		case worktrees.AdoptAdopted, worktrees.AdoptWouldAdopt:
			if _, err := fmt.Fprintf(out, "%-13s %-30s %s\n", result.Action, result.Task, result.Path); err != nil {
				return err
			}
		}
	}
	return writeAdoptionTotals(out, counts, apply)
}

func writeAdoptionTotals(out io.Writer, counts map[string]int, apply bool) error {
	actions := make([]string, 0, len(counts))
	for action := range counts {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	for _, action := range actions {
		if _, err := fmt.Fprintf(out, "%-15s %d\n", action, counts[action]); err != nil {
			return err
		}
	}
	if !apply {
		_, err := fmt.Fprintln(out, "dry-run only, pass --apply to write")
		return err
	}
	return nil
}

func writeOrphans(out io.Writer, report worktrees.OrphanReport, only string) error {
	shown := 0
	for _, family := range report.Families {
		if only != "" && family.Disposition != only {
			continue
		}
		shown++
		if _, err := fmt.Fprintf(out, "\n%s [%s] %s\n", family.RootEffort, family.Disposition, family.Reason); err != nil {
			return err
		}
		for _, worktree := range family.Worktrees {
			marks := []string{worktree.Layout}
			if !worktree.HasManifest {
				marks = append(marks, "no-manifest")
			} else if worktree.Provenance != "" {
				marks = append(marks, worktree.Provenance)
			}
			if worktree.Dirty {
				marks = append(marks, "dirty")
			}
			if worktree.Missing {
				marks = append(marks, "missing")
			}
			// Owner state is the difference between proof and a guess, so it
			// belongs on the row rather than only in the evidence lines.
			switch worktree.OwnerState {
			case worktrees.OwnerLive:
				marks = append(marks, "owner live")
			case worktrees.OwnerGone:
				marks = append(marks, "owner gone")
			default:
				marks = append(marks, "owner unstated")
			}
			if _, err := fmt.Fprintf(out, "  %-8s %s %s (%s)\n",
				worktree.Disposition, worktree.Repository, worktree.Branch, strings.Join(marks, ", ")); err != nil {
				return err
			}
			for _, evidence := range worktree.Evidence {
				if _, err := fmt.Fprintf(out, "           - %s\n", evidence); err != nil {
					return err
				}
			}
		}
	}
	// Residue is not a family: nothing registers it, so it has no branch, no
	// effort, and no disposition to group by. Print it as its own section so a
	// sweep that finds one cannot be read as a clean sweep.
	if len(report.Residue) > 0 && (only == "" || only == worktrees.DispositionReview) {
		if _, err := fmt.Fprintf(out, "\nunregistered checkouts (%d)\n", len(report.Residue)); err != nil {
			return err
		}
		for _, residue := range report.Residue {
			if _, err := fmt.Fprintf(out, "  %s %s (%s)\n", residue.Task, residue.Repository, residue.Layout); err != nil {
				return err
			}
			for _, evidence := range residue.Evidence {
				if _, err := fmt.Fprintf(out, "           - %s\n", evidence); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(out, "           > %s\n", residue.Remedy); err != nil {
				return err
			}
		}
	}
	totals := report.Totals
	if _, err := fmt.Fprintf(out,
		"\n%d worktrees in %d efforts (%d shown): %d current, %d legacy, %d external; %d without a manifest, %d dirty\n",
		totals.Worktrees, totals.Families, shown,
		totals.ByLayout[worktrees.LayoutCurrent], totals.ByLayout[worktrees.LayoutLegacy],
		totals.ByLayout[worktrees.LayoutExternal], totals.NoManifest, totals.Dirty); err != nil {
		return err
	}
	dispositions := make([]string, 0, len(totals.ByDispositn))
	for disposition := range totals.ByDispositn {
		dispositions = append(dispositions, disposition)
	}
	sort.Strings(dispositions)
	for _, disposition := range dispositions {
		if _, err := fmt.Fprintf(out, "  %-8s %d\n", disposition, totals.ByDispositn[disposition]); err != nil {
			return err
		}
	}
	for _, unscanned := range report.Unscanned {
		if _, err := fmt.Fprintf(out, "unscanned: %s\n", unscanned); err != nil {
			return err
		}
	}
	return nil
}
