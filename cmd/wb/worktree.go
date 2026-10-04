package main

import (
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/checkoutsetup"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cli/cmdworktree"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/worktreerun"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// autoReleaseWriter selects the output stream for auto-release lines based on
// format. For json output, the release line is informational and belongs on
// stderr to keep stdout as pure JSON (matching worktree create's pattern of
// routing auto-claim output to io.Discard in json mode). For text output,
// stdout is correct.
// remoteClaimWriter returns the stream remote-claim notes belong on.
//
// They are diagnostics about a side channel, not the result of the command the
// user asked for, and wb's contract puts diagnostics on stderr. Keeping them
// off stdout means a json document is never corrupted, a text result is never
// padded with unrelated lines, and a command that fails writes nothing to
// stdout at all.
func remoteClaimWriter(cmd *cobra.Command) io.Writer {
	return cmd.ErrOrStderr()
}

var recoverRetiredStages = worktrees.RecoverRetiredStages

// cleanupWorktreeTasks is the cleanup engine the command drives. It is a
// variable so the flag-to-option mapping can be asserted without Git.
var cleanupWorktreeTasks = worktrees.Cleanup

func newWorktreeCmd(inv *invocation) *cobra.Command {
	command := &cobra.Command{
		Use:     "worktree",
		Aliases: []string{"worktrees", "wt"},
		Short:   "Create, inspect, merge, and safely retire isolated agent work",
	}
	command.AddGroup(
		&cobra.Group{ID: "start", Title: "Start work"},
		&cobra.Group{ID: "finish", Title: "Finish work"},
		&cobra.Group{ID: "inspect", Title: "Inspect progress"},
		&cobra.Group{ID: "recover", Title: "Recover and coordinate"},
		&cobra.Group{ID: "admin", Title: "Administration"},
	)
	children := []struct {
		command *cobra.Command
		group   string
	}{
		{newWorktreeCreateCmd(inv), "start"},
		{newWorktreeAdoptCmd(inv), "start"},
		{newWorktreeMergeCmd(inv), "finish"},
		{newWorktreeLandCmd(inv), "finish"},
		{newWorktreeEndCmd(inv), "finish"},
		{newWorktreeCleanupCmd(inv), "finish"},
		{newWorktreeRetireCmd(inv), "finish"},
		{newWorktreeGCCmd(inv), "finish"},
		{newWorktreeAbortCmd(inv), "finish"},
		{newWorktreeSummaryCmd(inv), "inspect"},
		{newWorktreeActiveCmd(inv), "inspect"},
		{newWorktreeInfoCmd(inv), "inspect"},
		{newWorktreeListCmd(inv), "inspect"},
		{newWorktreeGuardCmd(inv), "recover"},
		{newWorktreeRescueCmd(inv), "recover"},
		{newWorktreeWorkLogCmd(inv), "recover"},
		{newWorktreeCheckpointFetchCmd(), "recover"},
		{newWorktreeOwnCmd(), "recover"},
		{newWorktreeMarkerCmd(inv), "admin"},
		{newWorktreeRelocateCmd(inv), "admin"},
		{newWorktreeRenameCmd(inv), "admin"},
		{newWorktreeCorrectIdentityCmd(inv), "admin"},
		{newWorktreeSetCmd(inv), "admin"},
		{newWorktreeOrphansCmd(inv), "admin"},
		{newWorktreeBackfillCmd(inv), "admin"},
	}
	for _, child := range children {
		child.command.GroupID = child.group
		command.AddCommand(child.command)
	}
	addCollaborationCommands(command, inv)
	return command
}

func newWorktreeRelocateCmd(inv *invocation) *cobra.Command {
	command := cmdworktree.NewRelocate(newCLIRuntime(inv), cmdworktree.RelocateDependencies{Run: worktrees.Relocate, Admit: requireMutationAdmission, AfterApply: func(command *cobra.Command, results []worktrees.RelocateResult) {
		markRelocatedCheckouts(inv, command, results)
	}, StartProgress: worktreerun.StartRelocationProgress})
	addMutationAdmissionFlags(command)
	return command
}

func newWorktreeCheckpointFetchCmd() *cobra.Command {
	return cmdworktree.NewCheckpointFetch(worktrees.FetchRemoteCheckpoint)
}

func newWorktreeInfoCmd(inv *invocation) *cobra.Command {
	service := worktreerun.DefaultInfoService()
	return cmdworktree.NewInfo(newCLIRuntime(inv), service.Inspect)
}

func newWorktreeWorkLogCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewWorkLog(newCLIRuntime(inv), worktreeJournalOperations(), requireMutationAdmission)
}

