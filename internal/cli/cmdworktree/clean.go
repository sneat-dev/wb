package cmdworktree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

// CleanDependencies are the engines `wb clean` drives.
type CleanDependencies struct {
	Run      func(context.Context, worktrees.CleanOptions) (worktrees.CleanOutcome, error)
	Progress func(*cobra.Command, bool) InventoryProgress
}

// NewClean constructs `wb clean`, the one fleet verb that audits and retires
// finished worktrees and branches.
func NewClean(runtime shared.Runtime, deps CleanDependencies) *cobra.Command {
	var base, format, supersededBy, repository, branch, scope, only string
	var tasks []string
	var apply, allowResidue, skipDetached, skipSizes, verbose bool
	var olderThan, ttl, sessionFreshness time.Duration
	var residueDepth, parallel int
	command := &cobra.Command{
		Use:   "clean",
		Short: "Audit, and with --apply retire, every finished worktree and branch",
		Long: `Audit every WB checkout, every branch that no longer has a checkout, and every
linked worktree WB does not manage; with --apply, retire the ones that are
provably finished. Dry run is the default. Every row names its class, the
evidence behind it, and, when it is kept, the command that resolves it.

Selectors narrow the dry run and --apply identically:

  --filter <s>      owner/name substring, or a glob such as 'datatug/*'
  --org <owner>     exact owner (repeatable)
  --repo <o/n>      one exact repository
  --branch <name>   one exact branch, of a checkout or a bare branch ref
  --task <name>     exact WB task (repeatable); selects checkouts only
  --scope           local (checkouts and local branches, the default),
                    remote (remote branches only) or all (both; a retired
                    checkout's unchanged origin branch is retired with it)
  --only <class>    one class, or "landed" for every class proving a landing
  --older-than      keep work whose pull request merged, or whose branch was
                    last committed, less than this ago (0 disables)

A selection that matches no local repository is an error ("0 repositories
matched ..."), never an empty success.

Checkouts are classified by commit identity, never by branch-name ancestry: a
squash merge produces a new commit, so Git reports every landed branch as
unmerged forever. WB reads GitHub's commit-to-pull-request index for the head
and, when that head was never pushed, for its ancestors.

  contained         head is in the freshly fetched origin target
  landed-clean      landed by receipt (squash, rebase, or absorbed by an
                    integration branch) with nothing left over
  landed-residue    landed, plus local commits past the landed head, reported
                    as "landed + residue"; retired only with --allow-residue,
                    which lists the commits first
  detached-review   a detached checkout at a landed pull request's head
  detached-unknown  detached with no landing association: kept
  open-pr           a pull request is still awaiting a decision: kept
  dirty             uncommitted changes: kept
  claimed-live      in use within --session-freshness, or held by a live
                    operation: kept
  unpushed          a head GitHub has never seen; nothing ever retires it
  unmerged          pushed, not landed, no open pull request: kept

Branches without a checkout use Branch Hygiene with landing receipts on:

  contained         ancestor of the fetched target: retired
  receipted         a merged pull request into the target proves the landing:
                    retired. That includes a squash-merged branch whose tip is
                    exactly the merged pull request's head, even after the
                    target moved on
  superseded        a trusted reviewer receipt (--superseded-by) replaces it
  absorbed          equal by patch-id or tree only: never retired
  unique            carries work the target does not: kept

Linked worktrees WB does not manage are listed as "unmanaged" and never
removed; adopt one with wb worktree adopt to bring it under this classifier.

Removal goes through the per-item transactions: wb worktree retire's cleanup
transaction for checkouts (descriptor-anchored guards, durable Work Log seal
and receipt) and Branch Hygiene's compare-and-delete for branches, each
re-proving its evidence against a fresh fetch immediately before deleting.
--parallel bounds both phases: repositories are inspected and retired that
many at a time, one writer per clone and per task, and each repository's
target is fetched once per walk however many tasks it holds.

There is deliberately no force flag. --allow-residue and --superseded-by are
the only widenings, and both print the evidence they widen past.
Work Logs and event logs are never touched. Sizes of eligible checkouts are reported
apparent and unshared, because pnpm hard-links node_modules into its store;
--skip-sizes skips the measurement.

Exit codes: 0 when nothing needed attention, 1 when something was kept or the
selection matched nothing, 2 on a usage error.`,
		Example: `# Plan a fleet-wide sweep
wb clean

# Plan one owner's repositories, landed work only
wb clean --filter 'datatug/*' --only landed

# Retire everything provably finished, including remote branches
wb clean --scope all --apply

# Machine-readable plan for an agent
wb clean --org datatug --format json`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			if sessionFreshness < 0 {
				return runtime.ExitError(shared.ExitUsage, "--session-freshness cannot be negative; use 0 to disable the in-use rule")
			}
			flags := runtime.Flags()
			progress := deps.Progress(command, verbose)
			defer progress.Finish()
			outcome, err := deps.Run(command.Context(), worktrees.CleanOptions{
				ProjectsRoot: flags.ProjectsRoot, Filter: flags.Filter, Orgs: flags.ExtraOrgs,
				Repository: repository, Branch: branch, Tasks: tasks, Scope: scope, Only: only,
				Base: base, OlderThan: olderThan, Apply: apply, AllowResidue: allowResidue,
				SupersededBy: supersededBy, Workers: parallel, SkipSizes: skipSizes, SkipDetached: skipDetached,
				TTL: ttl, SessionFreshness: disabledWhenZero(sessionFreshness), ResidueDepth: residueDepth,
				Progress: progress.Report,
			})
			if errors.Is(err, worktrees.ErrNoRepositoriesMatched) {
				return runtime.ExitError(shared.ExitFindings, err.Error())
			}
			if err != nil {
				return err
			}
			progress.Finish()
			if outcome.Worktrees != nil {
				warnings := &stickyWriter{out: command.ErrOrStderr()}
				for _, diagnostic := range outcome.Worktrees.Diagnostics {
					warnings.printf("warning: clean skipped malformed candidate in task %s: %s: %s\n", diagnostic.Task, diagnostic.Path, diagnostic.Message)
				}
				if warnings.err != nil {
					return warnings.err
				}
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(outcome); err != nil {
					return err
				}
			} else if err := printClean(command, outcome); err != nil {
				return err
			}
			if outcome.Refused() > 0 {
				return runtime.ExitError(shared.ExitFindings, fmt.Sprintf(
					"%d item(s) were kept; each row above names the reason and the command that resolves it", outcome.Refused()))
			}
			return nil
		},
	}
	flags := command.Flags()
	flags.BoolVar(&apply, "apply", false, "retire eligible worktrees and branches instead of planning")
	flags.StringVar(&repository, "repo", "", "exact owner/name repository selector")
	flags.StringVar(&branch, "branch", "", "exact branch selector, of a checkout or a branch ref")
	flags.StringArrayVar(&tasks, "task", nil, "exact WB task selector (repeatable); selects checkouts only")
	flags.StringVar(&scope, "scope", worktrees.CleanScopeLocal, "local (checkouts and local branches), remote (remote branches), or all")
	flags.StringVar(&only, "only", "", "narrow to one class, or landed for every class proving a landing")
	flags.BoolVar(&allowResidue, "allow-residue", false, "also retire a landed checkout holding local commits past the landed head, discarding them")
	flags.StringVar(&supersededBy, "superseded-by", "", "retire one item (--task, or --repo and --branch) on a trusted-reviewer supersession receipt")
	flags.BoolVar(&skipDetached, "skip-detached", false, "leave detached checkouts out of the sweep entirely")
	flags.BoolVar(&skipSizes, "skip-sizes", false, "do not measure disk usage of eligible checkouts")
	flags.StringVar(&base, "base", "main", "fallback origin target branch for candidates without a recorded one")
	flags.DurationVar(&olderThan, "older-than", 24*time.Hour, "keep work whose pull request merged, or whose branch was committed, less than this ago (0 disables)")
	flags.DurationVar(&ttl, "ttl", 7*24*time.Hour, "report a checkout older than this as expired")
	flags.DurationVar(&sessionFreshness, "session-freshness", worktrees.DefaultSessionFreshness,
		"how recently a checkout must have been used to count as in use; 0 disables the rule")
	flags.IntVar(&residueDepth, "residue-depth", worktrees.DefaultResidueDepth, "how many commits back from HEAD to consult the commit-to-pull-request index")
	flags.IntVar(&parallel, "parallel", worktrees.DefaultInspectWorkers, "maximum repositories to inspect and retire concurrently")
	flags.StringVar(&format, "format", "text", "stdout format: text or json")
	flags.BoolVarP(&verbose, "verbose", "v", false, "stream per-candidate inspection progress to stderr, even when not on a terminal")
	return command
}

