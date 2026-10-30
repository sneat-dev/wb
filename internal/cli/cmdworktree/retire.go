package cmdworktree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

type InventoryProgress struct {
	Report func(worktrees.ListProgress)
	Finish func()
}

// RetireDependencies are the transactions `wb worktree retire` drives.
type RetireDependencies struct {
	// Run is the cleanup transaction for landed work.
	Run      func(context.Context, worktrees.CleanupOptions) (worktrees.CleanupOutcome, error)
	Recover  func(context.Context, worktrees.RetiredStageRecoveryOptions) (worktrees.RetiredStageRecoveryOutcome, error)
	Progress func(*cobra.Command, bool) InventoryProgress
	// Root applies its existing fatal-release-leak policy; remote engine outcomes
	// remain authoritative and are never reconstructed by this CLI adapter.
	Release func(*cobra.Command, string, string) bool
	// Resolve maps a name that is not a task to the task whose checkout has
	// it as its branch ("" when none does).
	Resolve func(ctx context.Context, projectsRoot, filter, name string) (string, error)
	// Branch retires a bare branch ref that no checkout holds.
	Branch func(context.Context, worktrees.BranchCleanupOptions) (worktrees.BranchCleanupOutcome, error)
	// Preserve is the --preserve transaction for unlanded work.
	Preserve PreserveDependencies
}

// PreserveDependencies drive retirement of work that never landed: it is
// committed, published as a retired/* ref and archived before removal.
type PreserveDependencies struct {
	Run                 func(context.Context, worktrees.RetireOptions) (worktrees.RetireResult, error)
	Admit               func(*cobra.Command, bool) (worktrees.AgentIdentity, func(), error)
	CheckOwnership      func(context.Context, string, string) error
	ReleaseWhenComplete func(*cobra.Command, string, string) bool
}