func newWorktreeCorrectIdentityCmd(inv *invocation) *cobra.Command {
	command := cmdworktree.NewCorrectIdentity(newCLIRuntime(inv), worktrees.CorrectExecutionIdentity, requireMutationAdmission, mutationInitiator)
	addMutationAdmissionFlags(command)
	return command
}

func newWorktreeAbortCmd(inv *invocation) *cobra.Command {
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
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			results, err := worktrees.Abort(command.Context(), worktrees.AbortOptions{
				ProjectsRoot: inv.projectsRoot, Task: args[0], Base: base, Filter: inv.filterFlag,
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
				result := releaseRemoteClaim(inv.projectsRoot, args[0], remoteClaimWriter(command))
				releaseLeaked = exitNonZeroOnReleaseLeak && result.Leaked()
			case apply && releasable && remaining > 0:
				skippedAutoRelease(remoteClaimWriter(command), fmt.Sprintf("%d repositories excluded by --filter still remain", remaining))
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
			for _, result := range results {
				if result.Excluded {
					reason := result.Reason
					if reason == "" {
						reason = "not evaluated"
					}
					if _, err := fmt.Fprintf(command.OutOrStdout(), "excluded by --filter %s %s: %s\n", result.Repository, result.Disposition, reason); err != nil {
						return err
					}
					continue
				}
				state := "would seal"
				if !result.Eligible {
					// A dry run must not promise a seal the apply will refuse.
					if _, err := fmt.Fprintf(command.OutOrStdout(), "cannot seal %s %s: %s\n", result.Repository, result.Disposition, result.Reason); err != nil {
						return err
					}
					continue
				}
				if result.Applied && result.Disposition == worktrees.AbortOrphaned {
					state = "sealed absent claim"
				} else if result.Applied && result.WorktreeGone {
					state = "sealed and removed"
				} else if result.Applied {
					state = "sealed and resumable"
				}
				if _, err := fmt.Fprintf(command.OutOrStdout(), "%s %s %s\n", state, result.Repository, result.Disposition); err != nil {
					return err
				}
				if closed := result.ClosedPullRequest; closed != nil {
					if _, err := fmt.Fprintf(command.OutOrStdout(), "  closed pull request %s#%d head %s (%s)%s\n",
						closed.Repository, closed.Number, closed.HeadSHA, closed.Reason, closedAuditSuffix(closed.AuditPath)); err != nil {
						return err
					}
				}
			}
			if remaining > 0 {
				if _, err := fmt.Fprintf(command.OutOrStdout(), "%d repositories excluded by --filter remain unresolved for task %q\n", remaining, args[0]); err != nil {
					return err
				}
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

func closedAuditSuffix(path string) string {
	if path == "" {
		return ""
	}
	return "; audit " + path
}

func newWorktreeCreateCmd(inv *invocation) *cobra.Command {
	command := cmdworktree.NewCreate(newCLIRuntime(inv), worktreeCreateDependencies(inv))
	setDiscoveryTerms(command, "start begin create new work task agent isolated worktree branch implement edit code save tokens multi repo multiple repositories cross-repository repo")
	markQuietVerb(command)
	return command
}

func worktreeCreateDependencies(inv *invocation) cmdworktree.CreateDependencies {
	return cmdworktree.CreateDependencies{
		Run:                worktrees.Create,
		OriginSlug:         worktrees.OriginSlug,
		PrepareLog:         worktrees.PrepareWorkLogOptions,
		RegisteredIdentity: worktrees.RegisteredIdentity,
		BeforeCreate: func(root string, repositories []string) error {
			return checkoutsetup.BeforeCreate(root, repositories, checkoutsetup.DefaultHookDependencies(hookExecutable))
		},
		Claim: func(command *cobra.Command, noClaim bool, root, task string) worktreerun.RemoteClaimOutcome {
			return claimRemoteTask(noClaim, root, task, outcomeClaimWriter(command, inv.quiet))
		},
		AfterCreate: func(command *cobra.Command, base string, results []worktrees.CreateResult) {
			markCreatedCheckouts(inv, command, base, results)
		},
	}
}

// newCreateCmd is the root-level alias for `wb worktree create`: `wb create`.
// Starting isolated work is the other half of the agent workflow `wb land`
// (see newLandCmd in worktree_merge.go) closes out, and it had the identical
// discoverability problem: nothing at the top level named it, so it sat one
// noun below where an agent skimming `wb --help` would look first.
//
// It is built from the exact same constructor as `wb worktree create`, so the
// two commands share identical flags, help text, and exit codes by
// construction rather than by two copies staying in sync; only the command
// path they resolve under differs ("wb create" vs "wb worktree create").
func newCreateCmd(inv *invocation) *cobra.Command {
	return newWorktreeCreateCmd(inv)
}

func newWorktreeGuardCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewGuard(newCLIRuntime(inv), worktrees.Guard, console.IsTerminal)
}

// newWorktreeSetCmd is the human-facing remedy the admission gate names. It
// deliberately records a prompt rather than setting a bypass flag: the act of
// unblocking a commit is itself the record of who directed it.
func newWorktreeSetCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewSet(newCLIRuntime(inv), worktrees.LogSteer)
}

func newWorktreeBackfillCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewBackfill(newCLIRuntime(inv), worktrees.Backfill)
}

func newWorktreeAdoptCmd(inv *invocation) *cobra.Command {
	command := cmdworktree.NewAdopt(newCLIRuntime(inv), worktrees.Adopt, func(command *cobra.Command, apply bool) (func(), error) {
		_, release, err := requireMutationAdmission(command, apply)
		return release, err
	})
	addMutationAdmissionFlags(command)
	return command
}

func newWorktreeOrphansCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewOrphans(newCLIRuntime(inv), worktrees.Orphans)
}

func newWorktreeListCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewList(newCLIRuntime(inv), worktrees.ListWithDiagnostics)
}

func newWorktreeSummaryCmd(inv *invocation) *cobra.Command {
	return cmdworktree.NewSummary(newCLIRuntime(inv), worktrees.ListWithDiagnostics)
}

func newWorktreeCleanupCmd(inv *invocation) *cobra.Command {
	var base, format, reportDir, absorbedBy, supersededBy string
	var allMerged, apply, deleteRemote, resumeInterrupted, retireShells, recoverStages, verbose bool
	var olderThan time.Duration
	var workers int
	command := &cobra.Command{
		Use:   "cleanup [task...]",
		Short: "Plan or remove clean WB tasks integrated into the exact origin target",
		Long: `Plan or apply cleanup of WB-managed task worktrees and local branches.

Cleanup requires every repository in a coordinated task to be clean, unlocked,
and to have its current branch head integrated into the freshly fetched exact
origin target. A matching merged GitHub pull request supplies merge-time age
evidence, but a verified direct push to the target is also eligible. A local
merge that was not pushed remains awaiting_push. The default is a dry-run plan.

The target is the base recorded when the worktree was created, per repository.
Two situations change it, and the plan names the target that proved the work
either way (integration_proof in JSON, in parentheses in text):

  - The recorded base is gone from origin (an integration branch that landed
    and was deleted), or it is still there and has itself landed in the
    repository's default branch (a task stacked on another task's branch).
    The head is then judged against the freshly fetched default branch, and
    is eligible only when it is a plain Git ancestor of it. A head that is not
    stays in the plan with the reason; it is never dropped as malformed.
  - --base <branch> given explicitly is the exact origin target the head is
    judged against, and nothing else: the recorded base is only reported
    (recorded_base), no other branch is substituted (not the default branch,
    and not the target of a merged pull request), and a branch origin does
    not have is an error. A head that branch contains is reported as
    "contained in origin/<branch> at <sha>, the base named with --base".
    Left at its default, --base is only the fallback for a worktree with no
    recorded base.

A task whose branch is the recorded base of another listed task that is not
eligible, or whose stacked task failed to apply in this run, is held: retiring
it would delete the branch that other work, and any pull request into it,
still stands on. The refusal names the dependant.

Every refusal that compares against a target names the ref and SHA it used.
--apply removes worktrees and exact local branch refs; --remote additionally
deletes an unchanged remote branch with force-with-lease protection. Durable
Work Log archive/outbox evidence is written before any remote or local deletion.
The same named dry-run/apply command inspects and resumes a durable exact-ref
backlog if interruption happened after a worktree disappeared.

If 'wb worktree log finalize --apply' already sealed the exact current HEAD as
landed, cleanup corroborates that immutable terminal without ever rewriting
it. A branch that earned more commits after finalize sealed an earlier head —
a rebase or merge onto main, or a follow-up push, that then landed on the
target as a merge commit — is still eligible: once the current head
independently re-proves it is a Git descendant of the exact commit finalize
sealed, cleanup appends a separate, additive cleanup record next to the
sealed terminal instead of refusing outright. When the sealed commit was
instead force-pushed onto a moved target (a rebase, not a plain follow-up
commit) and is therefore no longer a Git descendant at all, cleanup falls
back to proving the narrower, still-sufficient fact a clean rebase preserves:
every one of the sealed commit's own non-merge patches (by stable
git patch-id) is present somewhere in the current head's own history since
their common ancestor. Only a landed terminal without a successor or exotic
evidence (handoff, dirty capture, supersession) can be advanced this way.

A branch whose exact head never reaches the target because a merger batched it
onto a differently named integration branch and landed that branch once is
still eligible, on evidence. WB reads GitHub's own commit-to-pull-request
index for the immutable source commit; when that names a merged pull request
into this exact base whose merge commit is contained in the fetched target, WB
then proves containment locally: merging the branch into that merge commit
must add nothing to it, and merging it into the target must add nothing there
either. Work that landed and was later reverted therefore stays blocked.

--absorbed-by <pr-number|pr-url|commit> covers a landing GitHub cannot
associate, such as content cherry-picked rather than merged into the
integration branch. It only selects which receipt to verify; every proof
above still runs, and the named commit must additionally be exactly where the
work entered the target, so the flag can never make unlanded work eligible.
A pull request may be named by its bare number, a leading "#", or its full
GitHub URL — all three resolve the same pull request.

--superseded-by <receipt.json> is the explicit trusted-reviewer receipt
authority for an intentionally split branch whose original head did not land
as one unit.
The receipt must bind the exact source and fetched target heads, replacement
PRs or commits, every source residual with a machine-readable classification,
and a trusted approving actor. Missing or unclassified residuals, changed refs,
or missing approval refuse without deleting anything. This option is named-task
only and never participates in --all-merged fleet sweeps.

Reserved .wb-stage-* and .wb-retired-stage-* entries are first-class cleanup
backlog. Apply descriptor-safely archives an exact recognized empty stage
outside the active task. A non-empty, symlinked, replaced, or invalid stage
blocks the task and is preserved for audited recovery. --recover-stages is the
explicit audited recovery flow for one or more named tasks: it inventories
every non-empty retired stage without following links, records Git identity and
a deterministic content digest in a private receipt, and with --apply archives
the exact stage before it can be cleaned. Ambiguous or changed evidence stays
in place. Run normal cleanup again after recovery has completed.

For one or more specifically named tasks, the implicit age window is zero and
definition of done includes retirement of the source remote branch as well as
the local worktree/branch — so --apply requires --remote whenever WB can still
see that branch on origin. It is the observed branch, not the flag shape, that
decides: a task whose origin branch is already gone (deleted by the merge that
landed it, or by an earlier cleanup) has nothing left to retire and is cleaned
without --remote. --all-merged fleet sweeping keeps the 24-hour merged-PR grace
window unless --older-than overrides it.

--parallel bounds both phases. The inventory walk inspects that many
candidates at once; the apply phase removes that many worktrees at once, one
task per canonical repository. Git allows a single writer per clone, so two
tasks in one repository still take turns and a task spanning several
repositories holds all of them for its whole transaction. The achievable gain
is therefore the size of the largest per-repository group, not the worker
count. Remote branch deletions are bounded more tightly still, against
GitHub's per-account secondary rate limit. --parallel 1 restores the fully
serial apply. Whatever order tasks finish in, the report reads in walk order.

--filter (see the root flag) and named [task...] arguments both narrow which candidates
are inspected at all, before any of the above safety checks run. A malformed
candidate outside that selection is invisible to the run. One inside it is
never fatal: it is reported as a warning and blocks eligibility only for its
own coordinated task, exactly like an unclean or locked sibling would.

--resume-interrupted is a named-task-only recovery authority. It validates the
exact retained .lock as operation=<task> and a positive PID that is
conclusively dead, then holds that descriptor-safe lock through normal cleanup.
Without --apply it is a non-mutating recovery plan; with --apply --remote it
quarantines only the exact held inode and completes the usual terminal flow.

--retire-shells sweeps every task directory instead of inspecting worktrees: a
task whose worktree(s) were already removed by a cleanup that predates the
terminal-namespace-residue fix can be left with an empty owner-namespace
directory and/or a retired operation lock and nothing else. It only ever
retires a task directory it can prove is exactly that — no live checkout
anywhere under it, no live or interrupted lock, no reserved stage entry.
Anything else is left untouched and reported. It cannot be combined with a
task argument or --all-merged, and is itself dry-run by default; --apply is
required to remove anything.`,
		Args: func(command *cobra.Command, args []string) error {
			if recoverStages {
				if len(args) == 0 {
					return fmt.Errorf("--recover-stages requires one or more named tasks")
				}
				if allMerged || retireShells {
					return fmt.Errorf("--recover-stages cannot be combined with --all-merged or --retire-shells")
				}
				return nil
			}
			if retireShells {
				if len(args) != 0 {
					return fmt.Errorf("--retire-shells sweeps every task; it cannot be combined with a task argument")
				}
				if allMerged {
					return fmt.Errorf("--retire-shells and --all-merged cannot be combined")
				}
				return nil
			}
			if len(args) == 0 && !allMerged {
				return fmt.Errorf("supply one or more tasks or use --all-merged")
			}
			if len(args) != 0 && allMerged {
				return fmt.Errorf("tasks and --all-merged cannot be combined")
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			if retireShells {
				outcome, err := worktrees.RetireTaskShells(command.Context(), worktrees.RetireShellsOptions{
					ProjectsRoot: inv.projectsRoot,
					Filter:       inv.filterFlag,
					Apply:        apply,
				})
				if err != nil {
					return err
				}
				switch format {
				case "text":
					return printRetireTaskShells(command, outcome)
				case "json":
					encoder := json.NewEncoder(command.OutOrStdout())
					encoder.SetIndent("", "  ")
					return encoder.Encode(outcome)
				default:
					return fmt.Errorf("unsupported format %q; use text or json", format)
				}
			}
			if recoverStages {
				outcomes := make([]worktrees.RetiredStageRecoveryOutcome, 0, len(args))
				for _, task := range args {
					outcome, err := recoverRetiredStages(command.Context(), worktrees.RetiredStageRecoveryOptions{
						ProjectsRoot: inv.projectsRoot, Task: task, Apply: apply,
					})
					if err != nil {
						return err
					}
					outcomes = append(outcomes, outcome)
				}
				switch format {
				case "text":
					for _, outcome := range outcomes {
						if outcome.ReceiptPath != "" {
							if _, err := fmt.Fprintf(command.OutOrStdout(), "receipt: %s\n", outcome.ReceiptPath); err != nil {
								return err
							}
						}
						for _, result := range outcome.Results {
							state := "preserved"
							if result.Applied {
								state = "archived"
							} else if result.Eligible {
								state = "would archive"
							}
							if _, err := fmt.Fprintf(command.OutOrStdout(), "%-13s %s: %s\n", state, result.Path, result.Reason); err != nil {
								return err
							}
						}
					}
					return nil
				case "json":
					encoder := json.NewEncoder(command.OutOrStdout())
					encoder.SetIndent("", "  ")
					return encoder.Encode(outcomes)
				default:
					return fmt.Errorf("unsupported format %q; use text or json", format)
				}
			}
			tasks := append([]string(nil), args...)
			if len(tasks) != 0 && !command.Flags().Changed("older-than") {
				olderThan = 0
			}
			if resumeInterrupted && len(tasks) != 1 {
				return fmt.Errorf("--resume-interrupted requires one explicit task")
			}
			now := time.Now()
			progress := newInventoryProgress(inv, command.ErrOrStderr(), verbose)
			defer progress.finish()
			outcome, err := cleanupWorktreeTasks(command.Context(), worktrees.CleanupOptions{
				ProjectsRoot:      inv.projectsRoot,
				Tasks:             tasks,
				Base:              base,
				ExplicitBase:      command.Flags().Changed("base"),
				Filter:            inv.filterFlag,
				AbsorbedBy:        absorbedBy,
				SupersededBy:      supersededBy,
				AllMerged:         allMerged,
				Apply:             apply,
				ResumeInterrupted: resumeInterrupted,
				DeleteRemote:      deleteRemote,
				// Finishing a named task must not leave its source branch on
				// origin as backlog. WB decides that from the branch WB can
				// actually see: a task whose origin branch is already gone —
				// deleted by the merge that landed it, or by an earlier
				// cleanup — has nothing left to retire and needs no --remote.
				RequireRemoteRetirement: len(tasks) != 0 && apply,
				OlderThan:               olderThan,
				ReportDir:               reportDir,
				Progress:                progress.report,
				Workers:                 workers,
				Now:                     func() time.Time { return now },
			})
			if err != nil {
				return err
			}
			for _, diagnostic := range outcome.Diagnostics {
				if _, err := fmt.Fprintf(command.ErrOrStderr(), "warning: cleanup skipped malformed candidate in task %s: %s: %s\n", diagnostic.Task, diagnostic.Path, diagnostic.Message); err != nil {
					return err
				}
			}
			for _, quarantined := range outcome.Quarantined {
				if _, err := fmt.Fprintf(command.ErrOrStderr(),
					"warning: cleanup skipped an unusable durable record %s (task %s): %s\n",
					quarantined.Path, quarantined.Task, quarantined.Reason); err != nil {
					return err
				}
			}
			for _, artifact := range quietArtifacts(inv, outcome.Artifacts) {
				if _, err := fmt.Fprintf(command.ErrOrStderr(), "info: cleanup WB internal %s %s: disposition=%s eligible=%t applied=%t reason=%s\n",
					artifact.Kind, artifact.Path, artifact.Disposition, artifact.Eligible, artifact.Applied, artifact.Reason); err != nil {
					return err
				}
			}
			switch format {
			case "text":
				if err := printWorktreeCleanup(command, outcome.Results, apply); err != nil {
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
			if apply && len(tasks) != 0 {
				appliedTasks := appliedCleanupTaskNames(outcome)
				namedApplied := namedCleanupApplySatisfied(tasks, outcome)
				// leakedReleases is wb#321's exit-code seam: see
				// exitNonZeroOnReleaseLeak in remote_autoclaim.go for why it
				// is false today, and flip that one constant, not this
				// loop, to change it.
				var leakedReleases []string
				for _, task := range tasks {
					shouldRelease := appliedTasks[task]
					if !shouldRelease && len(tasks) == 1 && namedApplied {
						// Logical effort resolved to session-resume-* members
						// that applied; the remote claim still uses the
						// operator's original selector.
						shouldRelease = true
					}
					if shouldRelease {
						result := releaseRemoteClaim(inv.projectsRoot, task, outcomeClaimWriter(command, inv.quiet))
						if result.Leaked() {
							leakedReleases = append(leakedReleases, task)
						}
					}
				}
				if len(tasks) == 1 && !namedApplied {
					return fmt.Errorf("task %q was not removed because it did not satisfy cleanup safety", tasks[0])
				}
				if exitNonZeroOnReleaseLeak && len(leakedReleases) > 0 {
					return fmt.Errorf("worktree removed but remote claim release failed for: %s (see wb#321)", strings.Join(leakedReleases, ", "))
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&base, "base", "main", "exact origin target branch required to contain the work; when given, it overrides each worktree's recorded base")
	command.Flags().BoolVar(&allMerged, "all-merged", false, "select every safely merged WB task")
	command.Flags().BoolVar(&apply, "apply", false, "remove eligible worktrees and local branches")
	command.Flags().BoolVar(&resumeInterrupted, "resume-interrupted", false, "recover only this named task's proven-dead interrupted lock before cleanup")
	command.Flags().BoolVar(&deleteRemote, "remote", false, "also delete an unchanged remote branch when applying")
	command.Flags().DurationVar(&olderThan, "older-than", 24*time.Hour, "minimum age of a merged pull request (0 disables)")
	command.Flags().StringVar(&reportDir, "report-dir", "", "cleanup audit directory (default <projects-root>/.wb/reports/worktree-cleanup/<timestamp>)")
	command.Flags().StringVar(&absorbedBy, "absorbed-by", "", "verify work landed inside this merged pull request number or exact landing commit")
	command.Flags().StringVar(&supersededBy, "superseded-by", "", "use an explicit trusted-reviewer receipt to retire an intentionally superseded split branch")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&retireShells, "retire-shells", false, "sweep every task for an empty pre-existing shell (owner directory and/or retired lock, no live checkout) and retire it")
	command.Flags().BoolVar(&recoverStages, "recover-stages", false, "audit and privately archive non-empty retired stages for named tasks")
	// --parallel is the fleet-wide name for this ceiling: six other commands
	// already spell it that way, and "workers" reads as a second noun beside
	// WB's own tasks. --workers/-j stays as a hidden deprecated alias so
	// existing scripts and muscle memory keep working.
	command.Flags().IntVar(&workers, "parallel", worktrees.DefaultInspectWorkers, "maximum repositories to inspect, and to apply, concurrently")
	command.Flags().IntVarP(&workers, "workers", "j", worktrees.DefaultInspectWorkers, "maximum repositories to inspect, and to apply, concurrently")
	_ = command.Flags().MarkDeprecated("workers", "use --parallel instead")
	command.Flags().BoolVarP(&verbose, "verbose", "v", false, "stream per-candidate inspection progress to stderr, even when not on a terminal (--quiet wins)")
	setDiscoveryTerms(command, "cleanup clean up retire remove landed merged worktrees branches tasks")
	markQuietVerb(command)
	return command
}

// appliedCleanupTaskNames collects physical task identities Cleanup actually
// retired. Session-resume members keep their on-disk namespace here, not the
// logical effort an operator typed.
func appliedCleanupTaskNames(outcome worktrees.CleanupOutcome) map[string]bool {
	applied := make(map[string]bool, len(outcome.Results)+len(outcome.Artifacts))
	for _, result := range outcome.Results {
		if result.Applied {
			applied[result.Task] = true
		}
	}
	for _, artifact := range outcome.Artifacts {
		if artifact.Applied {
			applied[artifact.Task] = true
		}
	}
	return applied
}

// namedCleanupApplySatisfied reports whether a single named --apply cleanup
// retired every identity Cleanup resolved from that selector. The library
// expands a logical effort to session-resume-* directories before it mutates;
// judging the pre-resolution argv is how a successful resume cleanup used to
// exit 1 after worktree_gone and branch_deleted were already true (wb#265).
func namedCleanupApplySatisfied(requested []string, outcome worktrees.CleanupOutcome) bool {
	if len(requested) != 1 {
		return true
	}
	applied := appliedCleanupTaskNames(outcome)
	names := outcome.ResolvedTasks
	if len(names) == 0 {
		names = requested
	}
	if len(names) == 0 {
		return false
	}
	for _, name := range names {
		if !applied[name] {
			return false
		}
	}
	return true
}

func printRetireTaskShells(command *cobra.Command, outcome worktrees.RetireShellsOutcome) error {
	out := command.OutOrStdout()
	if len(outcome.Results) == 0 {
		_, err := fmt.Fprintln(out, "no WB task directories found")
		return err
	}
	for _, result := range outcome.Results {
		switch {
		case result.Applied:
			if _, err := fmt.Fprintf(out, "retired      %s %s\n", result.Task, result.Path); err != nil {
				return err
			}
		case result.Eligible:
			if _, err := fmt.Fprintf(out, "would retire %s %s\n", result.Task, result.Path); err != nil {
				return err
			}
		case result.Error != "":
			if _, err := fmt.Fprintf(out, "failed       %s %s: %s\n", result.Task, result.Path, result.Error); err != nil {
				return err
			}
		default:
			if _, err := fmt.Fprintf(out, "skip         %s %s: %s\n", result.Task, result.Path, result.Reason); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	if outcome.Apply {
		_, err := fmt.Fprintf(out, "%d retired\n", outcome.Totals["retired"])
		return err
	}
	_, err := fmt.Fprintf(out, "%d would retire\n", outcome.Totals["would_retire"])
	return err
}

func newWorktreeRenameCmd(inv *invocation) *cobra.Command {
	command := cmdworktree.NewRename(newCLIRuntime(inv), cmdworktree.RenameDependencies{Run: worktrees.Rename, Admit: requireMutationAdmission, AfterApply: func(command *cobra.Command, base string, results []worktrees.RenameResult) {
		markRenamedCheckouts(inv, command, base, results)
	}})
	addMutationAdmissionFlags(command)
	return command
}

func requireOutputFormat(value string, allowed ...string) error {
	return shared.RequireOutputFormat(value, allowed...)
}

func printWorktreeCleanup(command *cobra.Command, results []worktrees.CleanupResult, apply bool) error {
	if len(results) == 0 {
		_, err := fmt.Fprintln(command.OutOrStdout(), "no WB worktrees matched")
		return err
	}
	eligible, removed := 0, 0
	for _, result := range results {
		switch {
		case result.Applied:
			removed++
			remote := ""
			if result.RemoteDeleted {
				remote = " and remote branch"
			}
			// Say when WB, not Git, deleted the checkout. The task finished
			// either way, and an operator reading this line is the person who
			// would otherwise have found the directory still on disk.
			residue := ""
			if result.WorktreeResidueRemoved {
				residue = " (WB removed the checkout Git unregistered but could not delete)"
			}
			if _, err := fmt.Fprintf(command.OutOrStdout(), "removed %s %s%s%s%s\n", result.Task, result.Repository, remote, residue, cleanupProofNote(result)); err != nil {
				return err
			}
		case result.Eligible:
			eligible++
			if _, err := fmt.Fprintf(command.OutOrStdout(), "would remove %s %s%s\n", result.Task, result.Repository, cleanupProofNote(result)); err != nil {
				return err
			}
		default:
			if _, err := fmt.Fprintf(command.OutOrStdout(), "skip %s %s: %s\n", result.Task, result.Repository, result.Reason); err != nil {
				return err
			}
		}
	}
	if apply {
		_, err := fmt.Fprintf(command.OutOrStdout(), "%d removed\n", removed)
		return err
	}
	_, err := fmt.Fprintf(command.OutOrStdout(), "%d eligible; dry-run only, pass --apply to remove\n", eligible)
	return err
}

// cleanupProofNote names the target that proved a candidate's work landed when
// that is not the base recorded for it, so the operator reads what authorized
// the removal on the same line that announces it.
func cleanupProofNote(result worktrees.CleanupResult) string {
	if result.IntegrationProof == "" {
		return ""
	}
	return " (" + result.IntegrationProof + ")"
}

func worktreeJournalOperations() cmdworktree.JournalOperations {
	return cmdworktree.JournalOperations{Load: worktrees.LoadWorkLogView, Show: worktrees.LogShow, Init: worktrees.LogInit, Steer: worktrees.LogSteer, Checkpoint: worktrees.LogCheckpoint, Refresh: worktrees.LogRefresh, Integrate: worktrees.LogIntegrate, Handoff: worktrees.LogHandoff, Recover: worktrees.LogRecover, Finalize: worktrees.LogFinalize, Sync: worktrees.LogSync, Archive: worktrees.LogArchive}
}
