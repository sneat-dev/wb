package cmdworktree

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

type AbortDependencies struct {
	Run         func(context.Context, worktrees.AbortOptions) ([]worktrees.AbortResult, error)
	Release     func(*cobra.Command, string, string) bool
	SkipRelease func(*cobra.Command, string)
}

func NewAbort(runtime shared.Runtime, deps AbortDependencies) *cobra.Command {
	var base, disposition, successor, absorbedBy, closedPR, format string
	var claimID, actor, reason string
	var model, cli, provider string
	var apply, deleteRemote, all bool
	command := &cobra.Command{
		Use:   "abort <task>",
		Short: "Seal an interrupted task so it can be resumed or explicitly discarded",
		Long: `Abort is the audited alternative to manually deleting an unfinished worktree.
It seals each local Work Log and queues an offline-safe outbox event. --apply
with handoff or not_landed transfers one active claim to the required
--successor while leaving the branch/worktree resumable. The creator must
declare the successor's exact --model or explicit unknown; --cli and
--provider independently record the invoking client and commercial route when
known, never credentials. --apply with
discarded removes only unlocked worktrees, retaining a bounded private capture
of tracked and untracked bytes before deleting dirty ones, and removes their
exact local branch refs only after that archive has been sealed. A discarded apply requires --remote,
except for a proven pre-apply recycle reservation with no checkout, branch, or remote ref; an
exact matching remote source branch is otherwise retired with force-with-lease.
If interruption happens after worktree removal, the same command inspects and
resumes the durable exact local-branch cleanup backlog.

For a clean source retained after a squash landing, --disposition discarded
--absorbed-by <merged-pr> verifies GitHub's merged PR metadata and rechecks
the exact source head, fetched PR head, landing tree, and freshly fetched
target before removal. It never accepts a commit-message reference as proof.
A genuine "Create a merge commit" landing is verified too, even when the
target advanced past the source's last sync with it before the merge and the
PR head's tree therefore no longer equals the merge commit's tree: the exact
source head's own Git ancestry into the freshly fetched target is checked
instead of tree equality, so a landing shape squash's tree-equality proof
would wrongly reject is still accepted on its own, topology-appropriate
evidence. A merged pull request may be named by its bare number, a leading
"#", or its full GitHub URL — all three resolve the same pull request.

If 'wb worktree log finalize --apply' already sealed the exact worktree as
landed before its branch was rebased onto a moved target (force-pushed with
lease) and only the rebased head ever landed, --absorbed-by's proof above
still verifies that landing; --disposition discarded then appends the same
additive cleanup record 'wb worktree cleanup' writes for an advanced
terminal instead of trying to reseal the immutable "landed" terminal as
"discarded" at a different commit, which used to refuse with "immutable
terminal conflicts with requested transition". The original finalize
terminal is never rewritten.

For a checkout whose pull request was CLOSED WITHOUT MERGING (a duplicate whose
twin landed, a superseded attempt), --disposition discarded --closed-pr
<pr-number|pr-url> --reason <why> is the supported discard. WB reads the pull
request from GitHub and proceeds only when it is closed and unmerged, in this
repository, with head branch and head commit exactly equal to the checkout's;
a checkout with any commit the closed pull request never carried, or any
uncommitted byte, is refused. The check is repeated under the task lock
immediately before removal, the remote branch is retired only at that exact
head (--remote, as for every discard), and a durable audit record naming the
task, repository, head commit, pull request, and your --reason is written under
WB home in closed-pr-discards/ before anything is removed. --closed-pr does
not combine with --absorbed-by: a pull request is either merged or not.

An orphaned disposition is narrower: the worktree and its local/remote branch
are already gone, so WB deletes nothing. It requires one exact --claim plus an
explicit --actor and --reason. Dry run and apply both prove the worktree path,
Git registration, local branch, remote branch, and prior terminal record are
absent; apply repeats every predicate under the claim lock before writing an
append-only orphaned terminal receipt. It never pretends the vanished content
or a final commit was inspected, and --remote is rejected.

--filter (see the root flag) narrows which repositories in the task are
touched, the same "owner/repository slug contains this substring" semantics
as wb branch cleanup --filter. A repository --filter excludes is left
completely untouched and still reported, so one repository blocked on
something abort cannot fix no longer makes the rest of the task un-abortable.
The task remains non-terminal — and the remote claim, if any, is not
released — until every repository is eventually resolved.

The default is a dry-run plan.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			flags := runtime.Flags()
			results, err := deps.Run(command.Context(), worktrees.AbortOptions{
				ProjectsRoot: flags.ProjectsRoot, Task: args[0], Base: base, Filter: flags.Filter,
				Disposition: worktrees.AbortDisposition(disposition), Successor: successor, All: all,
				AbsorbedBy: absorbedBy, ClosedPullRequest: closedPR,
				ClaimID: claimID, Actor: actor, Reason: reason,
				SuccessorIdentity: worktrees.ClaimExecutionIdentity{Model: model, CLI: cli, Provider: provider},
				DeleteRemote:      deleteRemote, Apply: apply,
			})
			if err != nil {
				return err
			}
			// Abort carries the durable records it declined to act on the
			// first result — see AbortResult.Quarantined. Name them on stderr
			// in both formats, exactly as cleanup does, so a text-mode
			// operator learns of them at all.
			if len(results) > 0 {
				for _, quarantined := range results[0].Quarantined {
					if _, err := fmt.Fprintf(command.ErrOrStderr(),
						"warning: abort skipped an unusable durable record %s (task %s): %s\n",
						quarantined.Path, quarantined.Task, quarantined.Reason); err != nil {
						return err
					}
				}
			}
			remaining := 0
			for _, result := range results {
				if result.Excluded {
					remaining++
				}
			}
			// Release only follows a disposition that actually ends the
			// task on this machine: discarded drops it, handoff moves it to
			// a successor. not_landed leaves the task expected to resume
			// here, so the claim must stay put. A --filter that left any
			// repository untouched means the task is not actually finished,
			// so the remote claim must not be released either.
			disp := worktrees.AbortDisposition(disposition)
			releasable := disp == worktrees.AbortDiscarded || disp == worktrees.AbortHandoff
			// releaseLeaked is wb#321's exit-code seam: see
			// exitNonZeroOnReleaseLeak in remote_autoclaim.go for why it is
			// false today, and flip that one constant, not this line, to
			// change it.
			var releaseLeaked bool
			switch {
			case apply && releasable && remaining == 0:
				releaseLeaked = deps.Release(command, flags.ProjectsRoot, args[0])
			case apply && releasable && remaining > 0:
				deps.SkipRelease(command, fmt.Sprintf("%d repositories excluded by --filter still remain", remaining))
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(results); err != nil {
					return err
				}
				if releaseLeaked {
					return fmt.Errorf("task %q worktree sealed but its remote claim release failed (see wb#321)", args[0])
				}
				return nil
			}
			if err := printAbort(command, results, remaining, args[0]); err != nil {
				return err
			}
			if releaseLeaked {
				return fmt.Errorf("task %q worktree sealed but its remote claim release failed (see wb#321)", args[0])
			}
			return nil
		},
	}
	command.Flags().StringVar(&base, "base", "main", "base branch for managed-worktree validation")
	command.Flags().StringVar(&disposition, "disposition", "", "required: handoff, not_landed, discarded, or orphaned")
	command.Flags().StringVar(&successor, "successor", "", "one successor agent/session ID (required for handoff or not_landed)")
	command.Flags().StringVar(&absorbedBy, "absorbed-by", "", "merged pull request that proves a clean squash-absorbed source before discarded removal")
	command.Flags().StringVar(&closedPR, "closed-pr", "", "pull request number or URL that GitHub reports closed unmerged with head exactly equal to this clean checkout's head; with discarded and --reason")
	command.Flags().StringVar(&claimID, "claim", "", "exact immutable Work Log claim ID (required for orphaned)")
	command.Flags().StringVar(&actor, "actor", "", "approving person or agent identity (required for orphaned)")
	command.Flags().StringVar(&reason, "reason", "", "audit reason: required for an orphaned claim and for discarded --closed-pr")
	command.Flags().StringVar(&model, "model", "", "required with applied handoff/not_landed: exact successor model or explicit unknown; WB never guesses")
	command.Flags().StringVar(&cli, "cli", "", "optional invoking CLI/client identifier, supplied only when known")
	command.Flags().StringVar(&provider, "provider", "", "optional routing/billing provider identifier, never a credential")
	command.Flags().BoolVar(&apply, "apply", false, "seal Work Logs and apply the selected disposition")
	command.Flags().BoolVar(&all, "all", false, "acknowledge every repository in a coordinated task")
	command.Flags().BoolVar(&deleteRemote, "remote", false, "retire an exact unchanged remote source branch when applying discarded")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}
