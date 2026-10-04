package cmdworktree

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/worktrees"
	"io"
)

func writeRename(out io.Writer, results []worktrees.RenameResult, apply bool) error {
	if len(results) == 0 {
		_, err := fmt.Fprintln(out, "no WB worktrees matched")
		return err
	}
	eligible, renamed := 0, 0
	for _, result := range results {
		switch {
		case result.Applied:
			renamed++
			note := ""
			if result.OldBranchDeleted {
				note = " and deleted old branch " + result.OldBranch
			}
			if _, err := fmt.Fprintf(out, "renamed %s %s -> %s (%s)%s\n", result.OldTask, result.Repository, result.NewWorktreeDir, result.NewBranch, note); err != nil {
				return err
			}
		case result.Eligible:
			eligible++
			if _, err := fmt.Fprintf(out, "would rename %s %s -> %s\n", result.OldTask, result.Repository, result.NewWorktreeDir); err != nil {
				return err
			}
		default:
			if _, err := fmt.Fprintf(out, "skip %s %s: %s\n", result.OldTask, result.Repository, result.Reason); err != nil {
				return err
			}
		}
	}
	if apply {
		_, err := fmt.Fprintf(out, "%d renamed\n", renamed)
		return err
	}
	_, err := fmt.Fprintf(out, "%d eligible; dry-run only, pass --apply to rename\n", eligible)
	return err
}
func writeRelocation(out io.Writer, results []worktrees.RelocateResult, apply bool) error {
	eligible, moved := 0, 0
	for _, result := range results {
		switch {
		case result.Applied:
			moved++
			if _, err := fmt.Fprintf(out, "relocated %s %s -> %s\n", result.Task, result.Repository, result.Destination); err != nil {
				return err
			}
		case result.AlreadyThere:
			if _, err := fmt.Fprintf(out, "already there %s %s %s\n", result.Task, result.Repository, result.Destination); err != nil {
				return err
			}
		case result.Eligible:
			eligible++
			if _, err := fmt.Fprintf(out, "would relocate %s %s -> %s\n", result.Task, result.Repository, result.Destination); err != nil {
				return err
			}
		default:
			if _, err := fmt.Fprintf(out, "skip %s %s: %s\n", result.Task, result.Repository, result.Reason); err != nil {
				return err
			}
		}
	}
	if apply {
		if moved == 0 && eligible > 0 {
			return fmt.Errorf("no planned worktree was relocated")
		}
		_, err := fmt.Fprintf(out, "%d relocated\n", moved)
		return err
	}
	_, err := fmt.Fprintf(out, "%d eligible; dry-run only, pass --apply to relocate\n", eligible)
	return err
}
