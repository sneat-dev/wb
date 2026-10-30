package cmdworktree

import (
	"fmt"
	"io"
	"sort"

	"github.com/sneat-dev/wb/internal/worktrees"
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
