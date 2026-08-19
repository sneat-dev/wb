package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// newBranchCmd is the top-level `wb branch` family. It is a sibling of
// `wb worktree`, not nested under it: its subject is any local or remote
// branch, most of which have no linked worktree and no WB Work Log claim, so
// none of worktree cleanup's task/coordination machinery applies. See
// spec/features/branch-hygiene/README.md.
func newBranchCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "branch",
		Short: "Inventory and safely retire local and remote Git branches across the fleet",
	}
	command.AddCommand(newBranchListCmd())
	command.AddCommand(newBranchCleanupCmd())
	return command
}

func newBranchListCmd() *cobra.Command {
	var base, scope, only, format string
	var olderThan time.Duration
	command := &cobra.Command{
		Use:   "list",
		Short: "Explain every branch's disposition and evidence, read-only",
		Long: `List every local and/or remote branch below --projects-root with the exact
evidence behind its disposition.

Every disposition is computed against the exact commit SHA WB fetches from
origin/<base> during this run, never a stale local branch or tracking ref. A
repository whose target cannot be fetched yields the unreadable disposition
for its branches without blocking the rest of the sweep.

Disposition is one of a closed set:
  contained   ancestor of the fetched exact target; the only one eligible for deletion
  absorbed    patch-id or tree equal to the target, but not an ancestor — report-only, forever
  unique      git cherry proves it has content that is not upstream
  protected   is --base, the canonical clone's current HEAD, or a protected name
  in-use      checked out in a linked worktree, or named by a live WB Work Log claim
  unreadable  required evidence could not be obtained

absorbed is never eligible for --apply on 'wb branch cleanup', under any flag,
in any configuration: patch-id equality proves identical content exists
upstream, not that this branch's work is still present in the target now — a
branch that landed and was later reverted still emits zero unique patches. Its
row names the remedy: 'wb worktree cleanup <task> --absorbed-by <pr-or-commit>'
for a WB-owned branch, or an explicit human decision otherwise.

A branch owned by a WB task is always in-use, never a candidate here — see
'wb worktree cleanup <task>' or 'wb worktree abort <task>'.

This command is read-only in every configuration: its only permitted remote
interaction is fetching. Progress streams to stderr as '[n/N] repository',
flushed per event, so a long fleet sweep never looks hung; stdout stays
reserved for the report.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			progress := command.ErrOrStderr()
			outcome, err := worktrees.BranchList(command.Context(), worktrees.BranchListOptions{
				ProjectsRoot: projectsRoot, Base: base, Scope: scope, Only: only,
				OlderThan: olderThan, Filter: filterFlag, Progress: progress,
			})
			if err != nil {
				return err
			}
			switch format {
			case "text":
				return printBranchList(command, outcome)
			case "json":
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(outcome)
			default:
				return fmt.Errorf("unsupported format %q; use text or json", format)
			}
		},
	}
	command.Flags().StringVar(&base, "base", "main", "exact origin target branch every disposition is computed against")
	command.Flags().StringVar(&scope, "scope", "local", "local, remote, or all")
	command.Flags().StringVar(&only, "only", "", "show only this disposition: contained, absorbed, unique, protected, in-use, or unreadable")
	command.Flags().DurationVar(&olderThan, "older-than", 0, "show only branches at least this old (0 shows every age)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func newBranchCleanupCmd() *cobra.Command {
	var base, scope, reportDir, format string
	var apply bool
	var olderThan time.Duration
	command := &cobra.Command{
		Use:   "cleanup",
		Short: "Plan or delete branches provably contained in the exact origin target",
		Long: `Plan, and only under --apply perform, retirement of branches whose content is
provably contained in the freshly fetched exact origin/<base> target. The
default is a dry-run plan; --apply is required for every deletion in every
scope, and a dry run never creates a report directory or mutates anything.

Only the contained disposition is ever eligible. absorbed is never eligible,
under any flag, in any configuration — see 'wb branch list --help' for why.
unique, protected, in-use, and unreadable are likewise never eligible. A
branch owned by a WB task is reported in-use and is never deleted here; use
'wb worktree cleanup <task>' or 'wb worktree abort <task>' instead.

A local branch is deleted with a compare-and-delete against the exact SHA the
plan recorded: 'git update-ref -d refs/heads/<branch> <expected-sha>', never
'git branch -d'/'-D', whose own merge test is against HEAD rather than the
fetched target. A remote branch is deleted with force-with-lease against its
observed SHA. Immediately before each deletion WB refetches the exact target,
re-resolves the branch, and re-verifies containment; a branch that moved
between plan and apply refuses only itself, with the moved SHA reported, and
never aborts the run.

--scope remote (and the remote half of --scope all) additionally requires
pull-request evidence: a branch that is the head of an open pull request is
refused regardless of containment, and when pull-request evidence cannot be
obtained at all, no remote branch is deleted in this run — reported, not
silently skipped. Local deletion is unaffected, because deleting a local ref
cannot alter a pull request.

--older-than is measured from each branch's committer date; the default 24h
grace window can be disabled with --older-than 0. An --apply attempt writes a
durable machine-readable plan below --report-dir before its first destructive
Git operation, and updates that same report with each candidate's outcome.
'wb branch cleanup' never removes, moves, or modifies any working tree,
worktree registration, index, or stash: a branch checked out anywhere is
in-use and therefore never a candidate.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			progress := command.ErrOrStderr()
			now := time.Now()
			outcome, err := worktrees.BranchCleanup(command.Context(), worktrees.BranchCleanupOptions{
				ProjectsRoot: projectsRoot, Base: base, Scope: scope, Apply: apply,
				OlderThan: olderThan, ReportDir: reportDir, Filter: filterFlag, Progress: progress,
				Now: func() time.Time { return now },
			})
			if err != nil {
				return err
			}
			switch format {
			case "text":
				if err := printBranchCleanup(command, outcome); err != nil {
					return err
				}
				if outcome.ReportPath != "" {
					if _, err := fmt.Fprintf(command.OutOrStdout(), "report: %s\n", outcome.ReportPath); err != nil {
						return err
					}
				}
			case "json":
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(outcome); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported format %q; use text or json", format)
			}
			return nil
		},
	}
	command.Flags().StringVar(&base, "base", "main", "exact origin target branch every candidate must be contained in")
	command.Flags().StringVar(&scope, "scope", "local", "local, remote, or all")
	command.Flags().BoolVar(&apply, "apply", false, "delete every eligible branch; the default is a dry-run plan")
	command.Flags().DurationVar(&olderThan, "older-than", 24*time.Hour, "minimum branch age required for eligibility (0 disables)")
	command.Flags().StringVar(&reportDir, "report-dir", "", "branch cleanup audit directory (default <wb-home>/reports/branch-cleanup/<timestamp>)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}

func printBranchList(command *cobra.Command, outcome worktrees.BranchListOutcome) error {
	out := command.OutOrStdout()
	if len(outcome.Entries) == 0 {
		_, err := fmt.Fprintln(out, "no branches matched")
		return err
	}
	currentRepository := ""
	for _, entry := range outcome.Entries {
		if entry.Repository != currentRepository {
			currentRepository = entry.Repository
			if _, err := fmt.Fprintf(out, "\n%s\n", currentRepository); err != nil {
				return err
			}
		}
		age := "-"
		if !entry.CommitterDate.IsZero() {
			age = time.Since(entry.CommitterDate).Round(time.Hour).String() + " old"
		}
		if _, err := fmt.Fprintf(out, "  %-8s %-40s %-12s %-11s %-10s %s\n",
			entry.Scope, entry.Branch, entry.ShortSHA, entry.Disposition, age, entry.Evidence); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	return printDispositionTotals(out, outcome.Totals)
}

func printDispositionTotals(out io.Writer, totals map[string]int) error {
	names := make([]string, 0, len(totals))
	for name := range totals {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := fmt.Fprintf(out, "%-11s %d\n", name, totals[name]); err != nil {
			return err
		}
	}
	return nil
}

func printBranchCleanup(command *cobra.Command, outcome worktrees.BranchCleanupOutcome) error {
	out := command.OutOrStdout()
	if len(outcome.Results) == 0 {
		_, err := fmt.Fprintln(out, "no branches matched")
		return err
	}
	currentRepository := ""
	deleted, eligible := 0, 0
	for _, result := range outcome.Results {
		if result.Repository != currentRepository {
			currentRepository = result.Repository
			if _, err := fmt.Fprintf(out, "\n%s\n", currentRepository); err != nil {
				return err
			}
		}
		switch {
		case result.Applied:
			deleted++
			if _, err := fmt.Fprintf(out, "  deleted      %s %s (%s)\n", result.Scope, result.Branch, result.ShortSHA); err != nil {
				return err
			}
		case result.Eligible:
			eligible++
			if _, err := fmt.Fprintf(out, "  would delete %s %s (%s)\n", result.Scope, result.Branch, result.ShortSHA); err != nil {
				return err
			}
		case result.Error != "":
			if _, err := fmt.Fprintf(out, "  failed       %s %s: %s\n", result.Scope, result.Branch, result.Error); err != nil {
				return err
			}
		default:
			if _, err := fmt.Fprintf(out, "  skip         %s %s (%s): %s\n", result.Scope, result.Branch, result.Disposition, result.SkipReason); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	if outcome.Apply {
		_, err := fmt.Fprintf(out, "%d deleted\n", deleted)
		return err
	}
	_, err := fmt.Fprintf(out, "%d eligible; dry-run only, pass --apply to delete\n", eligible)
	return err
}
