package worktrees

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// cleanupMemberRecheck keeps the read-only proof shared by locked task
// preflight and the immediate, descriptor-bound recheck before removal.
// Preflight still performs its additional Work Log and canonical checks; the
// removal path still performs legacy relocation recovery and terminal sealing.
type cleanupMemberRecheck struct {
	Options   CleanupOptions
	Now       time.Time
	Home      string
	Entry     CleanupResult
	Preflight bool
}

type cleanupMemberRecheckPorts struct {
	Validate        func() error
	Inspect         func(context.Context, cleanupMemberRecheck) (ListResult, error)
	MergeProof      func(context.Context, CleanupOptions, *ListResult) error
	Acknowledgement func(context.Context, string, *ListResult) error
	Supersession    func(context.Context, string, *ListResult)
	Eligible        func(ListResult, CleanupOptions, time.Time) (bool, string)
}

type cleanupRefKind uint8

const (
	cleanupRemoteRef cleanupRefKind = iota
	cleanupLocalRef
)

// invokeCleanupExactRefDelete is the small shared mutation boundary for a
// freshly sealed cleanup and a validated recovery record. Its caller retains
// the held descriptors, remote authority, phase journal and error wording.
func invokeCleanupExactRefDelete(kind cleanupRefKind, branch, expectedSHA string, run func(...string) error) error {
	if kind == cleanupRemoteRef {
		return run("push", "--force-with-lease=refs/heads/"+branch+":"+expectedSHA,
			"origin", ":refs/heads/"+branch)
	}
	return run("update-ref", "-d", "refs/heads/"+branch, expectedSHA)
}

func productionCleanupMemberRecheckPorts(worktree *cleanupWorktreeHandle) cleanupMemberRecheckPorts {
	return cleanupMemberRecheckPorts{
		Validate: worktree.validate,
		Inspect: func(ctx context.Context, request cleanupMemberRecheck) (ListResult, error) {
			entry := request.Entry
			options := request.Options
			return inspectLifecycleWorktree(ctx, options.ProjectsRoot, request.Home,
				wbhome.Layout{WorktreesRoot: entry.WorktreesRoot, Local: entry.Local},
				entry.Task, entry.WorktreeDir, options.Base, options.AbsorbedBy,
				true, false, entry.External, cleanupInspectPolicy(options))
		},
		MergeProof: func(ctx context.Context, options CleanupOptions, result *ListResult) error {
			return applyMergeReceiptCleanupProof(ctx, options.MergeReceiptProofs, result)
		},
		Acknowledgement: applyAbsorbedConflictAcknowledgementCleanupProof,
		Supersession:    applySupersessionReceipt,
		Eligible:        cleanupEligibility,
	}
}

func recheckCleanupMember(ctx context.Context, request cleanupMemberRecheck, ports cleanupMemberRecheckPorts) (ListResult, error) {
	if err := ports.Validate(); err != nil {
		return ListResult{}, err
	}
	refreshed, err := ports.Inspect(ctx, request)
	if err != nil {
		if request.Preflight {
			return ListResult{}, fmt.Errorf("preflight cleanup %s: %w", request.Entry.Repository, err)
		}
		return ListResult{}, err
	}
	if err := ports.MergeProof(ctx, request.Options, &refreshed); err != nil {
		if request.Preflight {
			return ListResult{}, fmt.Errorf("preflight cleanup %s receipt proof: %w", request.Entry.Repository, err)
		}
		return ListResult{}, fmt.Errorf("cleanup receipt proof for %s: %w", refreshed.Repository, err)
	}
	if err := ports.Acknowledgement(ctx, request.Home, &refreshed); err != nil {
		if request.Preflight {
			return ListResult{}, fmt.Errorf("preflight cleanup %s absorbed-conflict acknowledgement proof: %w", request.Entry.Repository, err)
		}
		return ListResult{}, fmt.Errorf("cleanup absorbed-conflict acknowledgement proof for %s: %w", refreshed.Repository, err)
	}
	ports.Supersession(ctx, request.Options.SupersededBy, &refreshed)
	if request.Preflight && refreshed.SupersessionRejection != "" {
		return ListResult{}, fmt.Errorf("preflight cleanup %s supersession receipt refused: %s", request.Entry.Repository, refreshed.SupersessionRejection)
	}
	if err := ports.Validate(); err != nil {
		return ListResult{}, err
	}
	if eligible, reason := ports.Eligible(refreshed, request.Options, request.Now); !eligible {
		return ListResult{}, fmt.Errorf("cleanup safety changed for %s: %s", refreshed.Repository, reason)
	}
	if refreshed.HeadSHA != request.Entry.HeadSHA {
		return ListResult{}, fmt.Errorf("cleanup safety changed for %s: branch head moved", refreshed.Repository)
	}
	return refreshed, nil
}

