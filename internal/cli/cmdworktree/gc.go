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

type GCDependencies struct {
	Run      func(context.Context, worktrees.GCOptions) (worktrees.GCOutcome, error)
	Progress func(*cobra.Command, bool) InventoryProgress
}

func NewGC(runtime shared.Runtime, deps GCDependencies) *cobra.Command {
	var base, format, supersededBy string
	var apply, allowResidue, skipDetached, skipSizes, deleteRemote, verbose bool
	var olderThan, ttl, sessionFreshness time.Duration
	var residueDepth, parallel int
	command := &cobra.Command{
		Use:   "gc [task...]",
		Short: "Classify every WB checkout by landing evidence and retire the finished ones",
		Long: `Retire every WB-managed checkout that is provably finished, and list every
one that is not with the reason and the command that resolves it.

Merged-ness is decided by commit identity, never by branch-name ancestry: a
squash merge produces a new commit, so Git reports every landed branch as
unmerged forever and the cheap local signal is always wrong. gc reads GitHub's
own commit-to-pull-request index for the checkout's head and, when that head was
never pushed, for its ancestors — so a branch carrying one post-merge commit is
reported as "landed + residue" with the residual commits listed, rather than as
a bare "awaiting push".

Classes, each decided by evidence and printed with it:

  contained         head is in the freshly fetched origin target
  landed-clean      landed by receipt: squash, rebase, or absorbed by an
                    integration branch, with nothing left over
  landed-residue    landed, plus local commits past the landed head; retire it
                    with --allow-residue, which prints them before discarding
  detached-review   a detached checkout at a landed pull request's head — what
                    every review creates, and what nothing in WB could retire
  detached-unknown  detached with no landing association: refused
  open-pr           a pull request is still awaiting a decision: refused
  dirty             uncommitted changes: refused
  claimed-live      a live operation holds it, or the checkout was used inside
                    --session-freshness: refused. "Used" means any of four
                    signals — a heartbeat every wb command inside the checkout
                    refreshes, a file Git reports as changed, a Work Log event,
                    or a commit — because a lane may be doing only one kind of
                    work. A live process id alone counts for nothing: ids are
                    recycled, and the question is about the worktree, not about
                    a process. --session-freshness 0 disables the rule
  unpushed          a head GitHub has never seen. This is the only class that
                    can lose work, so nothing ever retires it
  unmerged          pushed, not landed, no open pull request: refused

Dry run is the default. --apply removes eligible checkouts through the ordinary
cleanup transaction — the same descriptor-anchored guards, the same durable Work
Log seal and receipt — one repository at a time, so a coordinated task retires
per repository and names the repositories it left behind.

There is deliberately no force flag. --allow-residue and --superseded-by are the
only two widenings, and both print the evidence they widen past: --allow-residue
lists the commits it is about to discard, and --superseded-by names the receipt
and the reviewer who approved it, after re-verifying every head the receipt
binds. Work Logs and event logs are never touched: they are the evidence base
for every WB report and are small enough that keeping them costs nothing.

Empty .wb-retired-stage-* directories and inert .wb-retired-lock-* files are
purged unconditionally and silently on any worktree read path, gc included, and
counted in the footer rather than logged one line per artefact.

Sizes are reported twice, apparent and unshared, because pnpm hard-links
node_modules into its store: one measured sweep was 11.7 GB apparent against 5.9
GB unshared, and a reclaim figure counting hard-linked bytes promises a saving
it cannot deliver. Only eligible checkouts are measured, since the reclaim
footer is the only figure that needs a size.

Exit codes: 0 when nothing needed attention, 1 when something was refused, 2 on
a usage error.`,
		Example: `# Plan a fleet-wide sweep
wb worktree gc

# Retire everything provably finished
wb worktree gc --apply

# Retire one task whose branch landed and kept a post-merge commit
wb worktree gc my-task --allow-residue --apply

# Machine-readable plan for an agent
wb worktree gc --format json`,
		Args: cobra.ArbitraryArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			if sessionFreshness < 0 {
				return runtime.ExitError(shared.ExitUsage, "--session-freshness cannot be negative; use 0 to disable the in-use rule")
			}
			flags := runtime.Flags()
			progress := deps.Progress(command, verbose)
			defer progress.Finish()
			outcome, err := deps.Run(command.Context(), worktrees.GCOptions{
				ProjectsRoot: flags.ProjectsRoot,
				Tasks:        args,
				Filter:       flags.Filter,
				Base:         base,
				Apply:        apply,
				AllowResidue: allowResidue,
				SupersededBy: supersededBy,
				SkipDetached: skipDetached,
				OlderThan:    olderThan,
				TTL:          ttl,
				// `--session-freshness 0` reads as "do not apply this rule",
				// the same spelling as `--older-than 0`.
				SessionFreshness: disabledWhenZero(sessionFreshness),
				ResidueDepth:     residueDepth,
				Workers:          parallel,
				SkipSizes:        skipSizes,
				DeleteRemote:     deleteRemote,
				Progress:         progress.Report,
			})
			if err != nil {
				return err
			}
			progress.Finish()
			switch format {
			case "json":
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(outcome); err != nil {
					return err
				}
			default:
				if err := printWorktreeGC(command, outcome); err != nil {
					return err
				}
			}
			if outcome.Refused() > 0 {
				return runtime.ExitError(shared.ExitFindings, fmt.Sprintf(
					"%d checkout(s) were kept; each row above names the reason and the command that resolves it", outcome.Refused()))
			}
			return nil
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "retire eligible checkouts instead of planning")
	command.Flags().BoolVar(&allowResidue, "allow-residue", false, "also retire a landed checkout holding local commits past the landed head, discarding them")
	command.Flags().StringVar(&supersededBy, "superseded-by", "", "retire one named task on an explicit trusted-reviewer supersession receipt")
	command.Flags().BoolVar(&skipDetached, "skip-detached", false, "leave detached checkouts out of the sweep entirely")
	command.Flags().BoolVar(&skipSizes, "skip-sizes", false, "do not measure disk usage of eligible checkouts")
	command.Flags().BoolVar(&deleteRemote, "remote", false, "also delete an unchanged source branch on origin")
	command.Flags().StringVar(&base, "base", "main", "fallback origin target branch for candidates without a recorded one")
	command.Flags().DurationVar(&olderThan, "older-than", 0, "keep a checkout whose pull request merged less than this ago")
	command.Flags().DurationVar(&ttl, "ttl", 7*24*time.Hour, "report a checkout older than this as expired")
	command.Flags().DurationVar(&sessionFreshness, "session-freshness", worktrees.DefaultSessionFreshness,
		"how recently a checkout must have been used to count as in use; 0 disables the rule")
	command.Flags().IntVar(&residueDepth, "residue-depth", worktrees.DefaultResidueDepth, "how many commits back from HEAD to consult the commit-to-pull-request index")
	command.Flags().IntVar(&parallel, "parallel", worktrees.DefaultInspectWorkers, "maximum repositories to inspect concurrently")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVarP(&verbose, "verbose", "v", false, "stream per-candidate inspection progress to stderr, even when not on a terminal")
	return command
}
func disabledWhenZero(window time.Duration) time.Duration {
	if window == 0 {
		return worktrees.DisableSessionFreshness
	}
	return window
}
