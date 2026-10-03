// Package cmdbranch owns branch command arguments and report presentation.
package cmdbranch

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"time"
)

type Dependencies struct {
	List          func(context.Context, worktrees.BranchListOptions) (worktrees.BranchListOutcome, error)
	Cleanup       func(context.Context, worktrees.BranchCleanupOptions) (worktrees.BranchCleanupOutcome, error)
	Quarantine    func(context.Context, worktrees.BranchQuarantineOptions) (worktrees.BranchQuarantineOutcome, error)
	ArchiveTarget func(context.Context, string) (worktrees.RetiredArchivePlan, error)
}

func New(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "branch",
		Short: "Inventory and safely retire local and remote Git branches across the fleet",
	}
	command.AddCommand(newList(runtime, deps))
	command.AddCommand(newCount(runtime, deps))
	command.AddCommand(newCleanup(runtime, deps))
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
	var repository, branch, sha, reason, manifest, reportDir string
	var apply bool
	command := &cobra.Command{Use: "quarantine", Short: "Plan or locally quarantine exact old branches under retired/*", Args: cobra.NoArgs,
		Long: "Quarantine is dry-run by default. --apply atomically creates retired/<date>-<flat-source>-<short-sha> and compare-and-deletes the exact local source SHA, recording every row in a durable report. Remote quarantine is intentionally refused until WB has equivalent peer evidence and a leased remote rename.",
		RunE: func(command *cobra.Command, args []string) error {
			flags := runtime.Flags()
			outcome, err := deps.Quarantine(command.Context(), worktrees.BranchQuarantineOptions{ProjectsRoot: flags.ProjectsRoot, Repository: repository, Branch: branch, SHA: sha, Reason: reason, Manifest: manifest, Apply: apply, ReportDir: reportDir})
			if err != nil {
				return err
			}
			for _, result := range outcome.Results {
				if result.Error != "" {
					if _, err := fmt.Fprintf(command.OutOrStdout(), "%s %s: %s\n", result.Outcome, result.Ref, result.Error); err != nil {
						return err
					}
				} else {
					if _, err := fmt.Fprintf(command.OutOrStdout(), "%s %s -> %s\n", result.Outcome, result.Ref, result.Destination); err != nil {
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

absorbed is never eligible for --apply on 'wb branch cleanup', under any flag,
in any configuration: patch-id equality proves identical content exists
upstream, not that this branch's work is still present in the target now — a
branch that landed and was later reverted still emits zero unique patches. Its
row names the remedy: 'wb worktree cleanup <task> --absorbed-by <pr-or-commit>'
for a WB-owned branch, 'wb branch cleanup --absorbed-by <pr-or-commit>' for one
with no worktree left, or an explicit human decision otherwise.

A branch owned by a WB task is always in-use, never a candidate here — see
'wb worktree cleanup <task>' or 'wb worktree abort <task>'.

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

func newCleanup(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var base, scope, reportDir, format, absorbedBy, supersededBy, repository, branch string
	var peerEvidence, requireHosts []string
	var apply, receipts bool
	var olderThan time.Duration
	command := &cobra.Command{
		Use:   "cleanup",
		Short: "Plan or delete branches provably contained in the exact origin target",
		Long: `Plan, and only under --apply perform, retirement of branches whose content is
provably contained in the freshly fetched exact origin/<base> target. The
default is a dry-run plan; --apply is required for every deletion in every
scope, and a dry run never creates a report directory or mutates anything.

contained is always eligible. Under --receipts a branch with a proved landing
receipt is eligible too: GitHub's commit-to-pull-request index must name a
merged pull request into the exact base whose merge commit is contained in the
fetched target, and a local three-way merge proof must show the branch adds
nothing to the landing commit or the target. Patch-id equality never
qualifies: absorbed is never eligible, under any flag, in any configuration —
see 'wb branch list --help' for why. unique, protected, in-use, and unreadable
are likewise never eligible (a unique or absorbed branch can only become
eligible by the receipt proof above, which reclassifies it receipted). A
branch owned by a WB task is reported in-use and is never deleted here; use
'wb worktree cleanup <task>' or 'wb worktree abort <task>' instead.

--absorbed-by <pr-or-commit> names an explicit landing pull request or exact
commit and verifies it with the same attested-absorption proof
'wb worktree cleanup --absorbed-by' performs, for the branch this command's
own worktree analogue cannot reach because the worktree is already gone. Every
proof --receipts requires still applies (containment in the landing commit,
containment in the fetched target, a mutation-free three-way merge for both),
plus one more: the named commit must be exactly where the work entered the
target, so the flag cannot degrade into a bare content assertion by naming the
target tip. A branch that verifies is recorded receipted, exactly like a
discovered receipt, never absorbed — absorbed itself still never becomes
eligible, under any flag. A pointer that fails any check refuses only that
branch, with the failing check reported as its evidence, and never aborts the
sweep.

A local branch is deleted with a compare-and-delete against the exact SHA the
plan recorded: 'git update-ref -d refs/heads/<branch> <expected-sha>', never
'git branch -d'/'-D', whose own merge test is against HEAD rather than the
fetched target. A remote branch is deleted with force-with-lease against its
observed SHA. Immediately before each deletion WB refetches the exact target,
re-resolves the branch, and re-verifies containment; a branch that moved
between plan and apply refuses only itself, with the moved SHA reported, and
never aborts the run.

--superseded-by <receipt.json> retires one deliberately split branch only with
exact --repo and --branch selectors. Every scope containing remote deletion
also requires fresh --peer-evidence for every --require-host, rechecks that
evidence immediately before the leased push, refuses fork origins whose
upstream PR state cannot be proven, and archives a SHA-bound source bundle
outside the source clone before deletion.

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
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			progress := command.ErrOrStderr()
			flags := runtime.Flags()
			outcome, err := deps.Cleanup(command.Context(), worktrees.BranchCleanupOptions{
				Receipts:     receipts,
				AbsorbedBy:   absorbedBy,
				SupersededBy: supersededBy,
				ProjectsRoot: flags.ProjectsRoot, Base: base, Scope: scope, Apply: apply,
				OlderThan: olderThan, ReportDir: reportDir, Filter: flags.Filter, Progress: progress,
				Repository: repository, Branch: branch,
				PeerEvidence: peerEvidence, RequireHosts: requireHosts,
			})
			if err != nil {
				return err
			}
			switch format {
			case "text":
				if err := printBranchCleanup(command.OutOrStdout(), outcome); err != nil {
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
			}
			return nil
		},
	}
	command.Flags().StringVar(&base, "base", "main", "exact origin target branch every candidate must be contained in")
	command.Flags().StringVar(&scope, "scope", "local", "local, remote, or all")
	command.Flags().BoolVar(&apply, "apply", false, "delete every eligible branch; the default is a dry-run plan")
	command.Flags().BoolVar(&receipts, "receipts", false, "prove landings via GitHub pull-request receipts, making receipted branches eligible (one query per candidate)")
	command.Flags().StringVar(&absorbedBy, "absorbed-by", "", "verify this merged pull request number or exact landing commit absorbed a branch's content, making a content-proven squash-absorbed branch eligible even with no worktree left (same proof as 'wb worktree cleanup --absorbed-by')")
	command.Flags().StringVar(&supersededBy, "superseded-by", "", "trusted-reviewer receipt for one exact intentionally superseded branch; requires --repo, --branch, and peer evidence for remote scope")
	command.Flags().StringVar(&repository, "repo", "", "exact owner/repository selector")
	command.Flags().StringVar(&branch, "branch", "", "exact branch ref selector")
	command.Flags().StringSliceVar(&peerEvidence, "peer-evidence", nil, "machine-generated exact branch-list JSON from one required host; repeat per host")
	command.Flags().StringSliceVar(&requireHosts, "require-host", nil, "host ID required in peer evidence; repeat per host")
	command.Flags().DurationVar(&olderThan, "older-than", 24*time.Hour, "minimum branch age required for eligibility (0 disables)")
	command.Flags().StringVar(&reportDir, "report-dir", "", "branch cleanup audit directory (default <wb-home>/reports/branch-cleanup/<timestamp>)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
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
