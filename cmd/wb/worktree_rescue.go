package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/canonicalrescue"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/spf13/cobra"
)

func newWorktreeRescueCmd() *cobra.Command {
	var apply, restore, push, allowUnpushed, fleet bool
	var branch, remote, format string
	command := &cobra.Command{
		Use:   "rescue [canonical-clone-path]",
		Short: "Move uncommitted work out of a canonical clone onto a branch",
		Long: `Preserve work found in a canonical clone, without discarding any of it.

Reporting is the default. Nothing is written, moved, or removed until --apply,
and even then the clone is left exactly as it was found: --apply creates a
branch holding the content and stops there. Returning the clone to a clean
checkout is a second, separate decision behind --restore.

That separation is the point. A canonical clone has held a finished, unlanded
document that existed nowhere else; a rescue that preserved and discarded in
one step would be one bug away from being the loss it was meant to prevent.

The capture never disturbs the clone. WB copies the clone's index to a scratch
file, stages the working tree into the copy, writes a tree from it, and commits
that tree with 'git commit-tree' parented on HEAD — so the branch holds every
modified, staged, and untracked path while the clone's HEAD, branch, index, and
working tree are unchanged. 'git stash' is deliberately not used: its stack is
shared with every linked worktree, and 'git stash create' does not capture
untracked files, which is exactly the content most at risk.

--restore refuses unless the content is provably elsewhere: a rescue commit
must exist, every path the report named must be verifiably inside it, and the
branch must be on the remote unless --allow-unpushed accepts that risk. The
clean it then runs omits -x, so ignored paths — including WB's own generated
` + ".worktree.md" + ` — survive.

--fleet reports every dirty canonical clone under --projects-root. It never
applies anything.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			if fleet {
				if len(args) == 1 {
					return fmt.Errorf("--fleet reports every canonical clone; do not also name one")
				}
				if apply || restore || push {
					return fmt.Errorf("--fleet only reports; run rescue against one clone to apply anything")
				}
				return runFleetRescueReport(cmd, format)
			}
			if restore && !apply {
				return fmt.Errorf("--restore requires --apply: the content must be captured before the clone is cleaned")
			}
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			options := canonicalrescue.Options{ProjectsRoot: projectsRoot, Branch: branch}
			report, err := canonicalrescue.Inspect(cmd.Context(), path, options)
			if err != nil {
				return err
			}
			if apply && report.Dirty() {
				if report, err = canonicalrescue.Capture(cmd.Context(), report); err != nil {
					return err
				}
				if push {
					if report, err = canonicalrescue.Push(cmd.Context(), report, remote); err != nil {
						return err
					}
				}
				if restore {
					if report, err = canonicalrescue.Restore(cmd.Context(), report, allowUnpushed); err != nil {
						return err
					}
				}
			}
			return renderRescueReport(cmd, format, apply, report)
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "capture the clone's uncommitted content onto a branch")
	command.Flags().BoolVar(&push, "push", false, "publish the rescue branch so it does not live only on this machine")
	command.Flags().BoolVar(&restore, "restore", false, "after capturing, return the clone to a clean checkout of its HEAD")
	command.Flags().BoolVar(&allowUnpushed, "allow-unpushed", false, "allow --restore against a rescue branch that exists only locally")
	command.Flags().BoolVar(&fleet, "fleet", false, "report every dirty canonical clone under --projects-root")
	command.Flags().StringVar(&branch, "branch", "", "rescue branch name (default rescue/canonical-<timestamp>)")
	command.Flags().StringVar(&remote, "remote", "origin", "remote --push publishes to")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

// runFleetRescueReport finds every canonical clone holding uncommitted work.
//
// It is the detection half of the brief: the guard stops most writes, and this
// finds whatever still got through — including everything already sitting in a
// clone before the guard existed.
func runFleetRescueReport(cmd *cobra.Command, format string) error {
	repositories, err := discover.ScanLocal(projectsRoot)
	if err != nil {
		return fmt.Errorf("scan local repositories: %w", err)
	}
	options := canonicalrescue.Options{ProjectsRoot: projectsRoot}
	var dirty []canonicalrescue.Report
	for _, repository := range repositories {
		if filterFlag != "" && !strings.Contains(repository.Slug(), filterFlag) {
			continue
		}
		path := filepath.Join(projectsRoot, repository.Org, repository.Name)
		report, err := canonicalrescue.Inspect(cmd.Context(), path, options)
		if err != nil {
			// A clone WB cannot read is reported by wb fleet status, not here.
			// Skipping it keeps one broken clone from hiding the dirty ones.
			continue
		}
		if report.Dirty() {
			dirty = append(dirty, report)
		}
	}
	sort.Slice(dirty, func(i, j int) bool { return dirty[i].Path < dirty[j].Path })
	if format == "json" {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(dirty)
	}
	out := cmd.OutOrStdout()
	if len(dirty) == 0 {
		return writeLine(out, "every canonical clone under", projectsRoot, "is clean")
	}
	for _, report := range dirty {
		if err := writeFormat(out, "✗ %s: %d change(s), %d untracked\n", report.Path, len(report.Changes), report.UntrackedCount); err != nil {
			return err
		}
		if err := writeFormat(out, "    wb worktree rescue %s --apply --push\n", report.Path); err != nil {
			return err
		}
	}
	return &exitError{code: exitFindings, message: fmt.Sprintf("%d canonical clone(s) hold uncommitted work", len(dirty))}
}

func renderRescueReport(cmd *cobra.Command, format string, apply bool, report canonicalrescue.Report) error {
	if format == "json" {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	out := cmd.OutOrStdout()
	if !report.Dirty() {
		return writeFormat(out, "%s is clean\n", report.Path)
	}
	if err := writeFormat(out, "%s holds %d uncommitted change(s), %d untracked:\n",
		report.Path, len(report.Changes), report.UntrackedCount); err != nil {
		return err
	}
	for index, change := range report.Changes {
		if index == 20 {
			if err := writeFormat(out, "  … and %d more\n", len(report.Changes)-index); err != nil {
				return err
			}
			break
		}
		if err := writeFormat(out, "  %s %s\n", change.Status, change.Path); err != nil {
			return err
		}
	}
	if !apply {
		if err := writeFormat(out, "\nNothing has been changed. To preserve this onto a branch:\n  wb worktree rescue %s --apply --push\n", report.Path); err != nil {
			return err
		}
		return &exitError{code: exitFindings, message: fmt.Sprintf("%s holds uncommitted work", report.Path)}
	}
	if err := writeFormat(out, "\ncaptured onto %s (%s)\n", report.RescueBranch, report.RescueCommit); err != nil {
		return err
	}
	if report.Pushed {
		if err := writeLine(out, "pushed to the remote"); err != nil {
			return err
		}
	}
	if report.Restored {
		return writeFormat(out, "%s is now clean\n", report.Path)
	}
	return writeFormat(out,
		"%s is still dirty on purpose. Review the branch, then clean the clone:\n  wb worktree rescue %s --apply --branch %s --restore\n\nThat second run recognises the branch it already created and reuses it, so\nnothing is captured twice.\n",
		report.Path, report.Path, report.RescueBranch)
}
