package worktrees

import (
	"context"
	"fmt"
	"time"
)

// These are one invocation's concrete task collaborators. The facade keeps
// both WB_HOME locks, per-member ordering, immutable reservation, and report
// folding; tests can fault a boundary without inventing a second Git engine.
type renameTaskApplicationPorts struct {
	Report      func(string, []RenameResult, []ListDiagnostic) (string, error)
	PrepareTask func(string) (preparedOperationRoot, error)
	AcquireLock func(preparedOperationRoot, string) (operationLock, error)
	Inventory   func(string) (ListOutcome, error)
	Preflight   func(*renamePlan) error
	Reserve     func() error
	Apply       func(*renamePlan) error
	Rollback    func([]renamePlan) error
}

func productionRenameTaskApplicationPorts(ctx context.Context, options RenameOptions, now time.Time, home string) renameTaskApplicationPorts {
	return renameTaskApplicationPorts{
		Report: func(stage string, results []RenameResult, diagnostics []ListDiagnostic) (string, error) {
			return writeRenameReport(options, now, stage, results, diagnostics)
		},
		PrepareTask: func(task string) (preparedOperationRoot, error) {
			return prepareOperationRoot(home, task, nil)
		},
		AcquireLock: func(operation preparedOperationRoot, task string) (operationLock, error) {
			return acquireLockAt(operation.Directory, task)
		},
		Inventory: func(task string) (ListOutcome, error) {
			return ListWithDiagnostics(ctx, ListOptions{
				ProjectsRoot: options.ProjectsRoot, Task: task, Base: options.Base, GitHub: false,
			})
		},
		Preflight: func(plan *renamePlan) error {
			return preflightRename(ctx, options, plan)
		},
		Reserve: func() error {
			return reservePreApplyRenameWorkLog(home, options.OldTask, options.NewTask, options.WorkLog)
		},
		Apply: func(plan *renamePlan) error {
			return applyRename(ctx, home, options, plan)
		},
		Rollback: func(plans []renamePlan) error {
			return rollbackAppliedRenames(ctx, home, plans)
		},
	}
}