// NewRetire constructs `wb worktree retire`, the per-item transaction.
func NewRetire(runtime shared.Runtime, deps RetireDependencies) *cobra.Command {
	var base, format, reportDir, absorbedBy, supersededBy, preserve, message, repository string
	var apply, deleteRemote, resumeInterrupted, recoverStages, verbose, jsonShortcut, allowResidue bool
	var peerEvidence, requireHosts []string
	var olderThan time.Duration
	var workers int
	command := &cobra.Command{
		Use:   "retire <task|branch>...",
		Short: "Retire one finished task or branch: remove its worktrees and refs on landing evidence",
		Long: `Plan or apply retirement of named WB tasks: their worktrees, local branches
and, with --remote, their origin branches. The default is a dry run. For every
finished item across the fleet at once, use wb clean.

A name that is not a task is resolved as a branch: the task whose checkout
has that branch is retired (task batch5-assembly for branch batch5), and a
branch no checkout holds is retired as a bare ref on the same landing
evidence wb clean uses (--repo narrows it to one repository).

--preserve branch|tag retires one task whose work did NOT land: it commits
tracked and nonignored untracked changes on the branch with normal Git hooks
(--message names the commit), publishes the exact commit as a retired/*
branch or refs/tags/retired/* tag, pushes the Work Log and checkout metadata
to the configured private organization retirement repository, and only after
both remote receipts verify deletes the old remote ref with an exact lease and
removes the checkout. An open pull request, changed remote ref, competing
claim, or unavailable or public retirement repository refuses it; retry the
same command to resume. A coordinated task is preserved one repository at a
time with --filter.

Without --preserve, retirement requires landing evidence:

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
without --remote.

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
`,
		Args: func(command *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("supply one or more tasks or branches; wb clean retires every finished item")
			}
			if recoverStages && preserve != "" {
				return fmt.Errorf("--recover-stages and --preserve cannot be combined")
			}
			if preserve != "" && len(args) != 1 {
				return fmt.Errorf("--preserve retires exactly one task")
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			if jsonShortcut {
				format = "json"
			}
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			if preserve != "" {
				return runPreserve(command, runtime, deps.Preserve, args[0], preserve, message, apply, format)
			}
			flags := runtime.Flags()
			if recoverStages {
				outcomes := make([]worktrees.RetiredStageRecoveryOutcome, 0, len(args))
				for _, task := range args {
					outcome, err := deps.Recover(command.Context(), worktrees.RetiredStageRecoveryOptions{
						ProjectsRoot: flags.ProjectsRoot, Task: task, Apply: apply,
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
			progress := deps.Progress(command, verbose)
			defer progress.Finish()
			run := func(tasks []string) (worktrees.CleanupOutcome, error) {
				return deps.Run(command.Context(), worktrees.CleanupOptions{
					ProjectsRoot:      flags.ProjectsRoot,
					Tasks:             tasks,
					Base:              base,
					ExplicitBase:      command.Flags().Changed("base"),
					Filter:            flags.Filter,
					AbsorbedBy:        absorbedBy,
					AllowResidue:      allowResidue,
					SupersededBy:      supersededBy,
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
					Progress:                progress.Report,
					Workers:                 workers,
					Now:                     func() time.Time { return now },
				})
			}
			outcome, err := run(tasks)
			for attempt := 0; err != nil && attempt < len(tasks); attempt++ {
				var notFound *worktrees.TaskNotFoundError
				if !errors.As(err, &notFound) {
					break
				}
				task, resolveErr := deps.Resolve(command.Context(), flags.ProjectsRoot, flags.Filter, notFound.Task)
				if resolveErr != nil {
					return resolveErr
				}
				if task == "" {
					if len(tasks) != 1 {
						return fmt.Errorf("%w; it is not a checkout's branch either. Retire a bare branch on its own: wb worktree retire %s", err, notFound.Task)
					}
					return runBareBranchRetire(command, runtime, deps.Branch, worktrees.BranchCleanupOptions{
						ProjectsRoot: flags.ProjectsRoot, Base: base, Filter: flags.Filter, Repository: repository,
						Branch: notFound.Task, Apply: apply, OlderThan: olderThan, Receipts: true,
						AbsorbedBy: absorbedBy, SupersededBy: supersededBy, ReportDir: reportDir,
						Scope: bareBranchScope(deleteRemote), PeerEvidence: peerEvidence, RequireHosts: requireHosts,
					}, format)
				}
				if _, writeErr := fmt.Fprintf(command.ErrOrStderr(), "note: %s is the branch of task %s; retiring task %s\n", notFound.Task, task, task); writeErr != nil {
					return writeErr
				}
				for index := range tasks {
					if tasks[index] == notFound.Task {
						tasks[index] = task
					}
				}
				outcome, err = run(tasks)
			}
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
			for _, artifact := range quietArtifacts(flags.Quiet, outcome.Artifacts) {
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
						if deps.Release(command, flags.ProjectsRoot, task) {
							leakedReleases = append(leakedReleases, task)
						}
					}
				}
				if len(tasks) == 1 && !namedApplied {
					return fmt.Errorf("task %q was not removed because it did not satisfy cleanup safety", tasks[0])
				}
				if len(leakedReleases) > 0 {
					return fmt.Errorf("worktree removed but remote claim release failed for: %s (see wb#321)", strings.Join(leakedReleases, ", "))
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&base, "base", "main", "exact origin target branch required to contain the work; when given, it overrides each worktree's recorded base")
	command.Flags().BoolVar(&apply, "apply", false, "retire eligible worktrees and branches instead of planning")
	command.Flags().BoolVar(&resumeInterrupted, "resume-interrupted", false, "recover only this named task's proven-dead interrupted lock before cleanup")
	command.Flags().BoolVar(&deleteRemote, "remote", false, "also delete an unchanged remote branch when applying")
	command.Flags().DurationVar(&olderThan, "older-than", 24*time.Hour, "minimum age of a merged pull request (0 disables)")
	command.Flags().StringVar(&reportDir, "report-dir", "", "retirement audit directory (default <projects-root>/.wb/reports/worktree-cleanup/<timestamp>)")
	command.Flags().StringVar(&absorbedBy, "absorbed-by", "", "verify work landed inside this merged pull request number or exact landing commit")
	command.Flags().StringVar(&supersededBy, "superseded-by", "", "use an explicit trusted-reviewer receipt to retire an intentionally superseded split branch")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().StringVar(&preserve, "preserve", "", "retire one task whose work did not land, preserving it as a retired/* branch or tag: branch or tag")
	command.Flags().StringVarP(&message, "message", "m", "", "with --preserve: source commit message when changes remain")
	command.Flags().StringVar(&repository, "repo", "", "exact owner/name for a bare branch that no checkout holds")
	command.Flags().BoolVar(&jsonShortcut, "json", false, "shorthand for --format=json")
	command.Flags().BoolVar(&allowResidue, "allow-residue", false, "also retire a landed checkout holding local commits past the landed head, discarding them")
	command.Flags().StringArrayVar(&peerEvidence, "peer-evidence", nil, "bare branch with --superseded-by --remote: machine-generated exact wb branch list JSON from one required host; repeat per host")
	command.Flags().StringArrayVar(&requireHosts, "require-host", nil, "bare branch with --superseded-by --remote: host ID required in peer evidence; repeat per host")
	command.Flags().BoolVar(&recoverStages, "recover-stages", false, "audit and privately archive non-empty retired stages for named tasks")
	// --parallel is the fleet-wide name for this ceiling: six other commands
	// already spell it that way, and "workers" reads as a second noun beside
	// WB's own tasks. --workers/-j stays as a hidden deprecated alias so
	// existing scripts and muscle memory keep working.
	command.Flags().IntVar(&workers, "parallel", worktrees.DefaultInspectWorkers, "maximum repositories to inspect, and to apply, concurrently")
	command.Flags().IntVarP(&workers, "workers", "j", worktrees.DefaultInspectWorkers, "maximum repositories to inspect, and to apply, concurrently")
	_ = command.Flags().MarkDeprecated("workers", "use --parallel instead")
	command.Flags().BoolVarP(&verbose, "verbose", "v", false, "stream per-candidate inspection progress to stderr, even when not on a terminal (--quiet wins)")
	return command
}
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

func namedCleanupApplySatisfied(requested []string, outcome worktrees.CleanupOutcome) bool {
	if len(requested) != 1 {
		return true
	}
	applied := appliedCleanupTaskNames(outcome)
	names := outcome.ResolvedTasks
	if len(names) == 0 {
		names = requested
	}
	for _, name := range names {
		if !applied[name] {
			return false
		}
	}
	return true
}

func quietArtifacts(quiet bool, artifacts []worktrees.LifecycleArtifact) []worktrees.LifecycleArtifact {
	if !quiet {
		return artifacts
	}
	var applied []worktrees.LifecycleArtifact
	for _, artifact := range artifacts {
		if artifact.Applied {
			applied = append(applied, artifact)
		}
	}
	return applied
}

// runPreserve retires one task whose work did not land, preserving it in
// retired refs and the private retirement repository before removal.
func runPreserve(command *cobra.Command, runtime shared.Runtime, deps PreserveDependencies, task, preserve, message string, apply bool, format string) error {
	_, release, err := deps.Admit(command, apply)
	if err != nil {
		return err
	}
	defer release()
	flags := runtime.Flags()
	result, err := deps.Run(command.Context(), worktrees.RetireOptions{
		ProjectsRoot: flags.ProjectsRoot, Task: task, Repository: flags.Filter,
		Message: message, Preserve: preserve, Apply: apply,
		RemoteOwnership: func(ctx context.Context, task string) error {
			return deps.CheckOwnership(ctx, flags.ProjectsRoot, task)
		},
	})
	if err != nil {
		return err
	}
	releaseLeaked := apply && result.Phase == "complete" && deps.ReleaseWhenComplete(command, flags.ProjectsRoot, task)
	if format == "json" {
		err = json.NewEncoder(command.OutOrStdout()).Encode(result)
	} else {
		_, err = fmt.Fprintf(command.OutOrStdout(), "%s %s %s -> %s (archive %s, phase %s)\n", result.Task, result.Repository, result.Branch, result.RetiredRef, result.ArchiveRef, result.Phase)
	}
	if err != nil {
		return err
	}
	if releaseLeaked {
		return fmt.Errorf("task %q retirement completed but remote claim release failed", task)
	}
	return nil
}

func bareBranchScope(deleteRemote bool) string {
	if deleteRemote {
		return worktrees.BranchScopeAll
	}
	return worktrees.BranchScopeLocal
}

// runBareBranchRetire retires a branch ref no checkout holds, on the same
// evidence wb clean uses for branches.
func runBareBranchRetire(command *cobra.Command, runtime shared.Runtime, run func(context.Context, worktrees.BranchCleanupOptions) (worktrees.BranchCleanupOutcome, error), options worktrees.BranchCleanupOptions, format string) error {
	outcome, err := run(command.Context(), options)
	if err != nil {
		return err
	}
	if len(outcome.Results) == 0 {
		hint := ""
		if options.Scope == worktrees.BranchScopeLocal {
			hint = "; a branch that exists only on origin needs --remote"
		}
		return runtime.ExitError(shared.ExitFindings, fmt.Sprintf("%q is not a WB task, a checkout's branch, or a branch ref in the selected repositories%s", options.Branch, hint))
	}
	out := &stickyWriter{out: command.OutOrStdout()}
	if format == "json" {
		encoder := json.NewEncoder(command.OutOrStdout())
		encoder.SetIndent("", "  ")
		out.err = encoder.Encode(outcome)
	}
	kept := 0
	for _, result := range outcome.Results {
		state := "keep"
		switch {
		case result.Applied:
			state = "retired"
		case result.Eligible:
			state = "would retire"
		default:
			kept++
		}
		if format != "json" {
			out.printf("%s %s %s %s: %s\n", state, result.Repository, result.Scope, result.Branch, firstNonEmpty(result.Error, result.SkipReason, result.Evidence))
		}
	}
	if out.err != nil {
		return out.err
	}
	if kept > 0 {
		return runtime.ExitError(shared.ExitFindings, fmt.Sprintf("branch %s was kept; the row above names the reason", options.Branch))
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
