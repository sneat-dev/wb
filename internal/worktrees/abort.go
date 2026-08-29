package worktrees

import (
	"context"
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// AbortDisposition makes an unfinished effort legible instead of leaving an
// ambiguous directory behind. Handoff and not_landed retain the worktree for a
// later claim; discarded is the only disposition that removes local Git state.
type AbortDisposition string

const (
	AbortHandoff   AbortDisposition = "handoff"
	AbortNotLanded AbortDisposition = "not_landed"
	AbortDiscarded AbortDisposition = "discarded"
	AbortOrphaned  AbortDisposition = "orphaned"
)

type AbortOptions struct {
	ProjectsRoot string
	Task         string
	Base         string
	// Filter narrows which repositories within the coordinated task are
	// inspected further than today's cheap local Git state, preflighted, and
	// mutated — the same "owner/repository slug (or worktree path) contains
	// this substring" semantics as ListOptions.Filter and the root --filter
	// flag. An empty Filter matches everything, preserving today's
	// all-repositories, all-or-nothing behavior exactly. A repository Filter
	// excludes is reported via AbortResult.Excluded rather than dropped
	// silently, its own ineligibility (if any) never blocks the repositories
	// Filter did select, and it is left completely untouched: the task
	// remains non-terminal until a later abort call resolves it too.
	Filter string
	// All acknowledges that this invocation intentionally applies a terminal
	// disposition to every member of a coordinated task. Multi-repository
	// tasks otherwise require an explicit member filter.
	All         bool
	Disposition AbortDisposition
	Successor   string
	// ClaimID, Actor, and Reason form the explicit authority boundary for an
	// orphaned terminal record. Orphaned claims have no live checkout from
	// which task/repository identity can be reconstructed, so apply always
	// binds one exact immutable claim rather than guessing from a task name.
	ClaimID string
	Actor   string
	Reason  string
	// SuccessorIdentity is the caller's explicit execution identity declaration
	// for the claim created by an applied handoff/not_landed transition.
	SuccessorIdentity ClaimExecutionIdentity
	DeleteRemote      bool
	Apply             bool
	// beforeAbortRemoval is a test-only race seam immediately before the
	// destructive reinspection. A concurrent writer must make abort refuse,
	// never make its new work disappear.
	beforeAbortRemoval func(worktree string)
	// afterAbortWorktreeRemoval simulates interruption after the linked
	// checkout is gone but before its exact local branch is retired.
	afterAbortWorktreeRemoval func(worktree string) error
	// beforeOrphanSeal is a test-only race seam after the read-only plan and
	// claim lock acquisition but before every absence predicate is reread.
	beforeOrphanSeal func()
}

type AbortResult struct {
	ListResult
	Disposition AbortDisposition `json:"disposition"`
	Successor   string           `json:"successor,omitempty"`
	Eligible    bool             `json:"eligible"`
	// Excluded marks a repository that AbortOptions.Filter left out of this
	// run. It is never preflighted or mutated regardless of Eligible, and
	// recording it here — rather than omitting it — is what lets a filtered
	// abort report precisely which repositories still remain unresolved.
	Excluded      bool   `json:"excluded,omitempty"`
	Applied       bool   `json:"applied"`
	WorktreeGone  bool   `json:"worktree_gone"`
	BranchDeleted bool   `json:"branch_deleted"`
	RemoteDeleted bool   `json:"remote_deleted"`
	BacklogID     string `json:"backlog_id,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// Abort seals every Work Log in a coordinated task. It is the deliberate
// escape hatch for unused or interrupted claims which cannot meet the merged
// PR evidence required by Cleanup. --apply never destroys resumable work:
// only an explicit discarded disposition removes a clean linked checkout and
// its exact local branch ref. The private archive/outbox is written first.
func Abort(ctx context.Context, options AbortOptions) ([]AbortResult, error) {
	projectsRoot, task, base, _, err := normalizeListOptions(ListOptions{ProjectsRoot: options.ProjectsRoot, Task: options.Task, Base: options.Base})
	if err != nil {
		return nil, err
	}
	// Every later preflight and cleanup path must use the same normalized root
	// as ListWithDiagnostics. On macOS, keeping a caller's /var spelling here
	// while Git reports /private/var made a valid canonical clone look outside
	// the projects root during the second abort pass.
	options.ProjectsRoot = projectsRoot
	filter := strings.TrimSpace(options.Filter)
	if task == "" {
		return nil, fmt.Errorf("task is required")
	}
	if options.Disposition != AbortHandoff && options.Disposition != AbortNotLanded && options.Disposition != AbortDiscarded && options.Disposition != AbortOrphaned {
		return nil, fmt.Errorf("disposition must be handoff, not_landed, discarded, or orphaned")
	}
	options.Successor = strings.TrimSpace(options.Successor)
	terminalWithoutSuccessor := options.Disposition == AbortDiscarded || options.Disposition == AbortOrphaned
	if !terminalWithoutSuccessor && (options.Successor == "" || len(options.Successor) > 200 || strings.ContainsAny(options.Successor, "\x00\r\n")) {
		return nil, fmt.Errorf("--successor is required exactly once for %s", options.Disposition)
	}
	if terminalWithoutSuccessor && options.Successor != "" {
		return nil, fmt.Errorf("--successor cannot be used with %s", options.Disposition)
	}
	identitySupplied := strings.TrimSpace(options.SuccessorIdentity.Model) != "" || strings.TrimSpace(options.SuccessorIdentity.CLI) != "" || strings.TrimSpace(options.SuccessorIdentity.Provider) != ""
	if !terminalWithoutSuccessor && (options.Apply || identitySupplied) {
		if err := validateNewExecutionIdentity(options.SuccessorIdentity); err != nil {
			return nil, err
		}
	} else if terminalWithoutSuccessor && identitySupplied {
		return nil, fmt.Errorf("--model, --cli, and --provider cannot be used with %s", options.Disposition)
	}
	if options.Apply && options.Disposition == AbortDiscarded && !options.DeleteRemote {
		return nil, fmt.Errorf("discarded abort requires remote branch retirement; rerun with --remote")
	}
	if options.Disposition == AbortOrphaned {
		return abortOrphanedClaim(ctx, options)
	}
	if strings.TrimSpace(options.ClaimID) != "" || strings.TrimSpace(options.Actor) != "" || strings.TrimSpace(options.Reason) != "" {
		return nil, fmt.Errorf("--claim, --actor, and --reason are valid only with the orphaned disposition")
	}
	resolution, err := wbhome.Resolve(projectsRoot)
	if err != nil {
		return nil, err
	}
	var backlog []lifecycleBacklogRecord
	if options.Disposition == AbortDiscarded {
		recognizedWorktreesRoots := make([]string, 0, len(resolution.Read))
		for _, layout := range resolution.Read {
			recognizedWorktreesRoots = append(recognizedWorktreesRoots, layout.WorktreesRoot)
		}
		backlog, err = loadResumableLifecycleBacklog(ctx, resolution.Write.Home, projectsRoot, recognizedWorktreesRoots, taskSelectionSet([]string{task}), "", string(AbortDiscarded))
		if err != nil {
			return nil, err
		}
	}
	listed, err := ListWithDiagnostics(ctx, ListOptions{ProjectsRoot: projectsRoot, Task: task, Base: base})
	if err != nil {
		return nil, err
	}
	if len(listed.Results) == 0 && len(backlog) == 0 {
		return nil, fmt.Errorf("WB worktree task %q was not found", task)
	}
	results := make([]AbortResult, len(listed.Results))
	for i, entry := range listed.Results {
		eligible := !entry.Locked && (options.Disposition != AbortDiscarded || entry.Clean)
		reason := ""
		if entry.Locked {
			// Abort has no recovery authority of its own; point at the one
			// command that does rather than leaving a dead lock unexplained.
			reason = lockedReason(entry, resumeInterruptedCommand(task))
		} else if !entry.Clean && options.Disposition == AbortDiscarded {
			reason = "discarded worktree has local changes; use handoff/not_landed or checkpoint it"
		}
		excluded := abortRepositoryExcludedByFilter(filter, entry.Repository, entry.WorktreeDir)
		results[i] = AbortResult{ListResult: entry, Disposition: options.Disposition, Successor: options.Successor, Eligible: eligible, Excluded: excluded, Reason: reason}
	}
	for _, record := range backlog {
		excluded := abortRepositoryExcludedByFilter(filter, record.Repository, record.WorktreeDir)
		results = append(results, AbortResult{ListResult: ListResult{
			Task: record.Task, Repository: record.Repository, CanonicalDir: record.CanonicalDir,
			WorktreeDir: record.WorktreeDir, WorktreesRoot: record.WorktreesRoot,
			Branch: record.Branch, Base: record.Base, HeadSHA: record.HeadSHA,
			External: record.External,
		}, Disposition: AbortDiscarded, Eligible: true, Excluded: excluded, WorktreeGone: true,
			BacklogID: record.ID, Reason: "durable cleanup backlog awaiting exact local branch retirement"})
	}
	if len(results) > 1 && filter == "" && !options.All {
		return results, fmt.Errorf("task %q has %d repositories; select a member with --filter or acknowledge every member with --all", task, len(results))
	}
	// A malformed candidate outside the active --filter selection describes a
	// repository this run already leaves untouched; it must never block the
	// repositories --filter did select. An empty filter matches everything,
	// so this reduces to today's "any diagnostic blocks every result" rule.
	if diagnosticPath := firstFilterMatchingDiagnosticPath(filter, listed.Diagnostics); diagnosticPath != "" {
		for i := range results {
			if results[i].Excluded {
				continue
			}
			results[i].Eligible = false
			results[i].Reason = "task has malformed worktree candidate: " + diagnosticPath
		}
	}
	if !options.Apply {
		return results, nil
	}
	for _, result := range results {
		if result.Excluded {
			continue
		}
		if !result.Eligible {
			return results, fmt.Errorf("task %q cannot be aborted safely: %s", task, result.Reason)
		}
	}
	for backlogIndex := range backlog {
		record := &backlog[backlogIndex]
		if abortRepositoryExcludedByFilter(filter, record.Repository, record.WorktreeDir) {
			continue
		}
		if err := resumeLifecycleBacklog(ctx, resolution.Write.Home, record, false); err != nil {
			return results, err
		}
		for resultIndex := range results {
			if results[resultIndex].BacklogID == record.ID {
				results[resultIndex].Applied = true
				results[resultIndex].BranchDeleted = true
				results[resultIndex].Reason = "resumed durable cleanup backlog"
			}
		}
	}
	if len(listed.Results) == 0 {
		return results, nil
	}
	liveScopeEmpty := true
	for i := range listed.Results {
		if !results[i].Excluded {
			liveScopeEmpty = false
			break
		}
	}
	if liveScopeEmpty {
		// --filter selected nothing live to touch (backlog resumption above,
		// if any, already ran); never contend for the task lock for a run that
		// mutates nothing.
		return results, nil
	}
	taskHandle, err := acquireCleanupTaskAt(results[0].WorktreesRoot, results[0].Task)
	if err != nil {
		return results, err
	}
	defer func() {
		_ = taskHandle.lock.release()
		taskHandle.close()
	}()
	// Corroborate every live checkout, exact remote source ref, and private
	// claim before the first terminal write/removal. This is coordinated
	// preflight: a bad second repository cannot be discovered after the first
	// one has already been destroyed.
	for i := range listed.Results {
		if results[i].Excluded {
			continue
		}
		refreshed, remoteHead, preflightErr := preflightAbortRepository(ctx, projectsRoot, options, taskHandle, results[i], resolution.Write.Home)
		if preflightErr != nil {
			return results, preflightErr
		}
		results[i].ListResult = refreshed
		results[i].RemoteHeadSHA = remoteHead
	}
	for i := range listed.Results {
		if results[i].Excluded {
			continue
		}
		result := &results[i]
		if options.Disposition == AbortDiscarded {
			if err := applyDiscardedAbort(ctx, projectsRoot, options, taskHandle, resolution.Write.Home, result); err != nil {
				return results, err
			}
		} else if err := transferWorkLogClaim(resolution.Write.Home, result.WorktreeDir, result.HeadSHA, string(options.Disposition), options.Successor, options.SuccessorIdentity); err != nil {
			return results, fmt.Errorf("transfer resumable work log for %s: %w", result.Repository, err)
		}
		result.Applied = true
	}
	return results, nil
}

func preflightAbortRepository(
	ctx context.Context,
	projectsRoot string,
	options AbortOptions,
	task *cleanupTaskHandle,
	result AbortResult,
	home string,
) (ListResult, string, error) {
	worktree, err := openCleanupWorktree(task, CleanupResult{ListResult: result.ListResult})
	if err != nil {
		return ListResult{}, "", err
	}
	defer worktree.close()
	if err := worktree.validate(); err != nil {
		return ListResult{}, "", err
	}
	refreshed, err := inspectLifecycleWorktree(
		ctx,
		projectsRoot,
		wbhome.Layout{WorktreesRoot: result.WorktreesRoot},
		result.Task,
		result.WorktreeDir,
		result.Base,
		"", // Abort discards work outright; no landing receipt applies.
		false,
		false,
		result.External,
	)
	if err != nil {
		return ListResult{}, "", fmt.Errorf("preflight abort %s: %w", result.Repository, err)
	}
	if err := worktree.validate(); err != nil {
		return ListResult{}, "", err
	}
	if refreshed.HeadSHA != result.HeadSHA || refreshed.Branch != result.Branch || refreshed.Repository != result.Repository {
		return ListResult{}, "", fmt.Errorf("abort safety changed for %s: checkout identity or branch head moved", result.Repository)
	}
	if options.Disposition == AbortDiscarded && !refreshed.Clean {
		return ListResult{}, "", fmt.Errorf("abort safety changed for %s: discarded worktree has local changes", result.Repository)
	}
	canonical, err := openCanonicalRepository(refreshed.CanonicalDir)
	if err != nil {
		return ListResult{}, "", fmt.Errorf("open abort canonical repository %s: %w", refreshed.CanonicalDir, err)
	}
	defer canonical.close()
	if err := canonical.validate(); err != nil {
		return ListResult{}, "", err
	}
	remoteHead := ""
	if options.Disposition == AbortDiscarded {
		remoteHead, err = remoteBranchHead(ctx, refreshed.CanonicalDir, refreshed.Branch)
		if err != nil {
			return ListResult{}, "", fmt.Errorf("inspect remote branch before discarding %s: %w", refreshed.Repository, err)
		}
		if remoteHead != "" && remoteHead != refreshed.HeadSHA {
			return ListResult{}, "", fmt.Errorf("refuse to discard %s: origin/%s is %s, expected exact local head %s", refreshed.Repository, refreshed.Branch, remoteHead, refreshed.HeadSHA)
		}
	}
	if err := preflightWorkLogSeal(home, refreshed.WorktreeDir, refreshed.HeadSHA); err != nil {
		return ListResult{}, "", fmt.Errorf("preflight aborted Work Log for %s: %w", refreshed.Repository, err)
	}
	return refreshed, remoteHead, nil
}

func applyDiscardedAbort(
	ctx context.Context,
	projectsRoot string,
	options AbortOptions,
	task *cleanupTaskHandle,
	home string,
	result *AbortResult,
) error {
	worktree, err := openCleanupWorktree(task, CleanupResult{ListResult: result.ListResult})
	if err != nil {
		return err
	}
	defer worktree.close()
	if options.beforeAbortRemoval != nil {
		options.beforeAbortRemoval(result.WorktreeDir)
	}
	if err := worktree.validate(); err != nil {
		return err
	}
	// This is deliberately at the last destructive boundary. `git worktree
	// remove` also refuses dirty state without --force, providing a second
	// independent guard if a non-WB writer races after this inspection.
	refreshed, err := inspectLifecycleWorktree(
		ctx,
		projectsRoot,
		wbhome.Layout{WorktreesRoot: result.WorktreesRoot},
		result.Task,
		result.WorktreeDir,
		result.Base,
		"",
		false,
		false,
		result.External,
	)
	if err != nil {
		return fmt.Errorf("recheck discarded worktree %s: %w", result.Repository, err)
	}
	if !refreshed.Clean || refreshed.HeadSHA != result.HeadSHA || refreshed.Branch != result.Branch {
		return fmt.Errorf("abort safety changed for %s immediately before removal: worktree is dirty or branch head moved", result.Repository)
	}
	if err := worktree.validate(); err != nil {
		return err
	}
	remoteHead, err := remoteBranchHead(ctx, refreshed.CanonicalDir, refreshed.Branch)
	if err != nil {
		return fmt.Errorf("recheck remote branch before discarding %s: %w", refreshed.Repository, err)
	}
	if remoteHead != result.RemoteHeadSHA || (remoteHead != "" && remoteHead != refreshed.HeadSHA) {
		return fmt.Errorf("abort safety changed for %s: remote branch moved from %q to %q", refreshed.Repository, result.RemoteHeadSHA, remoteHead)
	}
	canonical, err := openCanonicalRepository(refreshed.CanonicalDir)
	if err != nil {
		return fmt.Errorf("open abort canonical repository %s: %w", refreshed.CanonicalDir, err)
	}
	defer canonical.close()
	if err := canonical.validate(); err != nil {
		return err
	}
	// The private archive/outbox is durable before remote or local Git state
	// is retired. A failed later step is therefore a visible cleanup backlog,
	// never an evidence-free disappearance.
	if err := sealWorkLogForRecycle(home, refreshed.WorktreeDir, refreshed.HeadSHA, string(options.Disposition)); err != nil {
		return fmt.Errorf("seal discarded work log for %s: %w", refreshed.Repository, err)
	}
	backlogRecord := newLifecycleBacklogRecord(projectsRoot, refreshed, string(AbortDiscarded))
	if err := persistLifecycleBacklog(home, &backlogRecord, lifecycleStageSealed); err != nil {
		return err
	}
	result.BacklogID = backlogRecord.ID
	if remoteHead != "" {
		if err := persistLifecycleBacklog(home, &backlogRecord, lifecycleStageRetiringRemote); err != nil {
			return err
		}
		if err := worktree.validate(); err != nil {
			return err
		}
		if err := runSecureCleanupGitHelper(ctx, canonical, worktree.parent, worktree.worktree, worktree.parentPath, refreshed.WorktreeDir,
			"push", "--force-with-lease=refs/heads/"+refreshed.Branch+":"+refreshed.HeadSHA, "origin", ":refs/heads/"+refreshed.Branch); err != nil {
			return fmt.Errorf("delete discarded remote branch %s at %s: %w", refreshed.Branch, refreshed.HeadSHA, err)
		}
		result.RemoteDeleted = true
		if err := persistLifecycleBacklog(home, &backlogRecord, lifecycleStageRemoteRetired); err != nil {
			return err
		}
	}
	if err := worktree.validate(); err != nil {
		return err
	}
	if err := persistLifecycleBacklog(home, &backlogRecord, lifecycleStageRemovingWorktree); err != nil {
		return err
	}
	if err := runSecureCleanupGitHelper(ctx, canonical, worktree.parent, worktree.worktree, worktree.parentPath, refreshed.WorktreeDir,
		"worktree", "remove", refreshed.WorktreeDir); err != nil {
		return fmt.Errorf("remove discarded worktree %s: %w", refreshed.WorktreeDir, err)
	}
	result.WorktreeGone = true
	if err := persistLifecycleBacklog(home, &backlogRecord, lifecycleStageWorktreeRemoved); err != nil {
		return err
	}
	if options.afterAbortWorktreeRemoval != nil {
		if err := options.afterAbortWorktreeRemoval(refreshed.WorktreeDir); err != nil {
			return fmt.Errorf("after discarded worktree removal for %s: %w", refreshed.Repository, err)
		}
	}
	if err := task.validate(); err != nil {
		return err
	}
	if err := persistLifecycleBacklog(home, &backlogRecord, lifecycleStageRemovingLocalBranch); err != nil {
		return err
	}
	if err := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "update-ref", "-d", "refs/heads/"+refreshed.Branch, refreshed.HeadSHA); err != nil {
		return fmt.Errorf("delete discarded branch %s: %w", refreshed.Branch, err)
	}
	result.BranchDeleted = true
	if refreshed.External {
		owner, repository, splitErr := splitRepository(refreshed.Repository)
		if splitErr != nil {
			return fmt.Errorf("resolve adopted worktree registration identity for %s: %w", refreshed.Repository, splitErr)
		}
		if err := removeAdoptedRegistration(task, owner, repository); err != nil {
			return err
		}
	}
	return persistLifecycleBacklog(home, &backlogRecord, lifecycleStageComplete)
}

func (d AbortDisposition) String() string { return strings.TrimSpace(string(d)) }

// abortRepositoryExcludedByFilter reports whether --filter leaves this
// repository out of the run, using the same substring-of-slug-or-path
// semantics as filterMatches/ListOptions.Filter. An empty filter excludes
// nothing, preserving today's all-repositories behavior exactly.
func abortRepositoryExcludedByFilter(filter, repository, worktreeDir string) bool {
	return !filterMatches(filter, repository, worktreeDir)
}

// firstFilterMatchingDiagnosticPath returns the first malformed-candidate
// path that --filter selects, or "" when none do (including when there are
// no diagnostics at all). An empty filter matches every diagnostic, so a
// non-empty result here reduces to "the first diagnostic" exactly as before
// --filter existed.
func firstFilterMatchingDiagnosticPath(filter string, diagnostics []ListDiagnostic) string {
	for _, diagnostic := range diagnostics {
		if diagnostic.NonBlocking {
			continue
		}
		if filterMatches(filter, diagnostic.Path) {
			return diagnostic.Path
		}
	}
	return ""
}
