// Package cmdbranch owns branch command arguments and report presentation.
package cmdbranch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

type Dependencies struct {
	List          func(context.Context, worktrees.BranchListOptions) (worktrees.BranchListOutcome, error)
	Quarantine    func(context.Context, worktrees.BranchQuarantineOptions) (worktrees.BranchQuarantineOutcome, error)
	ArchiveTarget func(context.Context, string) (worktrees.RetiredArchivePlan, error)
}

func New(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "branch",
		Short: "Inventory branches across the fleet; wb clean retires the finished ones",
	}
	command.AddCommand(newList(runtime, deps))
	command.AddCommand(newCount(runtime, deps))
	command.AddCommand(newQuarantine(runtime, deps))
	command.AddCommand(newArchiveTarget(runtime, deps))
	return command
}

func newCount(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var inventory inventoryFlags
	command := &cobra.Command{Use: "count", Short: "Count active branches plus retired branch and tag refs in locally discovered clones", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(inventory.format, "text", "json", "yaml"); err != nil {
				return err
			}
			flags := runtime.Flags()
			outcome, err := deps.List(command.Context(), worktrees.BranchListOptions{ProjectsRoot: flags.ProjectsRoot, Base: inventory.base, Scope: inventory.scope, Only: inventory.only, OlderThan: inventory.olderThan, Filter: flags.Filter, Repository: inventory.repository, Org: inventory.org, Name: inventory.name, Progress: command.ErrOrStderr()})
			if err != nil {
				return err
			}
			switch inventory.format {
			case "text":
				if err := printBranchCount(command.OutOrStdout(), outcome); err != nil {
					return err
				}
				return printBranchDiagnostics(command.ErrOrStderr(), outcome.Diagnostics)
			case "json":
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				err = encoder.Encode(outcome)
			case "yaml":
				raw, err := yamlCompatibleBranchList(outcome)
				if err != nil {
					return err
				}
				_, err = command.OutOrStdout().Write(raw)
				return err
			}
			return err
		},
	}
	bindInventoryFlags(command, &inventory, "count only this disposition, including retired", "count only branches at least this old")
	return command
}

func newQuarantine(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var repository, branch, sha, reason, description, retention, manifest, reportDir string
	var replacement []string
	var apply, annotate bool
	command := &cobra.Command{Use: "quarantine", Short: "Plan or locally quarantine exact old branches under retired/*", Args: cobra.NoArgs,
		Long: "Quarantine is dry-run by default. --apply creates a tree-identical metadata commit parented by the original tip, then atomically creates the retired ref and compare-and-deletes the exact source SHA. --annotate updates one explicitly named local retired/* ref after checking --sha. Both operations record durable receipts. Remote quarantine is intentionally refused until WB has equivalent peer evidence and a leased remote rename.",
		RunE: func(command *cobra.Command, args []string) error {
			flags := runtime.Flags()
			outcome, err := deps.Quarantine(command.Context(), worktrees.BranchQuarantineOptions{ProjectsRoot: flags.ProjectsRoot, Repository: repository, Branch: branch, SHA: sha, Reason: reason, Description: description, Replacement: replacement, Retention: retention, Annotate: annotate, Manifest: manifest, Apply: apply, ReportDir: reportDir})
			if err != nil {
				return err
			}
			for _, result := range outcome.Results {
				if result.Error != "" {
					if _, err := fmt.Fprintf(command.OutOrStdout(), "%s %s: %s\n", result.Outcome, result.Ref, result.Error); err != nil {
						return err
					}
				} else {
					destination := result.Destination
					if destination == "" {
						destination = result.RetiredSHA
					}
					if _, err := fmt.Fprintf(command.OutOrStdout(), "%s %s -> %s\n", result.Outcome, result.Ref, destination); err != nil {
						return err
					}
				}
			}
			if outcome.ReportPath != "" {
				_, err = fmt.Fprintf(command.OutOrStdout(), "report: %s\n", outcome.ReportPath)
			}
			return err
		},
	}
	command.Flags().StringVar(&repository, "repo", "", "exact owner/repository source")
	command.Flags().StringVar(&branch, "branch", "", "exact local source ref")
	command.Flags().StringVar(&sha, "sha", "", "expected source SHA (required by manifest rows)")
	command.Flags().StringVar(&reason, "reason", "", "durable operator reason")
	command.Flags().StringVar(&description, "description", "", "what the retired work preserved")
	command.Flags().StringArrayVar(&replacement, "replacement", nil, "replacement PR or commit reference (repeatable)")
	command.Flags().StringVar(&retention, "retention", "", "explicit retention decision; omission records unspecified (WB does not auto-expire retired refs)")
	command.Flags().BoolVar(&annotate, "annotate", false, "add a metadata commit to an existing local retired/* ref; requires exact --sha")
	command.Flags().StringVar(&manifest, "manifest", "", "JSON manifest containing exact repository, ref, sha, and reason rows")
	command.Flags().BoolVar(&apply, "apply", false, "perform planned local CAS renames")
	command.Flags().StringVar(&reportDir, "report-dir", "", "durable quarantine report directory")
	return command
}

