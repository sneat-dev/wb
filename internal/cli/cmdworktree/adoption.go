package cmdworktree

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"time"
)

// NewBackfill constructs the worktree backfill command.
func NewBackfill(runtime shared.Runtime, operation func(context.Context, worktrees.BackfillOptions) ([]worktrees.BackfillResult, error)) *cobra.Command {
	var base, format string
	var apply bool
	command := &cobra.Command{
		Use:   "backfill",
		Short: "Give existing worktrees a reconstructed manifest so the fleet becomes explicable",
		Long: `Write a reconstructed manifest into every reachable worktree that lacks one.

Adoption never requires stopping agents. This is additive by construction:
.wb/local/ is a new path, nothing moves, and no working tree is touched, so a
worktree holding uncommitted changes is unaffected. Re-running is safe, which
matters because a sweep over hundreds of worktrees will be interrupted.

A reconstructed manifest records which fields were inferred and from what
evidence, so triage never mistakes an inference for a creation record. It never
fabricates a prompt: a worktree whose instructions were never recorded has
none, and 'wb worktree set --prompt' is how it gets its first real one.

The default is a dry run.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			results, err := operation(command.Context(), worktrees.BackfillOptions{
				ProjectsRoot: runtime.Flags().ProjectsRoot, Base: base, Apply: apply,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(results)
			}
			counts := map[string]int{}
			for _, result := range results {
				counts[result.Action]++
				if result.Action == worktrees.BackfillSkipped {
					if _, err := fmt.Fprintf(command.OutOrStdout(), "skipped %s: %s\n", result.Path, result.Reason); err != nil {
						return err
					}
				}
			}
			return writeAdoptionTotals(command.OutOrStdout(), counts, apply)
		},
	}
	command.Flags().StringVar(&base, "base", "main", "remote target used while reconstructing a base ref")
	command.Flags().BoolVar(&apply, "apply", false, "write the reconstructed manifests")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

// NewAdopt constructs the worktree adopt command.
func NewAdopt(runtime shared.Runtime, operation func(context.Context, worktrees.AdoptOptions) ([]worktrees.AdoptResult, error), admit func(*cobra.Command, bool) (func(), error)) *cobra.Command {
	var base, format string
	var allExternal, apply bool
	command := &cobra.Command{
		Use:   "adopt [path]",
		Short: "Bring an external (pre-WB) worktree under WB management",
		Long: `Give wb worktree cleanup and abort a task to act on for a worktree they
currently refuse with "task ... was not found".

wb worktree orphans already discovers and classifies every worktree with
layout "external" — a linked worktree Git knows about that was never created
by wb. wb worktree backfill already gives one a manifest, but that is only
the identity half of adoption: no task directory or Work Log claim exists for
cleanup/abort's own preflight to resolve. Adopt writes that task directory,
manifest, and claim, reusing backfill's reconstruction (see wb worktree
backfill) rather than duplicating it, and it never fabricates a prompt.

It is additive, exactly like backfill: the working tree, its index, and its
checked-out branch are never moved, modified, or otherwise touched. Only a
small registration entry is created under the WB home. A worktree that
already belongs to a WB task — adopted before, or created directly by
wb worktree create — is a no-op, so re-running a sweep after an interruption
is safe.

After adoption, wb worktree cleanup <task> and wb worktree abort <task> apply
their full existing safety checks unchanged: dirty, unlanded, an open pull
request, a held lock, and awaiting_push are all still refused exactly as they
are for a worktree wb created directly.

Pass a worktree path to adopt exactly one, or --all-external to sweep every
external worktree (narrow it with --filter). The default is a dry run. Applying
adoption is a mutation: --mode agent requires a live registered session, while
--mode manual requires --initiator for an explicit audit record.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			path := ""
			if len(args) == 1 {
				path = args[0]
			}
			if (path == "") == !allExternal {
				return fmt.Errorf("supply exactly one of a worktree path or --all-external")
			}
			releaseAdmission, err := admit(command, apply)
			if err != nil {
				return err
			}
			defer releaseAdmission()
			initiator, _ := command.Flags().GetString("initiator")
			flags := runtime.Flags()
			results, err := operation(command.Context(), worktrees.AdoptOptions{
				ProjectsRoot: flags.ProjectsRoot, Base: base, Path: path, Initiator: initiator,
				AllExternal: allExternal, Filter: flags.Filter, Apply: apply,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(results)
			}
			return writeAdoption(command.OutOrStdout(), results, apply)
		},
	}
	command.Flags().StringVar(&base, "base", "main", "remote target used while reconstructing a base ref")
	command.Flags().BoolVar(&allExternal, "all-external", false, "adopt every worktree wb worktree orphans classifies as external")
	command.Flags().BoolVar(&apply, "apply", false, "write the task directory, manifest, and Work Log claim")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

// NewOrphans constructs the worktree orphans command.
func NewOrphans(runtime shared.Runtime, operation func(context.Context, worktrees.OrphanOptions) (worktrees.OrphanReport, error)) *cobra.Command {
	var base, format, only string
	var staleDays int
	command := &cobra.Command{
		Use:   "orphans",
		Short: "Explain every linked worktree and recommend what to do with it",
		Long: `Read-only triage of every linked worktree reachable from the projects root.

Discovery goes through each canonical clone's own Git worktree registry, so it
sees all three layout generations at once: WB's current home, the legacy
<projects-root>/.wb hierarchy, and pre-WB checkouts living anywhere else.

Identity comes from a worktree's own manifest where one exists. Where none
does, WB reconstructs what it can from path, branch, and commit evidence and
labels the row as reconstructed rather than presenting it as a creation record.

Rows group by root effort so a family of sub-agent worktrees is one subject. A
family is recommended for removal only when every worktree in it has landed.
This command never mutates anything.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			report, err := operation(command.Context(), worktrees.OrphanOptions{
				ProjectsRoot: runtime.Flags().ProjectsRoot,
				Base:         base,
				StaleAfter:   time.Duration(staleDays) * 24 * time.Hour,
			})
			if err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(report)
			}
			return writeOrphans(command.OutOrStdout(), report, only)
		},
	}
	command.Flags().StringVar(&base, "base", "main", "remote target a branch must be contained in to count as landed")
	command.Flags().IntVar(&staleDays, "stale-days", 14, "days without a commit before unmerged work needs a decision")
	command.Flags().StringVar(&only, "only", "", "show only families with this disposition: active, remove, review, or decide")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}