func applyRenameTask(options RenameOptions, plans []renamePlan, diagnostics []ListDiagnostic, ports renameTaskApplicationPorts) (RenameOutcome, error) {
	outcome := RenameOutcome{Results: collectRenameResults(plans), Diagnostics: diagnostics}
	fail := func(renameErr error) (RenameOutcome, error) {
		if options.ReportDir != "" {
			path, reportErr := ports.Report("failed", outcome.Results, outcome.Diagnostics)
			if reportErr != nil {
				return outcome, fmt.Errorf("%w; write failed rename report: %v", renameErr, reportErr)
			}
			outcome.ReportPath = path
		}
		return outcome, renameErr
	}
	if options.ReportDir != "" {
		if _, reportErr := ports.Report("planned", outcome.Results, outcome.Diagnostics); reportErr != nil {
			return outcome, reportErr
		}
	}

	anyEligible := false
	for _, plan := range plans {
		if plan.result.Eligible {
			anyEligible = true
			break
		}
	}
	if !anyEligible {
		return fail(fmt.Errorf("no repository under task %q is eligible to rename: %s", options.OldTask, firstRenameReason(plans)))
	}

	// WB_HOME owns both task locks for every physical placement. No local
	// canonical root may grow an independent lock for the same transaction.
	oldOperation, err := ports.PrepareTask(options.OldTask)
	if err != nil {
		return fail(fmt.Errorf("open task %q: %w", options.OldTask, err))
	}
	defer oldOperation.close()
	oldLock, err := ports.AcquireLock(oldOperation, options.OldTask)
	if err != nil {
		return fail(fmt.Errorf("lock task %q: %w", options.OldTask, err))
	}
	defer func() { _ = oldLock.release() }()

	// Prove every old member under the source lock before creating destination
	// control-plane state or sealing any source claim.
	if options.beforeRenamePreflight != nil {
		options.beforeRenamePreflight()
	}
	for index := range plans {
		if !plans[index].result.Eligible {
			continue
		}
		if err := ports.Preflight(&plans[index]); err != nil {
			outcome.Results = collectRenameResults(plans)
			return fail(err)
		}
	}
	// The first inventory decides collision before our reservation exists.
	// The second, under the destination lock, closes a concurrent create race.
	newInventory, err := ports.Inventory(options.NewTask)
	if err != nil {
		return fail(fmt.Errorf("inspect destination task %q: %w", options.NewTask, err))
	}
	if len(newInventory.Results) > 0 || len(newInventory.Diagnostics) > 0 {
		return fail(fmt.Errorf("destination task already exists: %s", options.NewTask))
	}
	newOperation, err := ports.PrepareTask(options.NewTask)
	if err != nil {
		return fail(err)
	}
	defer newOperation.close()
	newLock, err := ports.AcquireLock(newOperation, options.NewTask)
	if err != nil {
		return fail(fmt.Errorf("lock task %q: %w", options.NewTask, err))
	}
	defer func() { _ = newLock.release() }()
	newInventory, err = ports.Inventory(options.NewTask)
	if err != nil {
		return fail(fmt.Errorf("inspect destination task %q while locked: %w", options.NewTask, err))
	}
	if len(newInventory.Results) > 0 || len(newInventory.Diagnostics) > 0 {
		return fail(fmt.Errorf("destination task already exists: %s", options.NewTask))
	}
	if err := ports.Reserve(); err != nil {
		return fail(fmt.Errorf("reserve new private Work Log prompt: %w", err))
	}
	if options.afterPreApplyReservation != nil {
		if err := options.afterPreApplyReservation(); err != nil {
			return fail(fmt.Errorf("after pre-apply rename reservation: %w", err))
		}
	}
	for index := range plans {
		if !plans[index].result.Eligible {
			continue
		}
		if applyErr := ports.Apply(&plans[index]); applyErr != nil {
			rollbackErr := ports.Rollback(plans[:index])
			outcome.Results = collectRenameResults(plans)
			if rollbackErr != nil {
				applyErr = fmt.Errorf("%w; coordinated rollback failed: %v", applyErr, rollbackErr)
			}
			return fail(applyErr)
		}
	}
	outcome.Results = collectRenameResults(plans)

	// Keep the now-possibly-empty old task root while its descriptor lock is
	// live. Removing it after release would create an ABA window for create.
	if options.ReportDir != "" {
		outcome.ReportPath, err = ports.Report("applied", outcome.Results, outcome.Diagnostics)
		if err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

// The old claim becomes terminal before its live projection is removed.
// Rollback is armed by plan.sealed at that exact durable boundary.
type renameClaimCutoverPorts struct {
	Seal   func(string) error
	Remove func() error
}

func productionRenameClaimCutoverPorts(home, worktree string) renameClaimCutoverPorts {
	return renameClaimCutoverPorts{
		Seal: func(head string) error {
			return sealWorkLogForRecycle(home, worktree, head, "recycled")
		},
		Remove: func() error {
			return removeWorkLogProjection(worktree)
		},
	}
}

func sealPriorRenameClaimAndRemoveProjection(plan *renamePlan, refreshed ListResult, ports renameClaimCutoverPorts) error {
	if err := ports.Seal(refreshed.HeadSHA); err != nil {
		return fmt.Errorf("seal previous work log for %s: %w", refreshed.Repository, err)
	}
	plan.sealed = true
	return ports.Remove()
}

// renameRemoteRetirementPorts represent the exact old remote head recheck and
// leased deletion performed after sealing the previous claim. Callers retain
// authority to decide whether remote deletion is explicitly permitted.
type renameRemoteRetirementPorts struct {
	CurrentHead func() (string, error)
	DeleteExact func(string) error
}

func productionRenameRemoteRetirementPorts(ctx context.Context, plan *renamePlan) renameRemoteRetirementPorts {
	return renameRemoteRetirementPorts{
		CurrentHead: func() (string, error) {
			return remoteBranchHead(ctx, plan.entry.CanonicalDir, plan.entry.Branch)
		},
		DeleteExact: func(expectedHead string) error {
			canonical, err := openCanonicalRepository(plan.entry.CanonicalDir)
			if err != nil {
				return err
			}
			defer canonical.close()
			return invokeCleanupExactRefDelete(cleanupRemoteRef, plan.entry.Branch, expectedHead, func(args ...string) error {
				return runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", args...)
			})
		},
	}
}

func retireRenameRemote(options RenameOptions, plan *renamePlan, refreshed ListResult, ports renameRemoteRetirementPorts) error {
	currentRemoteHead, err := ports.CurrentHead()
	if err != nil {
		return fmt.Errorf("recheck remote branch before recycling %s: %w", plan.entry.Repository, err)
	}
	if currentRemoteHead != plan.remoteHead || (currentRemoteHead != "" && currentRemoteHead != refreshed.HeadSHA) {
		return fmt.Errorf("recycle safety changed for %s: remote branch moved from %q to %q", plan.entry.Repository, plan.remoteHead, currentRemoteHead)
	}
	if currentRemoteHead == "" {
		return nil
	}
	if !options.DeleteRemote {
		return fmt.Errorf("origin/%s still exists; recycle requires explicit --remote retirement", plan.entry.Branch)
	}
	if err := ports.DeleteExact(refreshed.HeadSHA); err != nil {
		return fmt.Errorf("retire old remote branch %s at %s: %w", plan.entry.Branch, refreshed.HeadSHA, err)
	}
	plan.remoteDeleted = true
	plan.result.OldRemoteDeleted = true
	return nil
}

// These collaborators start only after the descriptor-bound move succeeds.
// Old branch deletion remains under a retained canonical handle, and a new
// Work Log is bound only after checkout, guard and exact old-ref deletion.
type renameFinishPorts struct {
	Checkout        func() error
	Guard           func() error
	DeleteOldBranch func() (bool, error)
	BindWorkLog     func() error
}

func productionRenameFinishPorts(ctx context.Context, home string, options RenameOptions, plan *renamePlan, refreshed ListResult) renameFinishPorts {
	return renameFinishPorts{
		Checkout: func() error {
			return runSecureRenameGit(ctx, plan.entry.CanonicalDir, plan.destinationRoot, plan.result.NewWorktreeDir,
				"checkout", "-b", plan.result.NewBranch, plan.baseRevision)
		},
		Guard: func() error {
			_, err := Guard(ctx, plan.result.NewWorktreeDir, GuardOptions{ProjectsRoot: options.ProjectsRoot, Base: options.Base})
			return err
		},
		DeleteOldBranch: func() (bool, error) {
			canonical, err := openCanonicalRepository(plan.entry.CanonicalDir)
			if err != nil {
				return false, err
			}
			defer canonical.close()
			deleted, _, err := deleteOldBranchIfSafe(ctx, canonical, plan.entry.Branch, refreshed.HeadSHA,
				plan.result.NewBranch, options.Base, options.Force)
			return deleted, err
		},
		BindWorkLog: func() error {
			_, err := recordWorkLog(home, options.NewTask, CreateResult{
				Repository: plan.entry.Repository, CanonicalDir: plan.entry.CanonicalDir,
				WorktreeDir: plan.result.NewWorktreeDir, Branch: plan.result.NewBranch, Base: options.Base,
				BaseSHA: plan.baseRevision, Action: "recycled",
			}, options.WorkLog)
			return err
		},
	}
}

func finishRenameMember(options RenameOptions, plan *renamePlan, ports renameFinishPorts) error {
	if err := ports.Checkout(); err != nil {
		return fmt.Errorf("check out new branch %s in %s: %w", plan.result.NewBranch, plan.result.NewWorktreeDir, err)
	}
	plan.newBranchCreated = true
	if err := ports.Guard(); err != nil {
		return fmt.Errorf("renamed worktree %s failed guard: %w", plan.result.NewWorktreeDir, err)
	}
	deleted, err := ports.DeleteOldBranch()
	if err != nil {
		return err
	}
	if !deleted {
		return fmt.Errorf("old branch %q was not deleted; recycle is incomplete", plan.entry.Branch)
	}
	plan.oldBranchDeleted = true
	plan.result.OldBranchDeleted = true
	if options.beforeRenameBind != nil {
		if err := options.beforeRenameBind(plan.entry.Repository); err != nil {
			return fmt.Errorf("bind preflight for %s: %w", plan.entry.Repository, err)
		}
	}
	if err := ports.BindWorkLog(); err != nil {
		return fmt.Errorf("bind recycled worktree to a new work log: %w", err)
	}
	plan.result.Applied = true
	return nil
}