type cleanupPreflightPorts struct {
	OpenWorktree      func(*cleanupTaskHandle, CleanupResult) (*cleanupWorktreeHandle, error)
	Recheck           func(context.Context, cleanupMemberRecheck, *cleanupWorktreeHandle) (ListResult, error)
	OpenCanonical     func(string) (*canonicalRepository, error)
	ValidateCanonical func(*canonicalRepository) error
	WorkLog           func(context.Context, string, string, ListResult) error
}

func productionCleanupPreflightPorts() cleanupPreflightPorts {
	return cleanupPreflightPorts{
		OpenWorktree: openCleanupWorktree,
		Recheck: func(ctx context.Context, request cleanupMemberRecheck, worktree *cleanupWorktreeHandle) (ListResult, error) {
			return recheckCleanupMember(ctx, request, productionCleanupMemberRecheckPorts(worktree))
		},
		OpenCanonical:     openCanonicalRepository,
		ValidateCanonical: (*canonicalRepository).validate,
		WorkLog:           preflightWorkLogSealForCleanup,
	}
}

func preflightCleanupRepositoryWithPorts(ctx context.Context, options CleanupOptions, now time.Time, task *cleanupTaskHandle, entry CleanupResult, home string, ports cleanupPreflightPorts) (ListResult, error) {
	worktree, err := ports.OpenWorktree(task, entry)
	if err != nil {
		return ListResult{}, err
	}
	defer worktree.close()
	refreshed, err := ports.Recheck(ctx, cleanupMemberRecheck{
		Options: options, Now: now, Home: home, Entry: entry, Preflight: true,
	}, worktree)
	if err != nil {
		return ListResult{}, err
	}
	canonical, err := ports.OpenCanonical(refreshed.CanonicalDir)
	if err != nil {
		return ListResult{}, fmt.Errorf("open cleanup canonical repository %s: %w", refreshed.CanonicalDir, err)
	}
	defer canonical.close()
	if err := ports.ValidateCanonical(canonical); err != nil {
		return ListResult{}, fmt.Errorf("cleanup canonical repository changed during preflight: %w", err)
	}
	if err := ports.WorkLog(ctx, home, options.ProjectsRoot, refreshed); err != nil {
		return ListResult{}, fmt.Errorf("preflight Work Log for %s: %w", refreshed.Repository, err)
	}
	return refreshed, nil
}

// cleanupBacklogPorts are bound to one resume call. Recovery still owns its
// validated record and reclaimed task lock; these ports make each external
// observation or destructive operation explicit at that boundary.
type cleanupBacklogPorts struct {
	ValidateRecord    func(lifecycleBacklogRecord) error
	AcquireTask       func(string, string, bool) (*cleanupTaskHandle, error)
	ReleaseTask       func(*cleanupTaskHandle)
	CompleteVacant    func(context.Context, string, *lifecycleBacklogRecord, error) error
	OpenCanonical     func(string) (*canonicalRepository, error)
	ValidateCanonical func(*canonicalRepository) error
	CloseCanonical    func(*canonicalRepository)
	RemoteHead        func(context.Context, *lifecycleBacklogRecord) (string, error)
	DeleteRemote      func(context.Context, *canonicalRepository, *lifecycleBacklogRecord) error
	Persist           func(string, *lifecycleBacklogRecord, string) error
	Registrations     func(context.Context, *canonicalRepository) (map[string]bool, error)
	Lstat             func(string) (os.FileInfo, error)
	OpenWorktree      func(*cleanupTaskHandle, CleanupResult) (*cleanupWorktreeHandle, error)
	RemoveResidue     func(*cleanupWorktreeHandle, string) (bool, error)
	RemoveParent      func(*cleanupWorktreeHandle) error
	CloseWorktree     func(*cleanupWorktreeHandle)
	LocalExists       func(context.Context, *canonicalRepository, string) (bool, error)
	LocalHead         func(context.Context, *canonicalRepository, string) (string, error)
	DeleteLocal       func(context.Context, *canonicalRepository, *lifecycleBacklogRecord) error
	SealClaim         func(string, lifecycleBacklogRecord) error
}