func disabledWhenZero(window time.Duration) time.Duration {
	if window == 0 {
		return worktrees.DisableSessionFreshness
	}
	return window
}

// stickyWriter keeps the first write error so a renderer can write freely
// and report once.
type stickyWriter struct {
	out io.Writer
	err error
}

func (writer *stickyWriter) printf(format string, args ...any) {
	if writer.err == nil {
		_, writer.err = fmt.Fprintf(writer.out, format, args...)
	}
}

func printClean(command *cobra.Command, outcome worktrees.CleanOutcome) error {
	out := &stickyWriter{out: command.OutOrStdout()}
	out.printf("selected %d repositories\n", len(outcome.Selection.Repositories))
	if outcome.Worktrees != nil {
		out.printf("\nworktrees\n")
		if out.err != nil {
			return out.err
		}
		if err := printWorktreeGC(command, *outcome.Worktrees); err != nil {
			return err
		}
	}
	out.printf("\nbranches without a worktree\n")
	if len(outcome.Branches) == 0 {
		out.printf("none\n")
	}
	for _, result := range outcome.Branches {
		state := "keep"
		switch {
		case result.Applied:
			state = "retired"
		case result.Eligible:
			state = "would retire"
		}
		// One line per branch carries the repository and the evidence, so a
		// grepped or scrolled row is still actionable on its own.
		out.printf("%-28s %-6s %-34s %-10s %-12s %s: %s\n", result.Repository, result.Scope, result.Branch, result.Disposition, state, result.ShortSHA, result.Evidence)
		if !result.Eligible && result.SkipReason != "" && result.SkipReason != result.Evidence {
			out.printf("    kept: %s\n", result.SkipReason)
		}
		if result.Error != "" {
			out.printf("    error: %s\n", result.Error)
		}
	}
	for _, diagnostic := range outcome.BranchDiagnostics {
		out.printf("warning: %s\n", diagnostic)
	}
	for _, report := range outcome.BranchReports {
		out.printf("branch report: %s\n", report)
	}
	if len(outcome.Unmanaged) > 0 {
		out.printf("\nunmanaged worktrees (reported, never removed)\n")
	}
	for _, worktree := range outcome.Unmanaged {
		out.printf("%-28s %-34s %-9s %-8s %s\n", worktree.Repository, worktree.Branch, worktree.Layout, worktree.Disposition, worktree.Path)
		if len(worktree.Evidence) > 0 {
			out.printf("    %s\n", worktree.Evidence[len(worktree.Evidence)-1])
		}
	}
	for _, unscanned := range outcome.UnmanagedUnscanned {
		out.printf("warning: not scanned: %s\n", unscanned)
	}
	verb := "would retire"
	count := outcome.Totals["eligible"]
	if outcome.Apply {
		verb, count = "retired", outcome.Totals["retired"]
	}
	out.printf("\n%d %s, %d kept, %d unmanaged\n", count, verb, outcome.Totals["refused"], outcome.Totals["unmanaged"])
	return out.err
}
