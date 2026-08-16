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
)

type AbortOptions struct {
	ProjectsRoot string
	Task         string
	Base         string
	Disposition  AbortDisposition
	Successor    string
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
}

type AbortResult struct {
	ListResult
	Disposition   AbortDisposition `json:"disposition"`
	Successor     string           `json:"successor,omitempty"`
	Eligible      bool             `json:"eligible"`
	Applied       bool             `json:"applied"`
	WorktreeGone  bool             `json:"worktree_gone"`
	BranchDeleted bool             `json:"branch_deleted"`
	RemoteDeleted bool             `json:"remote_deleted"`
	BacklogID     string           `json:"backlog_id,omitempty"`
	Reason        string           `json:"reason,omitempty"`
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
	if task == "" {
		return nil, fmt.Errorf("task is required")
	}
	if options.Disposition != AbortHandoff && options.Disposition != AbortNotLanded && options.Disposition != AbortDiscarded {
		return nil, fmt.Errorf("disposition must be handoff, not_landed, or discarded")
	}
	options.Successor = strings.TrimSpace(options.Successor)
	if options.Disposition != AbortDiscarded && (options.Successor == "" || len(options.Successor) > 200 || strings.ContainsAny(options.Successor, "\x00\r\n")) {
		return nil, fmt.Errorf("--successor is required exactly once for %s", options.Disposition)
	}
	if options.Disposition == AbortDiscarded && options.Successor != "" {
		return nil, fmt.Errorf("--successor cannot be used with discarded")
	}
	identitySupplied := strings.TrimSpace(options.SuccessorIdentity.Model) != "" || strings.TrimSpace(options.SuccessorIdentity.CLI) != "" || strings.TrimSpace(options.SuccessorIdentity.Provider) != ""
	if options.Disposition != AbortDiscarded && (options.Apply || identitySupplied) {
		if err := validateNewExecutionIdentity(options.SuccessorIdentity); err != nil {
			return nil, err
		}
	} else if options.Disposition == AbortDiscarded && identitySupplied {
		return nil, fmt.Errorf("--model, --cli, and --provider cannot be used with discarded")
	}
	if options.Apply && options.Disposition == AbortDiscarded && !options.DeleteRemote {
		return nil, fmt.Errorf("discarded abort requires remote branch retirement; rerun with --remote")
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
		backlog, err = loadResumableLifecycleBacklog(resolution.Write.Home, projectsRoot, recognizedWorktreesRoots, task, "", string(AbortDiscarded))
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
			reason = "task is locked by an active or interrupted operation"
		} else if !entry.Clean && options.Disposition == AbortDiscarded {
			reason = "discarded worktree has local changes; use handoff/not_landed or checkpoint it"
		}
		results[i] = AbortResult{ListResult: entry, Disposition: options.Disposition, Successor: options.Successor, Eligible: eligible, Reason: reason}
	}
	for _, record := range backlog {
		results = append(results, AbortResult{ListResult: ListResult{
			Task: record.Task, Repository: record.Repository, CanonicalDir: record.CanonicalDir,
			WorktreeDir: record.WorktreeDir, WorktreesRoot: record.WorktreesRoot,
			Branch: record.Branch, Base: record.Base, HeadSHA: record.HeadSHA,
		}, Disposition: AbortDiscarded, Eligible: true, WorktreeGone: true,
			BacklogID: record.ID, Reason: "durable cleanup backlog awaiting exact local branch retirement"})
	}
	if len(listed.Diagnostics) > 0 {
		for i := range results {
			results[i].Eligible = false
			results[i].Reason = "task has malformed worktree candidate: " + listed.Diagnostics[0].Path
		}
	}
	if !options.Apply {
		return results, nil
	}
	for _, result := range results {
		if !result.Eligible {
			return results, fmt.Errorf("task %q cannot be aborted safely: %s", task, result.Reason)
		}
	}
	for backlogIndex := range backlog {
		if err := resumeLifecycleBacklog(ctx, resolution.Write.Home, &backlog[backlogIndex]); err != nil {
			return results, err
		}
		for resultIndex := range results {
			if results[resultIndex].BacklogID == backlog[backlogIndex].ID {
				results[resultIndex].Applied = true
				results[resultIndex].BranchDeleted = true
				results[resultIndex].Reason = "resumed durable cleanup backlog"
			}
		}
	}
	if len(listed.Results) == 0 {
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
		refreshed, remoteHead, preflightErr := preflightAbortRepository(ctx, options, taskHandle, results[i], resolution.Write.Home)
		if preflightErr != nil {
			return results, preflightErr
		}
		results[i].ListResult = refreshed
		results[i].RemoteHeadSHA = remoteHead
	}
	for i := range listed.Results {
		result := &results[i]
		if options.Disposition == AbortDiscarded {
			if err := applyDiscardedAbort(ctx, options, taskHandle, resolution.Write.Home, result); err != nil {
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
		options.ProjectsRoot,
		wbhome.Layout{WorktreesRoot: result.WorktreesRoot},
		result.Task,
		result.WorktreeDir,
		result.Base,
		"", // Abort discards work outright; no landing receipt applies.
		false,
		false,
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
		options.ProjectsRoot,
		wbhome.Layout{WorktreesRoot: result.WorktreesRoot},
		result.Task,
		result.WorktreeDir,
		result.Base,
		"",
		false,
		false,
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
	backlogRecord := newLifecycleBacklogRecord(options.ProjectsRoot, refreshed, string(AbortDiscarded))
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
	return persistLifecycleBacklog(home, &backlogRecord, lifecycleStageComplete)
}

func (d AbortDisposition) String() string { return strings.TrimSpace(string(d)) }
