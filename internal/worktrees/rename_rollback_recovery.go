package worktrees

import (
	"context"
	"errors"
	"fmt"
)

// The read-only selector examines both current and legacy projections. Only
// explicit absence is safe to pass; malformed or inaccessible evidence stops
// compensation before any Git ref or checkout mutation.
type renameRollbackClaimPorts struct {
	ReadProjection func() (workLogProjection, error)
	CurrentHead    func() (string, error)
	SealFresh      func(string) error
	RemoveFresh    func() error
}

func productionRenameRollbackClaimPorts(ctx context.Context, home, currentPath string) renameRollbackClaimPorts {
	return renameRollbackClaimPorts{
		ReadProjection: func() (workLogProjection, error) {
			return readWorkLogProjectionForReadOnlyClaim(currentPath)
		},
		CurrentHead: func() (string, error) {
			return git(ctx, currentPath, "rev-parse", "HEAD")
		},
		SealFresh: func(head string) error {
			return sealWorkLogForRecycle(home, currentPath, head, "recycle_failed")
		},
		RemoveFresh: func() error {
			return removeWorkLogProjection(currentPath)
		},
	}
}

func retireFreshRenameClaimOnRollback(plan *renamePlan, ports renameRollbackClaimPorts) error {
	projection, err := ports.ReadProjection()
	if err != nil && !errors.Is(err, errWorkLogProjectionNotFound) {
		return fmt.Errorf("inspect fresh recycle projection before rollback: %w", err)
	}
	if errors.Is(err, errWorkLogProjectionNotFound) || projection.ClaimID == plan.priorProjection.ClaimID {
		return nil
	}
	head, err := ports.CurrentHead()
	if err != nil {
		return err
	}
	if err := ports.SealFresh(head); err != nil {
		return err
	}
	return ports.RemoveFresh()
}

// The new branch is a failed-attempt artifact only while its exact tip still
// equals the planned base. A retained canonical handle spans query and CAS.
type renameFailedBranchPorts struct {
	Exists      func() (bool, error)
	Open        func() (*canonicalRepository, error)
	Head        func(*canonicalRepository) (string, error)
	DeleteExact func(*canonicalRepository) error
}

func productionRenameFailedBranchPorts(ctx context.Context, plan *renamePlan) renameFailedBranchPorts {
	return renameFailedBranchPorts{
		Exists: func() (bool, error) {
			return localBranchExists(ctx, plan.entry.CanonicalDir, plan.result.NewBranch)
		},
		Open: func() (*canonicalRepository, error) {
			return openCanonicalRepository(plan.entry.CanonicalDir)
		},
		Head: func(canonical *canonicalRepository) (string, error) {
			return gitCanonical(ctx, canonical, "rev-parse", "refs/heads/"+plan.result.NewBranch)
		},
		DeleteExact: func(canonical *canonicalRepository) error {
			_, err := gitCanonical(ctx, canonical, "update-ref", "-d", "refs/heads/"+plan.result.NewBranch, plan.baseRevision)
			return err
		},
	}
}

func removeFailedRenameBranch(plan *renamePlan, ports renameFailedBranchPorts) error {
	exists, err := ports.Exists()
	if err != nil || !exists {
		return err
	}
	canonical, err := ports.Open()
	if err != nil {
		return err
	}
	defer canonical.close()
	newHead, err := ports.Head(canonical)
	if err != nil {
		return err
	}
	if newHead != plan.baseRevision {
		return fmt.Errorf("refuse to remove failed recycle branch %s: expected %s, found %s", plan.result.NewBranch, plan.baseRevision, newHead)
	}
	return ports.DeleteExact(canonical)
}

// Restore usesthe recorded immutable source SHA under an empty remote lease.
// The same query is deliberately repeated after push: an accepted invocation
// is insufficient evidence that the remote still names the recorded object.
type renameRemoteRestorePorts struct {
	RemoteHead   func() (string, error)
	PushRecorded func() error
}

func productionRenameRemoteRestorePorts(ctx context.Context, plan *renamePlan) renameRemoteRestorePorts {
	return renameRemoteRestorePorts{
		RemoteHead: func() (string, error) {
			return remoteBranchHead(ctx, plan.entry.CanonicalDir, plan.entry.Branch)
		},
		PushRecorded: func() error {
			canonical, err := openCanonicalRepository(plan.entry.CanonicalDir)
			if err != nil {
				return err
			}
			defer canonical.close()
			return runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "",
				"push", "--force-with-lease=refs/heads/"+plan.entry.Branch+":", "origin",
				plan.entry.HeadSHA+":refs/heads/"+plan.entry.Branch)
		},
	}
}

func restoreRetiredRenameRemote(plan *renamePlan, ports renameRemoteRestorePorts) error {
	remoteHead, err := ports.RemoteHead()
	if err != nil {
		return fmt.Errorf("inspect remote before recycle rollback: %w", err)
	}
	if remoteHead != "" {
		return fmt.Errorf("refuse to restore remote branch %s: another actor created it at %s", plan.entry.Branch, remoteHead)
	}
	if err := ports.PushRecorded(); err != nil {
		return fmt.Errorf("restore retired remote branch %s at %s: %w", plan.entry.Branch, plan.entry.HeadSHA, err)
	}
	restoredHead, err := ports.RemoteHead()
	if err != nil {
		return err
	}
	if restoredHead != plan.entry.HeadSHA {
		return fmt.Errorf("restored remote branch %s is %s, expected %s", plan.entry.Branch, restoredHead, plan.entry.HeadSHA)
	}
	return nil
}
