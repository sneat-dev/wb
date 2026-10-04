package cmdworktree

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/canonicalrescue"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/spf13/cobra"
	"sort"
	"strings"
)

type RescueOperations struct {
	Inspect   func(context.Context, string, canonicalrescue.Options) (canonicalrescue.Report, error)
	Capture   func(context.Context, canonicalrescue.Report) (canonicalrescue.Report, error)
	Push      func(context.Context, canonicalrescue.Report, string) (canonicalrescue.Report, error)
	Restore   func(context.Context, canonicalrescue.Report, bool) (canonicalrescue.Report, error)
	ScanLocal func(string) ([]discover.Repo, error)
}

func NewRescue(runtime shared.Runtime, ops RescueOperations) *cobra.Command {
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

--push traverses managed pre-push hooks through a rescue-only attestation. The
hook accepts only the exact single rescue ref whose commit is parented on the
canonical HEAD and whose tree equals a fresh complete capture; it never grants
a general hook bypass. WB then rereads the exact remote ref before --restore.

--fleet reports every dirty canonical clone under --projects-root. It never
applies anything.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			if fleet {
				if len(args) == 1 {
					return fmt.Errorf("--fleet reports every canonical clone; do not also name one")
				}
				if apply || restore || push {
					return fmt.Errorf("--fleet only reports; run rescue against one clone to apply anything")
				}
				return runFleetRescueReport(runtime, ops, cmd, format)
			}
			if restore && !apply {
				return fmt.Errorf("--restore requires --apply: the content must be captured before the clone is cleaned")
			}
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			options := canonicalrescue.Options{ProjectsRoot: runtime.Flags().ProjectsRoot, Branch: branch}
			report, err := ops.Inspect(cmd.Context(), path, options)
			if err != nil {
				return err
			}
			if apply && report.Dirty() {
				if report, err = ops.Capture(cmd.Context(), report); err != nil {
					return err
				}
				if push {
					if report, err = ops.Push(cmd.Context(), report, remote); err != nil {
						return err
					}
				}
				if restore {
					if report, err = ops.Restore(cmd.Context(), report, allowUnpushed); err != nil {
						return err
					}
				}
			}
			return renderRescueReport(runtime, cmd, format, apply, report)
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
func runFleetRescueReport(runtime shared.Runtime, ops RescueOperations, cmd *cobra.Command, format string) error {
	flags := runtime.Flags()
	repositories, err := ops.ScanLocal(flags.ProjectsRoot)
	if err != nil {
		return fmt.Errorf("scan local repositories: %w", err)
	}
	options := canonicalrescue.Options{ProjectsRoot: flags.ProjectsRoot}
	var dirty []canonicalrescue.Report
	for _, repository := range repositories {
		if flags.Filter != "" && !strings.Contains(repository.Slug(), flags.Filter) {
			continue
		}
		// The clone's real path, as discovery found it: a host-level clone
		// lives under <root>/<host>/<org>/<repo>, not the flat legacy shape.
		path := repository.Path
		if path == "" {
			continue
		}
		report, err := ops.Inspect(cmd.Context(), path, options)
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
		// The findings exit code stays a property of the text renderer: this
		// path encodes and exits 0 (see TestCwWtWorktreeRescueFleetAndArgs).
		return encoder.Encode(dirty)
	}
	out := cmd.OutOrStdout()
	if len(dirty) == 0 {
		_, err := fmt.Fprintln(out, "every canonical clone under", flags.ProjectsRoot, "is clean")
		return err
	}
	for _, report := range dirty {
		if _, err := fmt.Fprintf(out, "✗ %s: %d change(s), %d untracked\n", report.Path, len(report.Changes), report.UntrackedCount); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "    wb worktree rescue %s --apply --push\n", report.Path); err != nil {
			return err
		}
	}
	return runtime.ExitError(shared.ExitFindings, fmt.Sprintf("%d canonical clone(s) hold uncommitted work", len(dirty)))
}
