package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	// AbsorbedBy selects the merged GitHub pull request whose immutable
	// metadata proves this clean source was carried by a squash landing. It is
	// accepted only by the terminal discarded path and is re-proved before
	// removal; it never turns a human assertion into deletion authority.
	AbsorbedBy string
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

// abortResultLayout restores the provenance needed to choose the one logical
// task lock. ListResult carries the physical root for deletion, while
// wbhome.Resolution knows whether that root is the historic readable layout
// or a relocated user placement coordinated from WB_HOME.
func abortResultLayout(resolution wbhome.Resolution, result ListResult) wbhome.Layout {
	for _, layout := range resolution.Read {
		if filepath.Clean(layout.WorktreesRoot) == filepath.Clean(result.WorktreesRoot) {
			layout.Local = layout.Local || result.Local
			return layout
		}
	}
	return wbhome.Layout{WorktreesRoot: result.WorktreesRoot, Local: result.Local}
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
	Excluded      bool                   `json:"excluded,omitempty"`
	Applied       bool                   `json:"applied"`
	WorktreeGone  bool                   `json:"worktree_gone"`
	BranchDeleted bool                   `json:"branch_deleted"`
	RemoteDeleted bool                   `json:"remote_deleted"`
	BacklogID     string                 `json:"backlog_id,omitempty"`
	DirtyCapture  *DirtyWorktreeEvidence `json:"dirty_capture,omitempty"`
	// ReservationRuns names immutable pre-apply Work Log reservations this
	// result terminalized. They have no checkout, branch, or remote ref, so
	// discarded recovery retains the prompt archive and needs no --remote.
	ReservationRuns []string `json:"reservation_runs,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	// Quarantined names durable cleanup records this run declined to act on.
	// It is carried on the first result rather than aborting the run: the
	// backlog directory is shared, and somebody else's unreadable record must
	// not be able to refuse this task's abort.
	Quarantined []LifecycleBacklogQuarantine `json:"quarantined,omitempty"`
}

// Abort seals every Work Log in a coordinated task. It is the deliberate
// escape hatch for unused or interrupted claims which cannot meet the merged
// PR evidence required by Cleanup. --apply never destroys resumable work:
// discarded removes a linked checkout only after its exact dirty bytes (when
// present) are retained in the private Work Log archive and the archive/outbox
// is durable.
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
	options.AbsorbedBy = strings.TrimSpace(options.AbsorbedBy)
	terminalWithoutSuccessor := options.Disposition == AbortDiscarded || options.Disposition == AbortOrphaned
	if !terminalWithoutSuccessor && (options.Successor == "" || len(options.Successor) > 200 || strings.ContainsAny(options.Successor, "\x00\r\n")) {
		return nil, fmt.Errorf("--successor is required exactly once for %s", options.Disposition)
	}
	if terminalWithoutSuccessor && options.Successor != "" {
		return nil, fmt.Errorf("--successor cannot be used with %s", options.Disposition)
	}
	if options.AbsorbedBy != "" && options.Disposition != AbortDiscarded {
		return nil, fmt.Errorf("--absorbed-by is valid only with discarded")
	}
	identitySupplied := strings.TrimSpace(options.SuccessorIdentity.Model) != "" || strings.TrimSpace(options.SuccessorIdentity.CLI) != "" || strings.TrimSpace(options.SuccessorIdentity.Provider) != ""
	if !terminalWithoutSuccessor && (options.Apply || identitySupplied) {
		if err := validateNewExecutionIdentity(options.SuccessorIdentity); err != nil {
			return nil, err
		}
	} else if terminalWithoutSuccessor && identitySupplied {
		return nil, fmt.Errorf("--model, --cli, and --provider cannot be used with %s", options.Disposition)
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
	var backlogQuarantine []LifecycleBacklogQuarantine
	if options.Disposition == AbortDiscarded {
		recognizedWorktreesRoots := make([]string, 0, len(resolution.Read))
		for _, layout := range resolution.Read {
			recognizedWorktreesRoots = append(recognizedWorktreesRoots, layout.WorktreesRoot)
		}
		// A removed local checkout leaves its canonical .worktrees parent in
		// place, while the durable backlog is still authoritative for retiring
		// the exact branch. Include those physical roots even though no live
		// task remains for List to discover.
		localLayouts, _ := discoverCanonicalLocalWorktreeLayouts(ctx, projectsRoot, "")
		for _, layout := range localLayouts {
			recognizedWorktreesRoots = append(recognizedWorktreesRoots, layout.WorktreesRoot)
		}
		configuredLayouts, configErr := appendConfiguredSharedWorktreesLayout(nil)
		if configErr != nil {
			return nil, configErr
		}
		for _, layout := range configuredLayouts {
			recognizedWorktreesRoots = append(recognizedWorktreesRoots, layout.WorktreesRoot)
		}
		var quarantined []LifecycleBacklogQuarantine
		backlog, quarantined, err = loadResumableLifecycleBacklog(ctx, resolution.Write.Home, projectsRoot, recognizedWorktreesRoots, taskSelectionSet([]string{task}), "", string(AbortDiscarded))
		if err != nil {
			return nil, err
		}
		backlogQuarantine = quarantined
	}
	// A detached checkout is the shape every pull-request review leaves behind,
	// and `wb worktree gc` names abort as the way to retire one. Reading the
	// inventory without it would make the named command fail on the exact
	// shape it was named for.
	listed, err := ListWithDiagnostics(ctx, ListOptions{
		ProjectsRoot: projectsRoot, Task: task, Base: base, IncludeDetached: true,
		AbsorbedBy: options.AbsorbedBy, GitHub: options.AbsorbedBy != "",
	})
	if err != nil {
		return nil, err
	}
	if len(listed.Results) == 0 && len(backlog) == 0 {
		if options.Disposition == AbortDiscarded {
			reservationResults, found, reservationErr := abortPreApplyRenameReservations(resolution, task, options)
			if reservationErr != nil {
				return reservationResults, reservationErr
			}
			if found {
				return reservationResults, nil
			}
		}
		return nil, fmt.Errorf("WB worktree task %q was not found", task)
	}
	if options.Apply && options.Disposition == AbortDiscarded && !options.DeleteRemote {
		return nil, fmt.Errorf("discarded abort requires remote branch retirement; rerun with --remote")
	}
	results := make([]AbortResult, len(listed.Results))
	for i, entry := range listed.Results {
		eligible := !entry.Locked
		reason := ""
		if entry.Locked {
			// Abort has no recovery authority of its own; point at the one
			// command that does rather than leaving a dead lock unexplained.
			reason = lockedReason(entry, resumeInterruptedCommand(task))
		}
		if options.AbsorbedBy != "" && !entry.AbsorbedAtOrigin {
			eligible = false
			reason = "--absorbed-by proof did not verify"
			if entry.AbsorbedByRejection != "" {
				reason += ": " + entry.AbsorbedByRejection
			}
		}
		if options.AbsorbedBy != "" && !entry.Clean {
			eligible = false
			reason = "--absorbed-by requires a clean worktree"
		}
		excluded := abortRepositoryExcludedByFilter(filter, entry.Repository, entry.WorktreeDir)
		results[i] = AbortResult{ListResult: entry, Disposition: options.Disposition, Successor: options.Successor, Eligible: eligible, Excluded: excluded, Reason: reason}
	}
	if len(backlogQuarantine) > 0 && len(results) > 0 {
		results[0].Quarantined = backlogQuarantine
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
		for i := range results {
			if results[i].Excluded || results[i].Locked || options.Disposition != AbortDiscarded {
				continue
			}
			evidence, captureErr := dirtyWorktreeEvidence(ctx, results[i].WorktreeDir)
			if captureErr != nil {
				results[i].Eligible = false
				results[i].Reason = captureErr.Error()
				continue
			}
			results[i].DirtyCapture = &evidence
		}
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
	lockRoot := lifecycleTaskLockRoot(resolution.Write.Home, abortResultLayout(resolution, results[0].ListResult))
	taskHandle, err := acquireCleanupTaskAtOrCreate(lockRoot, results[0].Task)
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
		refreshed, remoteHead, dirty, preflightErr := preflightAbortRepository(ctx, projectsRoot, options, taskHandle, results[i], resolution.Write.Home)
		if preflightErr != nil {
			return results, preflightErr
		}
		results[i].ListResult = refreshed
		results[i].RemoteHeadSHA = remoteHead
		results[i].DirtyCapture = dirty
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

// abortPreApplyRenameReservations is the narrow recovery path for a process
// that reserved a new recycle prompt but failed before it published the first
// worktree claim. It never removes a checkout, branch, remote ref, or prompt
// archive. A regular List result or lifecycle backlog bypasses this helper, so
// normal abort safety remains unchanged.
func abortPreApplyRenameReservations(resolution wbhome.Resolution, task string, options AbortOptions) ([]AbortResult, bool, error) {
	reservations, err := findPreApplyRenameReservations(resolution.Write.Home, task)
	if err != nil {
		return nil, false, fmt.Errorf("inspect pre-apply reservation for task %q: %w", task, err)
	}
	if len(reservations) == 0 {
		return nil, false, nil
	}
	result := AbortResult{
		ListResult: ListResult{Task: task}, Disposition: AbortDiscarded, Eligible: true,
		ReservationRuns: reservationRunNames(reservations),
		Reason:          "unclaimed pre-apply Work Log reservation; immutable prompt archive is retained",
	}
	if !options.Apply {
		return []AbortResult{result}, true, nil
	}

	cleanup, cleanupErr := acquirePreApplyReservationTask(resolution, task)
	if cleanupErr != nil {
		return []AbortResult{result}, true, cleanupErr
	}
	if cleanup != nil {
		defer cleanup.close()
		if err := preApplyReservationShellOnly(cleanup); err != nil {
			return []AbortResult{result}, true, err
		}
	}
	for _, reservation := range reservations {
		if err := terminalizePreApplyRenameReservation(resolution.Write.Home, reservation); err != nil {
			return []AbortResult{result}, true, fmt.Errorf("terminalize pre-apply reservation %s: %w", reservation.RunID, err)
		}
	}
	if cleanup != nil {
		if err := cleanup.lock.release(); err != nil {
			return []AbortResult{result}, true, fmt.Errorf("release pre-apply reservation task lock: %w", err)
		}
		cleanup.lock = operationLock{}
		purgeTerminalTaskLockDebris(cleanup)
		if !removeEmptyTaskDirectory(cleanup) {
			return []AbortResult{result}, true, fmt.Errorf("pre-apply reservation task shell changed before terminal cleanup")
		}
	}
	result.Applied = true
	result.Reason = "terminalized unclaimed pre-apply Work Log reservation; immutable prompt archive retained"
	return []AbortResult{result}, true, nil
}

func reservationRunNames(reservations []preApplyRenameReservationCandidate) []string {
	runs := make([]string, 0, len(reservations))
	for _, reservation := range reservations {
		runs = append(runs, reservation.EffortID+"/"+reservation.RunID)
	}
	return runs
}

// acquirePreApplyReservationTask opens only WB_HOME's logical task shell. A
// crashed rename can leave either a retired lock or its exact dead-owner lock;
// the latter follows the existing interrupted-lock proof before reuse.
func acquirePreApplyReservationTask(resolution wbhome.Resolution, task string) (*cleanupTaskHandle, error) {
	root := filepath.Join(resolution.Write.Home, "worktrees")
	if _, err := os.Lstat(filepath.Join(root, task)); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("inspect pre-apply reservation task shell: %w", err)
	}
	cleanup, err := acquireCleanupTaskAt(root, task)
	if err == nil {
		return cleanup, nil
	}
	// Only the existing named interrupted-lock recovery may reclaim a live
	// entry, and it independently proves the recorded owner is conclusively
	// dead before returning the held descriptor.
	recovered, _, recoveryErr := reclaimNamedInterruptedCleanupTask(resolution, task)
	if recoveryErr != nil {
		return nil, fmt.Errorf("recover pre-apply reservation task shell: %w", recoveryErr)
	}
	return recovered, nil
}

func preApplyReservationShellOnly(task *cleanupTaskHandle) error {
	if task == nil || task.task == nil {
		return fmt.Errorf("pre-apply reservation task shell is unavailable")
	}
	if err := task.validate(); err != nil {
		return err
	}
	if _, err := task.task.Seek(0, 0); err != nil {
		return err
	}
	entries, err := task.task.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == ".lock" || strings.HasPrefix(entry.Name(), ".wb-retired-lock-") {
			continue
		}
		return fmt.Errorf("pre-apply reservation task shell contains %s; preserve it for explicit recovery", entry.Name())
	}
	return nil
}

func preflightAbortRepository(
	ctx context.Context,
	projectsRoot string,
	options AbortOptions,
	task *cleanupTaskHandle,
	result AbortResult,
	home string,
) (ListResult, string, *DirtyWorktreeEvidence, error) {
	worktree, err := openCleanupWorktree(task, CleanupResult{ListResult: result.ListResult})
	if err != nil {
		return ListResult{}, "", nil, err
	}
	defer worktree.close()
	if err := worktree.validate(); err != nil {
		return ListResult{}, "", nil, err
	}
	refreshed, err := inspectLifecycleWorktree(
		ctx,
		projectsRoot,
		home,
		wbhome.Layout{WorktreesRoot: result.WorktreesRoot, Local: result.Local},
		result.Task,
		result.WorktreeDir,
		result.Base,
		options.AbsorbedBy,
		options.AbsorbedBy != "",
		false,
		result.External,
		inspectPolicy{includeDetached: true},
	)
	if err != nil {
		return ListResult{}, "", nil, fmt.Errorf("preflight abort %s: %w", result.Repository, err)
	}
	if err := worktree.validate(); err != nil {
		return ListResult{}, "", nil, err
	}
	if refreshed.HeadSHA != result.HeadSHA || refreshed.Branch != result.Branch || refreshed.Repository != result.Repository {
		return ListResult{}, "", nil, fmt.Errorf("abort safety changed for %s: checkout identity or branch head moved", result.Repository)
	}
	if err := absorbedAbortSafety(result.ListResult, refreshed, options.AbsorbedBy); err != nil {
		return ListResult{}, "", nil, fmt.Errorf("abort safety changed for %s: %w", result.Repository, err)
	}
	canonical, err := openCanonicalRepository(refreshed.CanonicalDir)
	if err != nil {
		return ListResult{}, "", nil, fmt.Errorf("open abort canonical repository %s: %w", refreshed.CanonicalDir, err)
	}
	defer canonical.close()
	if err := canonical.validate(); err != nil {
		return ListResult{}, "", nil, err
	}
	remoteHead := ""
	if options.Disposition == AbortDiscarded && refreshed.Branch != "" {
		remoteHead, err = remoteBranchHead(ctx, refreshed.CanonicalDir, refreshed.Branch)
		if err != nil {
			return ListResult{}, "", nil, fmt.Errorf("inspect remote branch before discarding %s: %w", refreshed.Repository, err)
		}
		if remoteHead != "" && remoteHead != refreshed.HeadSHA {
			return ListResult{}, "", nil, fmt.Errorf("refuse to discard %s: origin/%s is %s, expected exact local head %s", refreshed.Repository, refreshed.Branch, remoteHead, refreshed.HeadSHA)
		}
	}
	if err := preflightWorkLogSeal(home, refreshed.WorktreeDir, refreshed.HeadSHA); err != nil {
		return ListResult{}, "", nil, fmt.Errorf("preflight aborted Work Log for %s: %w", refreshed.Repository, err)
	}
	var dirty *DirtyWorktreeEvidence
	if options.Disposition == AbortDiscarded {
		evidence, err := dirtyWorktreeEvidence(ctx, refreshed.WorktreeDir)
		if err != nil {
			return ListResult{}, "", nil, fmt.Errorf("capture dirty worktree evidence for %s: %w", refreshed.Repository, err)
		}
		dirty = &evidence
		if result.DirtyCapture != nil && !dirtyCaptureMatches(*result.DirtyCapture, evidence) {
			return ListResult{}, "", nil, dirtyCaptureChangedError(*result.DirtyCapture, evidence)
		}
	}
	return refreshed, remoteHead, dirty, nil
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
	// This is deliberately at the last destructive boundary. A dirty checkout
	// is removable only after captureAndPersistDirtyWorktree has retained the
	// exact bytes and the Work Log seal below is durable; --force is then
	// required because Git's ordinary remove refuses any dirty checkout.
	refreshed, err := inspectLifecycleWorktree(
		ctx,
		projectsRoot,
		home,
		wbhome.Layout{WorktreesRoot: result.WorktreesRoot, Local: result.Local},
		result.Task,
		result.WorktreeDir,
		result.Base,
		options.AbsorbedBy,
		options.AbsorbedBy != "",
		false,
		result.External,
		inspectPolicy{includeDetached: true},
	)
	if err != nil {
		return fmt.Errorf("recheck discarded worktree %s: %w", result.Repository, err)
	}
	if refreshed.HeadSHA != result.HeadSHA || refreshed.Branch != result.Branch {
		return fmt.Errorf("abort safety changed for %s immediately before removal: branch head moved", result.Repository)
	}
	if err := absorbedAbortSafety(result.ListResult, refreshed, options.AbsorbedBy); err != nil {
		return fmt.Errorf("abort safety changed for %s immediately before removal: %w", result.Repository, err)
	}
	if err := worktree.validate(); err != nil {
		return err
	}
	remoteHead := ""
	if refreshed.Branch != "" {
		remoteHead, err = remoteBranchHead(ctx, refreshed.CanonicalDir, refreshed.Branch)
		if err != nil {
			return fmt.Errorf("recheck remote branch before discarding %s: %w", refreshed.Repository, err)
		}
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
	observed, captureErr := dirtyWorktreeEvidence(ctx, refreshed.WorktreeDir)
	if captureErr != nil {
		return fmt.Errorf("inspect dirty worktree for %s before discard: %w", result.Repository, captureErr)
	}
	if result.DirtyCapture != nil && !dirtyCaptureMatches(*result.DirtyCapture, observed) {
		return dirtyCaptureChangedError(*result.DirtyCapture, observed)
	}
	if !refreshed.Clean {
		dirty, captureErr := captureAndPersistDirtyWorktree(ctx, home, refreshed.WorktreeDir, result.DirtyCapture)
		if captureErr != nil {
			return fmt.Errorf("capture dirty worktree for %s before discard: %w", result.Repository, captureErr)
		}
		result.DirtyCapture = dirty
	} else {
		result.DirtyCapture = nil
	}
	// The private archive/outbox is durable before remote or local Git state
	// is retired. A failed later step is therefore a visible cleanup backlog,
	// never an evidence-free disappearance.
	if err := sealWorkLogForRecycleWithDirtyCapture(home, refreshed.WorktreeDir, refreshed.HeadSHA, string(options.Disposition), result.DirtyCapture); err != nil {
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
		"worktree", "remove", "--force", refreshed.WorktreeDir); err != nil {
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
	// A detached checkout has no ref of its own; removing the checkout is the
	// whole of its retirement.
	if refreshed.Branch != "" {
		if err := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "update-ref", "-d", "refs/heads/"+refreshed.Branch, refreshed.HeadSHA); err != nil {
			return fmt.Errorf("delete discarded branch %s: %w", refreshed.Branch, err)
		}
		result.BranchDeleted = true
	}
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

// absorbedAbortSafety keeps the destructive abort path bound to the exact
// proof it showed in its plan. inspectLifecycleWorktree has just fetched the
// target again and re-read GitHub's PR metadata, so a source advance, target
// rewrite, or changed PR receipt cannot reuse a prior dry-run decision.
func absorbedAbortSafety(planned, refreshed ListResult, absorbedBy string) error {
	if strings.TrimSpace(absorbedBy) == "" {
		return nil
	}
	if !refreshed.Clean {
		return fmt.Errorf("--absorbed-by requires a clean worktree")
	}
	if !refreshed.AbsorbedAtOrigin {
		if refreshed.AbsorbedByRejection != "" {
			return fmt.Errorf("--absorbed-by proof no longer verifies: %s", refreshed.AbsorbedByRejection)
		}
		return fmt.Errorf("--absorbed-by proof no longer verifies")
	}
	if refreshed.AbsorbedBySHA != planned.AbsorbedBySHA {
		return fmt.Errorf("--absorbed-by landing changed from %s to %s", planned.AbsorbedBySHA, refreshed.AbsorbedBySHA)
	}
	if !sameAbsorbedPullRequest(planned.MergedPullRequest, refreshed.MergedPullRequest) {
		return fmt.Errorf("--absorbed-by pull request evidence changed")
	}
	return nil
}

func sameAbsorbedPullRequest(left, right *PullRequest) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Number == right.Number && left.Repository == right.Repository &&
		left.Base == right.Base && left.BaseSHA == right.BaseSHA &&
		left.HeadSHA == right.HeadSHA && left.MergeSHA == right.MergeSHA &&
		left.Merged != nil && right.Merged != nil && left.Merged.Equal(*right.Merged)
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
