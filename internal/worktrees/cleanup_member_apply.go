package worktrees

import "fmt"

// applyCleanupMember rechecks one checkout under the held task and repository
// locks, then advances its durable backlog before every destructive step.
func (run *cleanupRun) applyCleanupMember(task *cleanupTaskHandle, index int, remoteGate *remoteBranchDeletionGate, recoveredTransaction bool, pendingLifecycleBacklogs *int) error {
	return run.applyCleanupMemberWithPorts(task, index, remoteGate, recoveredTransaction, pendingLifecycleBacklogs, productionCleanupApplyMemberPorts())
}

func (run *cleanupRun) applyCleanupMemberWithPorts(task *cleanupTaskHandle, index int, remoteGate *remoteBranchDeletionGate, recoveredTransaction bool, pendingLifecycleBacklogs *int, ports cleanupApplyMemberPorts) error {
	worktree, err := ports.OpenWorktree(task, run.outcome.Results[index])
	if err != nil {
		return err
	}
	defer ports.CloseWorktree(worktree)
	refreshed, err := ports.Recheck(run.ctx, cleanupMemberRecheck{
		Options: run.normalized, Now: run.now, Home: run.resolution.Write.Home,
		Entry: run.outcome.Results[index],
	}, worktree)
	if err != nil {
		return err
	}
	run.outcome.Results[index].ListResult = refreshed
	canonical, err := ports.OpenCanonical(refreshed.CanonicalDir)
	if err != nil {
		return fmt.Errorf("open cleanup canonical repository %s: %w", refreshed.CanonicalDir, err)
	}
	defer ports.CloseCanonical(canonical)
	if err := ports.ValidateCanonical(canonical); err != nil {
		return fmt.Errorf("cleanup canonical repository changed before Git operations: %w", err)
	}
	if run.normalized.beforeCleanupWorktreeRemoval != nil {
		run.normalized.beforeCleanupWorktreeRemoval(refreshed.WorktreeDir)
	}
	// Git's worktree-remove command requires the registered lexical path
	// (it rejects descriptor aliases such as /dev/fd/N). Reauthorize that
	// spelling against the retained task/owner/worktree descriptors at the
	// last possible point; any substitution conservatively aborts before Git
	// can remove a checkout or its registration.
	if err := ports.ValidateWorktree(worktree); err != nil {
		return err
	}
	if err := ports.PreflightWorkLog(run.resolution.Write.Home, refreshed.WorktreeDir, refreshed.HeadSHA); err != nil {
		if recoveryErr := ports.RecoverLegacy(run.ctx, run.resolution.Write.Home, run.normalized.ProjectsRoot, refreshed, run.normalized.beforeLegacyRelocationReceipt); recoveryErr != nil {
			return fmt.Errorf("recover legacy Work Log repository relocation before removing %s: %w", refreshed.WorktreeDir, recoveryErr)
		}
	}
	// Archive the recoverable run record while every Git asset still
	// exists. Remote branch deletion is destructive too, so it must never
	// precede the durable terminal/outbox record.
	var sealErr error
	if refreshed.SupersededAtOrigin && refreshed.supersessionReceipt != nil {
		sealErr = ports.SealSupersession(run.resolution.Write.Home, refreshed.WorktreeDir, refreshed.HeadSHA, refreshed.supersessionReceipt)
	} else {
		sealErr = ports.SealCleanup(run.resolution.Write.Home, refreshed.WorktreeDir, refreshed.HeadSHA)
	}
	if sealErr != nil {
		return fmt.Errorf("seal work log before removing %s: %w", refreshed.WorktreeDir, sealErr)
	}
	backlogRecord := ports.NewBacklog(run.normalized.ProjectsRoot, refreshed, "removed")
	if err := ports.Persist(run.resolution.Write.Home, &backlogRecord, lifecycleStageSealed); err != nil {
		return err
	}
	(*pendingLifecycleBacklogs)++
	run.outcome.Results[index].BacklogID = backlogRecord.ID
	if run.normalized.DeleteRemote && refreshed.RemoteHeadSHA != "" {
		if err := ports.Persist(run.resolution.Write.Home, &backlogRecord, lifecycleStageRetiringRemote); err != nil {
			return err
		}
		// Every authorization and the network call itself sit inside one
		// gate slot, so the bound counts branch deletions actually in
		// flight against origin rather than tasks that intend one. The
		// slot is taken after this task's repository locks and released
		// before them; nothing holding a slot waits for a lock, so the
		// two resources cannot form a cycle.
		deleteErr := func() error {
			releaseRemoteSlot := remoteGate.enter()
			defer releaseRemoteSlot()
			if err := ports.ValidateWorktree(worktree); err != nil {
				return err
			}
			if run.normalized.beforeCleanupNetworkBranchOperation != nil {
				run.normalized.beforeCleanupNetworkBranchOperation(refreshed.WorktreeDir)
			}
			if err := ports.ValidateWorktree(worktree); err != nil {
				return err
			}
			if run.normalized.afterCleanupGitAuthorization != nil {
				run.normalized.afterCleanupGitAuthorization("delete remote branch")
			}
			if err := ports.ValidateRecovered(recoveredTransaction, task); err != nil {
				return err
			}
			if err := invokeCleanupExactRefDelete(cleanupRemoteRef, refreshed.Branch, refreshed.RemoteHeadSHA, func(args ...string) error {
				return ports.WorktreeGit(run.ctx, canonical, worktree, refreshed.WorktreeDir, args...)
			}); err != nil {
				return fmt.Errorf("delete remote branch %s at %s: %w", refreshed.Branch, refreshed.RemoteHeadSHA, err)
			}
			return nil
		}()
		if deleteErr != nil {
			return deleteErr
		}
		run.outcome.Results[index].RemoteDeleted = true
		if err := ports.Persist(run.resolution.Write.Home, &backlogRecord, lifecycleStageRemoteRetired); err != nil {
			return err
		}
	}
	if err := ports.ValidateWorktree(worktree); err != nil {
		return err
	}
	if run.normalized.afterCleanupGitAuthorization != nil {
		run.normalized.afterCleanupGitAuthorization("remove worktree")
	}
	if err := ports.ValidateRecovered(recoveredTransaction, task); err != nil {
		return err
	}
	if err := ports.Persist(run.resolution.Write.Home, &backlogRecord, lifecycleStageRemovingWorktree); err != nil {
		return err
	}
	if removeErr := ports.WorktreeGit(run.ctx, canonical, worktree, refreshed.WorktreeDir, "worktree", "remove", refreshed.WorktreeDir); removeErr != nil {
		// Git deletes the working tree first and the registration
		// second, and it deletes the registration even when the
		// tree delete failed partway. Ask which of the two failures
		// this was before deciding whether the task is finishable.
		residue, residueErr := ports.Residue(run.ctx, canonical, refreshed.WorktreeDir)
		if residueErr != nil {
			return fmt.Errorf("remove worktree %s: %w; inspect its registration afterwards: %v", refreshed.WorktreeDir, removeErr, residueErr)
		}
		if !residue {
			return fmt.Errorf("remove worktree %s: %w", refreshed.WorktreeDir, removeErr)
		}
		if run.normalized.beforeCleanupResidueRemoval != nil {
			if err := run.normalized.beforeCleanupResidueRemoval(refreshed.WorktreeDir); err != nil {
				return err
			}
		}
		removed, repairErr := ports.RemoveResidue(worktree, refreshed.WorktreeDir)
		if repairErr != nil {
			return fmt.Errorf("remove worktree %s: %w; %v", refreshed.WorktreeDir, removeErr, repairErr)
		}
		run.outcome.Results[index].WorktreeResidueRemoved = removed
	}
	run.outcome.Results[index].WorktreeGone = true
	if err := ports.Persist(run.resolution.Write.Home, &backlogRecord, lifecycleStageWorktreeRemoved); err != nil {
		return err
	}
	if run.normalized.afterCleanupWorktreeRemoval != nil {
		if err := run.normalized.afterCleanupWorktreeRemoval(refreshed.WorktreeDir); err != nil {
			return fmt.Errorf("after worktree removal for %s: %w", refreshed.Repository, err)
		}
	}
	if err := ports.ValidateTask(task); err != nil {
		return err
	}
	if run.normalized.afterCleanupGitAuthorization != nil {
		run.normalized.afterCleanupGitAuthorization("delete local branch")
	}
	if err := ports.ValidateRecovered(recoveredTransaction, task); err != nil {
		return err
	}
	if err := ports.Persist(run.resolution.Write.Home, &backlogRecord, lifecycleStageRemovingLocalBranch); err != nil {
		return err
	}
	// A detached checkout has no branch ref of its own: a review
	// checkout points straight at a commit. Deleting the checkout is
	// the whole of its retirement, and asking Git to delete
	// refs/heads/ with an empty name would be a request to remove
	// something that was never created.
	if refreshed.Branch != "" {
		if err := invokeCleanupExactRefDelete(cleanupLocalRef, refreshed.Branch, refreshed.HeadSHA, func(args ...string) error {
			return ports.CanonicalGit(run.ctx, canonical, args...)
		}); err != nil {
			return fmt.Errorf("delete local branch %s at %s: %w", refreshed.Branch, refreshed.HeadSHA, err)
		}
		run.outcome.Results[index].BranchDeleted = true
	}
	if err := ports.RemoveParent(worktree, run.normalized.afterCleanupParentAuthorization, run.normalized.afterCleanupOwnerRetirement); err != nil {
		return err
	}
	if refreshed.External {
		owner, repository, splitErr := splitRepository(refreshed.Repository)
		if splitErr != nil {
			return fmt.Errorf("resolve adopted worktree registration identity for %s: %w", refreshed.Repository, splitErr)
		}
		if err := ports.RemoveAdopted(task, owner, repository); err != nil {
			return err
		}
	}
	if err := ports.Persist(run.resolution.Write.Home, &backlogRecord, lifecycleStageComplete); err != nil {
		return err
	}
	(*pendingLifecycleBacklogs)--
	run.outcome.Results[index].Applied = true
	return nil
}
