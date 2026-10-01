package worktrees

import (
	"context"
	"fmt"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// abortPorts belongs to one invocation. Its defaults retain the existing
// inventory, held-descriptor, private Work Log, backlog, and secure Git paths.
type abortPorts struct {
	resolve           func(string) (wbhome.Resolution, error)
	discoverLocal     func(context.Context, string, string) ([]wbhome.Layout, []ListDiagnostic)
	configuredLayouts func([]wbhome.Layout) ([]wbhome.Layout, error)
	loadBacklog       func(context.Context, string, string, []string, map[string]bool, string, string) ([]lifecycleBacklogRecord, []LifecycleBacklogQuarantine, error)
	list              func(context.Context, ListOptions) (ListOutcome, error)
	reservations      func(wbhome.Resolution, string, AbortOptions) ([]AbortResult, bool, error)
	resumeBacklog     func(context.Context, string, *lifecycleBacklogRecord, bool) error
	acquireTask       func(string, string) (*cleanupTaskHandle, error)
	preflightWorkLog  func(string, AbortOptions, ListResult) (bool, error)
	dirtyEvidence     func(context.Context, string) (DirtyWorktreeEvidence, error)
	preflightMember   func(context.Context, string, AbortOptions, *cleanupTaskHandle, AbortResult, string) (ListResult, string, *DirtyWorktreeEvidence, bool, error)
	applyDiscarded    func(context.Context, string, AbortOptions, *cleanupTaskHandle, string, *AbortResult) error
	transferWorkLog   func(string, string, string, string, string, ClaimExecutionIdentity) error
}

func productionAbortPorts() abortPorts {
	member := productionAbortMemberPorts()
	ports := abortPorts{
		resolve: wbhome.Resolve, discoverLocal: discoverCanonicalLocalWorktreeLayouts,
		configuredLayouts: appendConfiguredSharedWorktreesLayout, loadBacklog: loadResumableLifecycleBacklog,
		list: ListWithDiagnostics, reservations: abortPreApplyRenameReservations,
		resumeBacklog: resumeLifecycleBacklog, acquireTask: acquireCleanupTaskAtOrCreate,
		preflightWorkLog: preflightAbortWorkLog, dirtyEvidence: dirtyWorktreeEvidence,
		transferWorkLog: transferWorkLogClaim,
	}
	ports.preflightMember = func(ctx context.Context, root string, options AbortOptions, task *cleanupTaskHandle, result AbortResult, home string) (ListResult, string, *DirtyWorktreeEvidence, bool, error) {
		return preflightAbortRepositoryWithPorts(ctx, root, options, task, result, home, member)
	}
	ports.applyDiscarded = func(ctx context.Context, root string, options AbortOptions, task *cleanupTaskHandle, home string, result *AbortResult) error {
		return applyDiscardedAbortWithPorts(ctx, root, options, task, home, result, member)
	}
	return ports
}

// abortMemberPorts name the fallible observations and effects of one freshly
// selected member. The caller still owns its task lock and held descriptors.
type abortMemberPorts struct {
	openWorktree      func(*cleanupTaskHandle, CleanupResult) (*cleanupWorktreeHandle, error)
	validateWorktree  func(*cleanupWorktreeHandle) error
	inspect           func(context.Context, abortMemberInspection) (ListResult, error)
	openCanonical     func(string) (*canonicalRepository, error)
	validateCanonical func(*canonicalRepository) error
	remoteHead        func(context.Context, string, string) (string, error)
	dirtyEvidence     func(context.Context, string) (DirtyWorktreeEvidence, error)
	captureDirty      func(context.Context, string, string, *DirtyWorktreeEvidence) (*DirtyWorktreeEvidence, error)
	preflightWorkLog  func(string, AbortOptions, ListResult) (bool, error)
	recoverLegacy     func(string, AbortOptions, ListResult) error
	sealDiscarded     func(string, string, string, *DirtyWorktreeEvidence) error
	newBacklog        func(string, ListResult, string) lifecycleBacklogRecord
	persistBacklog    func(string, *lifecycleBacklogRecord, string) error
	worktreeGit       func(context.Context, *canonicalRepository, *cleanupWorktreeHandle, string, ...string) error
	canonicalGit      func(context.Context, *canonicalRepository, ...string) error
	validateTask      func(*cleanupTaskHandle) error
	removeAdopted     func(*cleanupTaskHandle, string, string) error
}

type abortMemberInspection struct {
	projectsRoot string
	home         string
	options      AbortOptions
	entry        ListResult
}

func productionAbortMemberPorts() abortMemberPorts {
	return abortMemberPorts{
		openWorktree: openCleanupWorktree, validateWorktree: (*cleanupWorktreeHandle).validate,
		inspect: func(ctx context.Context, request abortMemberInspection) (ListResult, error) {
			entry, options := request.entry, request.options
			return inspectLifecycleWorktree(ctx, request.projectsRoot, request.home,
				wbhome.Layout{WorktreesRoot: entry.WorktreesRoot, Local: entry.Local},
				entry.Task, entry.WorktreeDir, entry.Base, options.AbsorbedBy,
				options.AbsorbedBy != "", false, entry.External, inspectPolicy{includeDetached: true})
		},
		openCanonical: openCanonicalRepository, validateCanonical: (*canonicalRepository).validate,
		remoteHead: remoteBranchHead, dirtyEvidence: dirtyWorktreeEvidence,
		captureDirty: captureAndPersistDirtyWorktree, preflightWorkLog: preflightAbortWorkLog,
		recoverLegacy: recoverLegacyMissingClaimForAbort,
		sealDiscarded: sealDiscardedWorkLogAfterAbsorbedByProof,
		newBacklog:    newLifecycleBacklogRecord, persistBacklog: persistLifecycleBacklog,
		worktreeGit: func(ctx context.Context, canonical *canonicalRepository, worktree *cleanupWorktreeHandle, path string, args ...string) error {
			return runSecureCleanupGitHelper(ctx, canonical, worktree.parent, worktree.worktree, worktree.parentPath, path, args...)
		},
		canonicalGit: func(ctx context.Context, canonical *canonicalRepository, args ...string) error {
			return runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", args...)
		},
		validateTask: (*cleanupTaskHandle).validate, removeAdopted: removeAdoptedRegistration,
	}
}

// observeAbortMember shares the descriptor-bound lifecycle observation used
// under the task lock during preflight and again before discard. Each caller
// retains its own phase-specific identity, remote, Work Log and dirty policy.
func observeAbortMember(ctx context.Context, request abortMemberInspection, phase string, worktree *cleanupWorktreeHandle, ports abortMemberPorts) (ListResult, error) {
	if err := ports.validateWorktree(worktree); err != nil {
		return ListResult{}, err
	}
	refreshed, err := ports.inspect(ctx, request)
	if err != nil {
		return ListResult{}, fmt.Errorf("%s %s: %w", phase, request.entry.Repository, err)
	}
	return refreshed, nil
}

func preflightAbortRepositoryWithPorts(
	ctx context.Context,
	projectsRoot string,
	options AbortOptions,
	task *cleanupTaskHandle,
	result AbortResult,
	home string,
	ports abortMemberPorts,
) (ListResult, string, *DirtyWorktreeEvidence, bool, error) {
	worktree, err := ports.openWorktree(task, CleanupResult{ListResult: result.ListResult})
	if err != nil {
		return ListResult{}, "", nil, false, err
	}
	defer worktree.close()
	refreshed, err := observeAbortMember(ctx, abortMemberInspection{projectsRoot: projectsRoot, home: home, options: options, entry: result.ListResult}, "preflight abort", worktree, ports)
	if err != nil {
		return ListResult{}, "", nil, false, err
	}
	if err := ports.validateWorktree(worktree); err != nil {
		return ListResult{}, "", nil, false, err
	}
	if refreshed.HeadSHA != result.HeadSHA || refreshed.Branch != result.Branch || refreshed.Repository != result.Repository {
		return ListResult{}, "", nil, false, fmt.Errorf("abort safety changed for %s: checkout identity or branch head moved", result.Repository)
	}
	if err := absorbedAbortSafety(result.ListResult, refreshed, options.AbsorbedBy); err != nil {
		return ListResult{}, "", nil, false, fmt.Errorf("abort safety changed for %s: %w", result.Repository, err)
	}
	canonical, err := ports.openCanonical(refreshed.CanonicalDir)
	if err != nil {
		return ListResult{}, "", nil, false, fmt.Errorf("open abort canonical repository %s: %w", refreshed.CanonicalDir, err)
	}
	defer canonical.close()
	if err := ports.validateCanonical(canonical); err != nil {
		return ListResult{}, "", nil, false, err
	}
	remoteHead := ""
	if options.Disposition == AbortDiscarded && refreshed.Branch != "" {
		remoteHead, err = ports.remoteHead(ctx, refreshed.CanonicalDir, refreshed.Branch)
		if err != nil {
			return ListResult{}, "", nil, false, fmt.Errorf("inspect remote branch before discarding %s: %w", refreshed.Repository, err)
		}
		if remoteHead != "" && remoteHead != refreshed.HeadSHA {
			return ListResult{}, "", nil, false, fmt.Errorf("refuse to discard %s: origin/%s is %s, expected exact local head %s", refreshed.Repository, refreshed.Branch, remoteHead, refreshed.HeadSHA)
		}
	}
	recovery, err := ports.preflightWorkLog(home, options, refreshed)
	if err != nil {
		return ListResult{}, "", nil, false, fmt.Errorf("preflight aborted Work Log for %s: %w", refreshed.Repository, err)
	}
	var dirty *DirtyWorktreeEvidence
	if options.Disposition == AbortDiscarded {
		evidence, err := ports.dirtyEvidence(ctx, refreshed.WorktreeDir)
		if err != nil {
			return ListResult{}, "", nil, false, fmt.Errorf("capture dirty worktree evidence for %s: %w", refreshed.Repository, err)
		}
		dirty = &evidence
		if result.DirtyCapture != nil && !dirtyCaptureMatches(*result.DirtyCapture, evidence) {
			return ListResult{}, "", nil, false, dirtyCaptureChangedError(*result.DirtyCapture, evidence)
		}
	}
	return refreshed, remoteHead, dirty, recovery, nil
}

func applyDiscardedAbortWithPorts(
	ctx context.Context,
	projectsRoot string,
	options AbortOptions,
	task *cleanupTaskHandle,
	home string,
	result *AbortResult,
	ports abortMemberPorts,
) error {
	worktree, err := ports.openWorktree(task, CleanupResult{ListResult: result.ListResult})
	if err != nil {
		return err
	}
	defer worktree.close()
	if options.beforeAbortRemoval != nil {
		options.beforeAbortRemoval(result.WorktreeDir)
	}
	// This is deliberately at the last destructive boundary. A dirty checkout
	// is removable only after captureAndPersistDirtyWorktree has retained the
	// exact bytes and the Work Log seal below is durable; --force is then
	// required because Git's ordinary remove refuses any dirty checkout.
	refreshed, err := observeAbortMember(ctx, abortMemberInspection{projectsRoot: projectsRoot, home: home, options: options, entry: result.ListResult}, "recheck discarded worktree", worktree, ports)
	if err != nil {
		return err
	}
	if refreshed.HeadSHA != result.HeadSHA || refreshed.Branch != result.Branch {
		return fmt.Errorf("abort safety changed for %s immediately before removal: branch head moved", result.Repository)
	}
	if err := absorbedAbortSafety(result.ListResult, refreshed, options.AbsorbedBy); err != nil {
		return fmt.Errorf("abort safety changed for %s immediately before removal: %w", result.Repository, err)
	}
	if err := ports.validateWorktree(worktree); err != nil {
		return err
	}
	remoteHead := ""
	if refreshed.Branch != "" {
		remoteHead, err = ports.remoteHead(ctx, refreshed.CanonicalDir, refreshed.Branch)
		if err != nil {
			return fmt.Errorf("recheck remote branch before discarding %s: %w", refreshed.Repository, err)
		}
	}
	if remoteHead != result.RemoteHeadSHA || (remoteHead != "" && remoteHead != refreshed.HeadSHA) {
		return fmt.Errorf("abort safety changed for %s: remote branch moved from %q to %q", refreshed.Repository, result.RemoteHeadSHA, remoteHead)
	}
	canonical, err := ports.openCanonical(refreshed.CanonicalDir)
	if err != nil {
		return fmt.Errorf("open abort canonical repository %s: %w", refreshed.CanonicalDir, err)
	}
	defer canonical.close()
	if err := ports.validateCanonical(canonical); err != nil {
		return err
	}
	observed, captureErr := ports.dirtyEvidence(ctx, refreshed.WorktreeDir)
	if captureErr != nil {
		return fmt.Errorf("inspect dirty worktree for %s before discard: %w", result.Repository, captureErr)
	}
	if result.DirtyCapture != nil && !dirtyCaptureMatches(*result.DirtyCapture, observed) {
		return dirtyCaptureChangedError(*result.DirtyCapture, observed)
	}
	if !refreshed.Clean {
		dirty, captureErr := ports.captureDirty(ctx, home, refreshed.WorktreeDir, result.DirtyCapture)
		if captureErr != nil {
			return fmt.Errorf("capture dirty worktree for %s before discard: %w", result.Repository, captureErr)
		}
		result.DirtyCapture = dirty
	} else {
		result.DirtyCapture = nil
	}
	if result.WorkLogRecoveryPlanned {
		if err := ports.recoverLegacy(home, options, refreshed); err != nil {
			return fmt.Errorf("recover legacy missing Work Log claim for %s: %w", refreshed.Repository, err)
		}
		result.WorkLogRecovered = true
	}
	// The private archive/outbox is durable before remote or local Git state
	// is retired. A failed later step is therefore a visible cleanup backlog,
	// never an evidence-free disappearance.
	//
	// This discard path only ever runs for
	// options.Disposition == AbortDiscarded. A worktree may already have an
	// immutable "landed" terminal from an earlier `wb worktree log finalize
	// --apply` whose branch was then rebased onto a moved target and landed
	// as a merge commit (S63): --absorbed-by's proof above already verified
	// that exact landing, so sealDiscardedWorkLogAfterAbsorbedByProof composes
	// with the same additive authorization cleanup uses instead of refusing.
	// A worktree with no existing terminal (the ordinary abort case) is
	// unaffected: its first, ordinary seal attempt succeeds immediately.
	if err := ports.sealDiscarded(home, refreshed.WorktreeDir, refreshed.HeadSHA, result.DirtyCapture); err != nil {
		return fmt.Errorf("seal discarded work log for %s: %w", refreshed.Repository, err)
	}
	backlogRecord := ports.newBacklog(projectsRoot, refreshed, string(AbortDiscarded))
	if err := ports.persistBacklog(home, &backlogRecord, lifecycleStageSealed); err != nil {
		return err
	}
	result.BacklogID = backlogRecord.ID
	if remoteHead != "" {
		if err := ports.persistBacklog(home, &backlogRecord, lifecycleStageRetiringRemote); err != nil {
			return err
		}
		if err := ports.validateWorktree(worktree); err != nil {
			return err
		}
		if err := invokeCleanupExactRefDelete(cleanupRemoteRef, refreshed.Branch, refreshed.HeadSHA, func(args ...string) error {
			return ports.worktreeGit(ctx, canonical, worktree, refreshed.WorktreeDir, args...)
		}); err != nil {
			return fmt.Errorf("delete discarded remote branch %s at %s: %w", refreshed.Branch, refreshed.HeadSHA, err)
		}
		result.RemoteDeleted = true
		if err := ports.persistBacklog(home, &backlogRecord, lifecycleStageRemoteRetired); err != nil {
			return err
		}
	}
	if err := ports.validateWorktree(worktree); err != nil {
		return err
	}
	if err := ports.persistBacklog(home, &backlogRecord, lifecycleStageRemovingWorktree); err != nil {
		return err
	}
	if err := ports.worktreeGit(ctx, canonical, worktree, refreshed.WorktreeDir,
		"worktree", "remove", "--force", refreshed.WorktreeDir); err != nil {
		return fmt.Errorf("remove discarded worktree %s: %w", refreshed.WorktreeDir, err)
	}
	result.WorktreeGone = true
	if err := ports.persistBacklog(home, &backlogRecord, lifecycleStageWorktreeRemoved); err != nil {
		return err
	}
	if options.afterAbortWorktreeRemoval != nil {
		if err := options.afterAbortWorktreeRemoval(refreshed.WorktreeDir); err != nil {
			return fmt.Errorf("after discarded worktree removal for %s: %w", refreshed.Repository, err)
		}
	}
	if err := ports.validateTask(task); err != nil {
		return err
	}
	if err := ports.persistBacklog(home, &backlogRecord, lifecycleStageRemovingLocalBranch); err != nil {
		return err
	}
	// A detached checkout has no ref of its own; removing the checkout is the
	// whole of its retirement.
	if refreshed.Branch != "" {
		if err := invokeCleanupExactRefDelete(cleanupLocalRef, refreshed.Branch, refreshed.HeadSHA, func(args ...string) error {
			return ports.canonicalGit(ctx, canonical, args...)
		}); err != nil {
			return fmt.Errorf("delete discarded branch %s: %w", refreshed.Branch, err)
		}
		result.BranchDeleted = true
	}
	if refreshed.External {
		owner, repository, splitErr := splitRepository(refreshed.Repository)
		if splitErr != nil {
			return fmt.Errorf("resolve adopted worktree registration identity for %s: %w", refreshed.Repository, splitErr)
		}
		if err := ports.removeAdopted(task, owner, repository); err != nil {
			return err
		}
	}
	return ports.persistBacklog(home, &backlogRecord, lifecycleStageComplete)
}