func productionCleanupBacklogPorts() cleanupBacklogPorts {
	return cleanupBacklogPorts{
		ValidateRecord: validateLifecycleBacklog,
		AcquireTask:    acquireCleanupTaskAtReclaimingInterrupted,
		ReleaseTask: func(task *cleanupTaskHandle) {
			_ = task.lock.release()
			task.close()
		},
		CompleteVacant:    completeVacantLifecycleBacklog,
		OpenCanonical:     openCanonicalRepository,
		ValidateCanonical: (*canonicalRepository).validate,
		CloseCanonical:    (*canonicalRepository).close,
		RemoteHead:        detachedAwareRemoteBranchHead,
		DeleteRemote: func(ctx context.Context, canonical *canonicalRepository, record *lifecycleBacklogRecord) error {
			return invokeCleanupExactRefDelete(cleanupRemoteRef, record.Branch, record.RemoteHeadSHA, func(args ...string) error {
				return runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", args...)
			})
		},
		Persist:       persistLifecycleBacklog,
		Registrations: registeredWorktreePathsCanonical,
		Lstat:         os.Lstat,
		OpenWorktree:  openCleanupWorktree,
		RemoveResidue: removeUnregisteredWorktreeResidue,
		RemoveParent: func(worktree *cleanupWorktreeHandle) error {
			return worktree.removeEmptyParent(nil, nil)
		},
		CloseWorktree: (*cleanupWorktreeHandle).close,
		LocalExists:   localBranchExistsCanonical,
		LocalHead: func(ctx context.Context, canonical *canonicalRepository, branch string) (string, error) {
			return gitCanonical(ctx, canonical, "rev-parse", "refs/heads/"+branch)
		},
		DeleteLocal: func(ctx context.Context, canonical *canonicalRepository, record *lifecycleBacklogRecord) error {
			return invokeCleanupExactRefDelete(cleanupLocalRef, record.Branch, record.HeadSHA, func(args ...string) error {
				return runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", args...)
			})
		},
		SealClaim: sealCreateFailureBacklogClaim,
	}
}

// cleanupApplyMemberPorts is scoped to one held task member. The phase owner
// invokes existing secure/claim/journal services through these exact ports;
// it never substitutes path-only operations for retained descriptors.
type cleanupApplyMemberPorts struct {
	OpenWorktree      func(*cleanupTaskHandle, CleanupResult) (*cleanupWorktreeHandle, error)
	CloseWorktree     func(*cleanupWorktreeHandle)
	Recheck           func(context.Context, cleanupMemberRecheck, *cleanupWorktreeHandle) (ListResult, error)
	OpenCanonical     func(string) (*canonicalRepository, error)
	CloseCanonical    func(*canonicalRepository)
	ValidateCanonical func(*canonicalRepository) error
	ValidateWorktree  func(*cleanupWorktreeHandle) error
	PreflightWorkLog  func(string, string, string) error
	RecoverLegacy     func(context.Context, string, string, ListResult, func() error) error
	SealSupersession  func(string, string, string, *SupersessionReceipt) error
	SealCleanup       func(string, string, string) error
	NewBacklog        func(string, ListResult, string) lifecycleBacklogRecord
	Persist           func(string, *lifecycleBacklogRecord, string) error
	ValidateRecovered func(bool, *cleanupTaskHandle) error
	WorktreeGit       func(context.Context, *canonicalRepository, *cleanupWorktreeHandle, string, ...string) error
	CanonicalGit      func(context.Context, *canonicalRepository, ...string) error
	Residue           func(context.Context, *canonicalRepository, string) (bool, error)
	RemoveResidue     func(*cleanupWorktreeHandle, string) (bool, error)
	ValidateTask      func(*cleanupTaskHandle) error
	RemoveParent      func(*cleanupWorktreeHandle, func(string), func(string)) error
	RemoveAdopted     func(*cleanupTaskHandle, string, string) error
}

func productionCleanupApplyMemberPorts() cleanupApplyMemberPorts {
	return cleanupApplyMemberPorts{
		OpenWorktree:  openCleanupWorktree,
		CloseWorktree: (*cleanupWorktreeHandle).close,
		Recheck: func(ctx context.Context, request cleanupMemberRecheck, worktree *cleanupWorktreeHandle) (ListResult, error) {
			return recheckCleanupMember(ctx, request, productionCleanupMemberRecheckPorts(worktree))
		},
		OpenCanonical:     openCanonicalRepository,
		CloseCanonical:    (*canonicalRepository).close,
		ValidateCanonical: (*canonicalRepository).validate,
		ValidateWorktree:  (*cleanupWorktreeHandle).validate,
		PreflightWorkLog:  preflightWorkLogSeal,
		RecoverLegacy:     recordLegacyRepositoryRelocationForCleanup,
		SealSupersession:  sealWorkLogForSupersession,
		SealCleanup:       sealWorkLogForCleanup,
		NewBacklog:        newLifecycleBacklogRecord,
		Persist:           persistLifecycleBacklog,
		ValidateRecovered: validateRecoveredCleanupLock,
		WorktreeGit: func(ctx context.Context, canonical *canonicalRepository, worktree *cleanupWorktreeHandle, path string, args ...string) error {
			return runSecureCleanupGitHelper(ctx, canonical, worktree.parent, worktree.worktree, worktree.parentPath, path, args...)
		},
		CanonicalGit: func(ctx context.Context, canonical *canonicalRepository, args ...string) error {
			return runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", args...)
		},
		Residue:       worktreeRemovalLeftResidue,
		RemoveResidue: removeUnregisteredWorktreeResidue,
		ValidateTask:  (*cleanupTaskHandle).validate,
		RemoveParent:  (*cleanupWorktreeHandle).removeEmptyParent,
		RemoveAdopted: removeAdoptedRegistration,
	}
}