func newList(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var inventory inventoryFlags
	var branch string
	var includeRetired, withPRs bool
	command := &cobra.Command{
		Use:   "list",
		Short: "Explain every branch's disposition and evidence, read-only",
		Long: `List every local and/or remote branch below --projects-root with the exact
evidence behind its disposition.

Every disposition is computed for locally discovered canonical clones against the exact commit SHA WB fetches from
origin/<base> during this run, never a stale local branch or tracking ref. A
repository whose target cannot be fetched yields the unreadable disposition
for its branches without blocking the rest of the sweep.

--only retired, an exact retired --branch, or a retired/* --name selector
uses a bounded quarantine inventory instead: local scope reads only local
retired refs and does not fetch origin/<base>; remote scope refreshes only
origin's retired namespace before reading its tracking refs. The report names
the skipped base fetch, and a failed remote namespace refresh sets
retired_remote_unavailable with its diagnostic.

The default --scope local inventories local refs. Use --scope remote or --scope
all to include known origin refs; --org narrows only locally discovered
canonical clones and never queries every repository on GitHub.

--with-prs enriches selected remote branch rows with exact same-repository
head PR history (open, merged, closed) and open PRs using the branch as base.
It reads GitHub's paginated PR API and may take longer on a large inventory.
Protected, unreadable, and retired rows are excluded from PR enrichment;
in-use branches are included so their open PRs remain visible.
Without it, list makes no PR API calls. Cleanup independently checks open PRs
for each remote deletion candidate and again before applying deletion.

Disposition is one of a closed set:
  contained   ancestor of the fetched exact target; the only one eligible for deletion
  absorbed    patch-id or tree equal to the target, but not an ancestor — report-only, forever
  unique      git cherry proves it has content that is not upstream
  protected   is --base, the canonical clone's current HEAD, or a protected name
  in-use      checked out in a linked worktree, or named by a live WB Work Log claim
  unreadable  required evidence could not be obtained

absorbed is never eligible for --apply on 'wb clean', under any flag,
in any configuration: patch-id equality proves identical content exists
upstream, not that this branch's work is still present in the target now — a
branch that landed and was later reverted still emits zero unique patches. Its
row names the remedy: 'wb worktree retire <task> --absorbed-by <pr-or-commit>'
for a WB-owned branch, 'wb worktree retire <branch> --absorbed-by <pr-or-commit>' for one
with no worktree left, or an explicit human decision otherwise.

A branch owned by a WB task is always in-use, never a candidate here — see
'wb worktree retire <task>' or 'wb worktree abort <task>'.

This command is read-only in every configuration: it fetches Git refs and,
with --with-prs, reads GitHub PR metadata. Progress streams to stderr as '[n/N] repository',
flushed per event, so a long fleet sweep never looks hung; stdout stays
reserved for the report.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(inventory.format, "text", "json", "yaml"); err != nil {
				return err
			}
			progress := command.ErrOrStderr()
			flags := runtime.Flags()
			outcome, err := deps.List(command.Context(), worktrees.BranchListOptions{
				ProjectsRoot: flags.ProjectsRoot, Base: inventory.base, Scope: inventory.scope, Only: inventory.only, Org: inventory.org,
				OlderThan: inventory.olderThan, Filter: flags.Filter, Progress: progress,
				Repository: inventory.repository, Branch: branch, Name: inventory.name,
				IncludeRetired: includeRetired, WithPRs: withPRs,
			})
			if err != nil {
				return err
			}
			switch inventory.format {
			case "text":
				if err := printBranchList(command.OutOrStdout(), outcome); err != nil {
					return err
				}
				return printBranchDiagnostics(command.ErrOrStderr(), outcome.Diagnostics)
			case "json":
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				err = encoder.Encode(outcome)
			case "yaml":
				raw, err := yamlCompatibleBranchList(outcome)
				if err != nil {
					return err
				}
				_, err = command.OutOrStdout().Write(raw)
				return err
			}
			return err
		},
	}
	command.Flags().StringVar(&branch, "branch", "", "exact branch ref selector")
	command.Flags().BoolVar(&includeRetired, "include-retired", false, "include retired/* quarantine branches (excluded by default)")
	command.Flags().BoolVar(&withPRs, "with-prs", false, "read GitHub PR history for selected remote branches (head all states; base open only)")
	bindInventoryFlags(command, &inventory, "show only this disposition: contained, absorbed, retired, unique, protected, in-use, or unreadable", "show only branches at least this old (0 shows every age)")
	return command
}

// List and count share their inventory selectors, with fresh storage per command.
type inventoryFlags struct {
	base, scope, only, format, repository, org, name string
	olderThan                                        time.Duration
}

func bindInventoryFlags(command *cobra.Command, inventory *inventoryFlags, onlyHelp, ageHelp string) {
	command.Flags().StringVar(&inventory.base, "base", "main", "exact origin target branch every disposition is computed against")
	command.Flags().StringVar(&inventory.scope, "scope", "local", "local, remote, or all")
	command.Flags().StringVar(&inventory.only, "only", "", onlyHelp)
	command.Flags().DurationVar(&inventory.olderThan, "older-than", 0, ageHelp)
	command.Flags().StringVar(&inventory.format, "format", "text", "stdout format: text, json, or yaml")
	command.Flags().StringVar(&inventory.repository, "repo", "", "exact owner/repository selector")
	command.Flags().StringVar(&inventory.org, "org", "", "exact repository owner selector")
	command.Flags().StringVar(&inventory.name, "name", "", "branch-name glob, for example 'retired/*'")
}
