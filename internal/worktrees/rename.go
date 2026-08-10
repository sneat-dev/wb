package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
)

// RenameOptions controls re-homing every worktree below one task to a new
// task name. Recycling is deliberately opt-in and starts from a clean base:
// every untracked and ignored path outside an explicit, safe cache allow-list
// makes the operation refuse. WB never broadly cleans those paths. Callers may
// preserve a cache path (for example "node_modules") when the setup-time
// saving is worth it. This prevents a previous effort's source, credentials,
// or generated artefacts leaking merely because Git happened to ignore them.
//
// The branch itself is never recycled. Every renamed worktree is switched
// onto a freshly created branch based on an up-to-date Base, matching the
// rule that "the branch always goes; the worktree may be recycled."
type RenameOptions struct {
	ProjectsRoot string
	OldTask      string
	NewTask      string
	// Filter narrows which of OldTask's repositories are renamed to those
	// whose owner/repository slug contains this substring — see
	// ListOptions.Filter for the exact semantics. An empty Filter renames
	// every repository under OldTask.
	Filter string
	// Branch is the feature branch created in every renamed worktree. Empty
	// derives "codex/<new-task>", exactly like `wb worktree create` derives
	// "codex/<task>" when --branch is omitted.
	Branch string
	Base   string
	// DeleteOldBranch is retained for source compatibility; recycle always
	// deletes the old local branch. Force is the explicit discarded-work
	// authorization for an old branch not integrated into origin/Base.
	DeleteOldBranch bool
	// DeleteRemote must be explicit for apply. If origin/<old-branch> exists,
	// it must still equal the preflight head and is retired with an exact
	// force-with-lease after the old Work Log is durable.
	DeleteRemote bool
	Force        bool
	// PreserveCachePaths is the allow-list of ignored/untracked paths that may
	// survive recycle. Empty means no cache survives. Paths are repository
	// relative, safe, and audited in the rename report.
	PreserveCachePaths []string
	WorkLog            WorkLogOptions
	// Apply performs the rename. The default is a dry-run plan, exactly like
	// `wb worktree cleanup`.
	Apply     bool
	ReportDir string
	Now       func() time.Time
	// beforeRenameBind is a test-only failure seam after the old exact branch
	// was deleted and before the fresh claim is bound. It proves rollback can
	// recover a later repository without erasing earlier partial-result evidence.
	beforeRenameBind func(repository string) error
}

// RenameResult records one repository's rename decision and outcome.
type RenameResult struct {
	OldTask             string   `json:"old_task"`
	NewTask             string   `json:"new_task"`
	Repository          string   `json:"repository"`
	CanonicalDir        string   `json:"canonical_dir"`
	OldWorktreeDir      string   `json:"old_worktree_dir"`
	NewWorktreeDir      string   `json:"new_worktree_dir"`
	OldBranch           string   `json:"old_branch"`
	NewBranch           string   `json:"new_branch"`
	Base                string   `json:"base"`
	Eligible            bool     `json:"eligible"`
	Applied             bool     `json:"applied"`
	Repaired            bool     `json:"repaired,omitempty"`
	OldBranchDeleted    bool     `json:"old_branch_deleted"`
	OldRemoteDeleted    bool     `json:"old_remote_deleted"`
	PreservedCachePaths []string `json:"preserved_cache_paths,omitempty"`
	Reason              string   `json:"reason,omitempty"`
}

// RenameOutcome contains the decisions plus the durable audit report written
// before any destructive apply — see Cleanup's identical convention. A
// malformed candidate or an ineligible sibling blocks the whole task: moving
// part of a coordinated task to the new name and leaving the rest behind
// would strand exactly the recycling this verb exists to enable.
type RenameOutcome struct {
	Results     []RenameResult   `json:"results"`
	ReportPath  string           `json:"report_path,omitempty"`
	Diagnostics []ListDiagnostic `json:"diagnostics,omitempty"`
}

type renameReport struct {
	GeneratedAt        time.Time        `json:"generated_at"`
	Phase              string           `json:"phase"`
	OldTask            string           `json:"old_task"`
	NewTask            string           `json:"new_task"`
	Filter             string           `json:"filter,omitempty"`
	Branch             string           `json:"branch,omitempty"`
	Base               string           `json:"base"`
	DeleteOldBranch    bool             `json:"delete_old_branch"`
	DeleteRemote       bool             `json:"delete_remote"`
	Force              bool             `json:"force"`
	PreserveCachePaths []string         `json:"preserve_cache_paths,omitempty"`
	Apply              bool             `json:"apply"`
	Results            []RenameResult   `json:"results"`
	Diagnostics        []ListDiagnostic `json:"diagnostics,omitempty"`
}

// renamePlan bundles the validated local inventory (entry) with the public
// decision/result (result) so apply can use the former without exposing it.
type renamePlan struct {
	entry            ListResult
	refreshed        ListResult
	baseRevision     string
	remoteHead       string
	priorProjection  workLogProjection
	hadProjection    bool
	sealed           bool
	moved            bool
	newBranchCreated bool
	oldBranchDeleted bool
	remoteDeleted    bool
	result           RenameResult
}

// Rename re-homes every worktree under OldTask (optionally narrowed by
// Filter) to NewTask. It always uses `git worktree move` — with a plain move
// plus `git worktree repair` as a verified fallback — because a bare
// directory move leaves Git's own administrative gitdir pointer stale and
// `wb worktree guard` (and Git itself) rejects the result.
func Rename(ctx context.Context, options RenameOptions) (RenameOutcome, error) {
	normalized, err := normalizeRenameOptions(options)
	if err != nil {
		return RenameOutcome{}, err
	}
	resolution, err := wbhome.Resolve(normalized.ProjectsRoot)
	if err != nil {
		return RenameOutcome{}, err
	}
	normalized.WorkLog, err = PrepareWorkLogOptions(normalized.ProjectsRoot, normalized.NewTask, normalized.WorkLog)
	if err != nil {
		return RenameOutcome{}, err
	}
	if normalized.Apply {
		if err := requireGitFilesystemCapability(); err != nil {
			return RenameOutcome{}, err
		}
	}
	now := normalized.Now()
	if normalized.ReportDir == "" && normalized.Apply {
		normalized.ReportDir = DefaultRenameReportDir(resolution.Write.Home, now)
	}

	listed, err := ListWithDiagnostics(ctx, ListOptions{
		ProjectsRoot: normalized.ProjectsRoot,
		Task:         normalized.OldTask,
		Base:         normalized.Base,
		Filter:       normalized.Filter,
		GitHub:       false,
	})
	if err != nil {
		return RenameOutcome{}, err
	}
	if len(listed.Results) == 0 && len(listed.Diagnostics) == 0 {
		return RenameOutcome{}, fmt.Errorf("WB worktree task %q was not found", normalized.OldTask)
	}
	worktreesRoots := make(map[string]bool, 1)
	for _, entry := range listed.Results {
		worktreesRoots[entry.WorktreesRoot] = true
	}
	if len(worktreesRoots) > 1 {
		return RenameOutcome{}, fmt.Errorf("task %q exists under more than one WB worktrees root; rename is not supported for a split task", normalized.OldTask)
	}

	destinationTaskPath := filepath.Join(resolution.Write.WorktreesRoot, normalized.NewTask)
	destinationReason := ""
	if _, statErr := os.Lstat(destinationTaskPath); statErr == nil {
		destinationReason = fmt.Sprintf("destination task already exists: %s", destinationTaskPath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return RenameOutcome{}, fmt.Errorf("inspect destination task %s: %w", destinationTaskPath, statErr)
	}

	plans := make([]renamePlan, len(listed.Results))
	for index, entry := range listed.Results {
		owner, repository, splitErr := splitRepository(entry.Repository)
		if splitErr != nil {
			return RenameOutcome{}, splitErr
		}
		eligible, reason := renameEligibility(entry)
		plans[index] = renamePlan{entry: entry, result: RenameResult{
			OldTask: normalized.OldTask, NewTask: normalized.NewTask,
			Repository: entry.Repository, CanonicalDir: entry.CanonicalDir,
			OldWorktreeDir: entry.WorktreeDir,
			NewWorktreeDir: filepath.Join(destinationTaskPath, owner, repository),
			OldBranch:      entry.Branch, NewBranch: normalized.Branch, Base: normalized.Base,
			Eligible: eligible, Reason: reason,
		}}
	}
	blockRenameTask(plans, listed.Diagnostics, destinationReason)

	outcome := RenameOutcome{Results: collectRenameResults(plans), Diagnostics: listed.Diagnostics}
	if !normalized.Apply {
		return outcome, nil
	}

	fail := func(renameErr error) (RenameOutcome, error) {
		if normalized.ReportDir != "" {
			path, reportErr := writeRenameReport(normalized, now, "failed", outcome.Results, outcome.Diagnostics)
			if reportErr != nil {
				return outcome, fmt.Errorf("%w; write failed rename report: %v", renameErr, reportErr)
			}
			outcome.ReportPath = path
		}
		return outcome, renameErr
	}
	if normalized.ReportDir != "" {
		if _, reportErr := writeRenameReport(normalized, now, "planned", outcome.Results, outcome.Diagnostics); reportErr != nil {
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
		return fail(fmt.Errorf("no repository under task %q is eligible to rename: %s", normalized.OldTask, firstRenameReason(plans)))
	}

	oldWorktreesRoot := plans[0].entry.WorktreesRoot
	oldTaskDirectory, err := openExistingTaskDirectory(oldWorktreesRoot, normalized.OldTask)
	if err != nil {
		return fail(fmt.Errorf("open task %q: %w", normalized.OldTask, err))
	}
	defer func() { _ = oldTaskDirectory.Close() }()
	oldLock, err := acquireLockAt(oldTaskDirectory)
	if err != nil {
		return fail(fmt.Errorf("lock task %q: %w", normalized.OldTask, err))
	}
	defer oldLock.release()

	// Preflight every repository while the source task lock is held before
	// creating the destination or terminalizing the first claim. This prevents
	// a second-repository branch/fetch failure from leaving a half-recycled
	// coordinated task.
	for index := range plans {
		if !plans[index].result.Eligible {
			continue
		}
		if preflightErr := preflightRename(ctx, normalized, &plans[index]); preflightErr != nil {
			outcome.Results = collectRenameResults(plans)
			return fail(preflightErr)
		}
	}
	if err := reserveOriginalPromptArchive(resolution.Write.Home, normalized.NewTask, normalized.WorkLog); err != nil {
		return fail(fmt.Errorf("reserve new private Work Log prompt: %w", err))
	}

	newWorktreesDirectory, err := openOrCreateWorktreesRoot(resolution.Write.Home)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = newWorktreesDirectory.Close() }()
	newTaskDirectory, newTaskPath, err := createNewTaskDirectory(newWorktreesDirectory, resolution.Write.WorktreesRoot, normalized.NewTask)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = newTaskDirectory.Close() }()
	newLock, err := acquireLockAt(newTaskDirectory)
	if err != nil {
		return fail(fmt.Errorf("lock task %q: %w", normalized.NewTask, err))
	}
	defer func() { newLock.release() }()
	if err := prepareRenameDestinations(newTaskDirectory, newTaskPath, plans); err != nil {
		if cleanupErr := retireEmptyRenameDestination(newWorktreesDirectory, newTaskDirectory, &newLock, normalized.NewTask, plans); cleanupErr != nil {
			err = fmt.Errorf("%w; preserve failed destination for audit: %v", err, cleanupErr)
		}
		return fail(err)
	}

	for index := range plans {
		if !plans[index].result.Eligible {
			continue
		}
		if applyErr := applyRename(ctx, newTaskDirectory, newTaskPath, normalized, &plans[index]); applyErr != nil {
			rollbackErr := rollbackAppliedRenames(ctx, filepath.Dir(plans[index].entry.WorktreesRoot), plans[:index])
			outcome.Results = collectRenameResults(plans)
			if rollbackErr != nil {
				applyErr = fmt.Errorf("%w; coordinated rollback failed: %v", applyErr, rollbackErr)
			} else if cleanupErr := retireEmptyRenameDestination(newWorktreesDirectory, newTaskDirectory, &newLock, normalized.NewTask, plans); cleanupErr != nil {
				applyErr = fmt.Errorf("%w; rollback restored repositories but destination retirement failed: %v", applyErr, cleanupErr)
			}
			return fail(applyErr)
		}
	}
	outcome.Results = collectRenameResults(plans)

	// Keep the now-possibly-empty old task root in place while its descriptor
	// lock is live, exactly like Cleanup does: removing it after releasing the
	// lock would open an ABA window where a concurrent create makes a new,
	// unreachable task directory at the same pathname. A filtered rename that
	// leaves sibling repositories behind needs the root to stay anyway.
	if normalized.ReportDir != "" {
		outcome.ReportPath, err = writeRenameReport(normalized, now, "applied", outcome.Results, outcome.Diagnostics)
		if err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

// rollbackAppliedRenames reverses every repository already moved by this
// coordinated call, in reverse order. Its durable terminal/new-claim history
// remains append-only, but the live projection is rebound to a recovery claim
// at the original path so the same command can be retried. This is the normal
// error transaction; process crashes remain recoverable from durable records
// but are not yet automatically replayed.
func rollbackAppliedRenames(ctx context.Context, home string, plans []renamePlan) error {
	var rollbackErrors []string
	for index := len(plans) - 1; index >= 0; index-- {
		plan := &plans[index]
		if !plan.result.Applied {
			continue
		}
		if err := rollbackRenamePlan(ctx, home, plan); err != nil {
			rollbackErrors = append(rollbackErrors, plan.entry.Repository+": "+err.Error())
			continue
		}
		resetRenameResultAfterRollback(plan)
	}
	if len(rollbackErrors) > 0 {
		return errors.New(strings.Join(rollbackErrors, "; "))
	}
	return nil
}

func renameEligibility(entry ListResult) (bool, string) {
	switch {
	case entry.Locked:
		return false, "task is locked by an active or interrupted operation"
	case !entry.Clean:
		return false, "worktree has local changes"
	default:
		return true, ""
	}
}

// blockRenameTask makes the whole rename all-or-nothing. It mirrors Cleanup's
// coordinated per-task blocking (see blockDiagnosedTasks/blockUnsafeTasks),
// simplified because a rename call is always scoped to exactly one task. A
// destination collision is an absolute blocker checked first: it means
// nothing about this task's own repositories, so it is reported verbatim
// rather than wrapped as "coordinated task blocked by ...".
func blockRenameTask(plans []renamePlan, diagnostics []ListDiagnostic, destinationReason string) {
	if destinationReason != "" {
		for index := range plans {
			plans[index].result.Eligible = false
			plans[index].result.Reason = destinationReason
		}
		return
	}
	reason := ""
	switch {
	case len(diagnostics) > 0:
		reason = "malformed candidate " + diagnostics[0].Path + ": " + diagnostics[0].Message
	default:
		for _, plan := range plans {
			if !plan.result.Eligible {
				reason = plan.result.Repository + ": " + plan.result.Reason
				break
			}
		}
	}
	if reason == "" {
		return
	}
	for index := range plans {
		if plans[index].result.Eligible {
			plans[index].result.Eligible = false
			plans[index].result.Reason = "coordinated task blocked by " + reason
		}
	}
}

func collectRenameResults(plans []renamePlan) []RenameResult {
	results := make([]RenameResult, len(plans))
	for index, plan := range plans {
		results[index] = plan.result
	}
	return results
}

func firstRenameReason(plans []renamePlan) string {
	for _, plan := range plans {
		if plan.result.Reason != "" {
			return plan.result.Reason
		}
	}
	return ""
}

// applyRename moves one repository's worktree and switches it onto a freshly
// created branch. newTaskDirectory/newTaskPath are the already-created,
// already-locked destination task directory shared by every repository in
// this Rename call.
func applyRename(ctx context.Context, newTaskDirectory *os.File, newTaskPath string, options RenameOptions, plan *renamePlan) (returnErr error) {
	owner, repository, err := splitRepository(plan.entry.Repository)
	if err != nil {
		return err
	}

	// Recheck safety immediately before mutating under the source task lock.
	refreshed, err := inspectLifecycleWorktree(
		ctx, options.ProjectsRoot, wbhome.Layout{WorktreesRoot: plan.entry.WorktreesRoot},
		options.OldTask, plan.entry.WorktreeDir, options.Base, false, false,
	)
	if err != nil {
		return fmt.Errorf("recheck %s before renaming: %w", plan.entry.Repository, err)
	}
	if !refreshed.Clean {
		return fmt.Errorf("rename safety changed for %s: worktree has local changes", refreshed.Repository)
	}
	if refreshed.HeadSHA != plan.entry.HeadSHA {
		return fmt.Errorf("rename safety changed for %s: branch head moved", refreshed.Repository)
	}
	if err := verifyRecycleState(ctx, plan.entry.WorktreeDir, options.PreserveCachePaths); err != nil {
		return fmt.Errorf("prepare %s for recycle: %w", refreshed.Repository, err)
	}
	if refreshed.HeadSHA != plan.refreshed.HeadSHA {
		return fmt.Errorf("rename safety changed for %s after coordinated preflight", refreshed.Repository)
	}
	home := filepath.Dir(plan.entry.WorktreesRoot)
	priorProjection, projectionErr := readWorkLogProjectionForClaim(home, plan.entry.WorktreeDir)
	plan.priorProjection = priorProjection
	plan.hadProjection = projectionErr == nil
	if projectionErr != nil && !errors.Is(projectionErr, errWorkLogProjectionNotFound) {
		return projectionErr
	}
	defer func() {
		if returnErr == nil || !plan.sealed {
			return
		}
		if rollbackErr := rollbackRenamePlan(ctx, home, plan); rollbackErr != nil {
			returnErr = fmt.Errorf("%w; deterministic recycle rollback failed: %v", returnErr, rollbackErr)
		} else {
			resetRenameResultAfterRollback(plan)
		}
	}()
	if err := sealWorkLogForRecycle(home, plan.entry.WorktreeDir, refreshed.HeadSHA, "recycled"); err != nil {
		return fmt.Errorf("seal previous work log for %s: %w", refreshed.Repository, err)
	}
	plan.sealed = true
	if err := removeWorkLogProjection(plan.entry.WorktreeDir); err != nil {
		return err
	}
	currentRemoteHead, err := remoteBranchHead(ctx, plan.entry.CanonicalDir, plan.entry.Branch)
	if err != nil {
		return fmt.Errorf("recheck remote branch before recycling %s: %w", plan.entry.Repository, err)
	}
	if currentRemoteHead != plan.remoteHead || (currentRemoteHead != "" && currentRemoteHead != refreshed.HeadSHA) {
		return fmt.Errorf("recycle safety changed for %s: remote branch moved from %q to %q", plan.entry.Repository, plan.remoteHead, currentRemoteHead)
	}
	if currentRemoteHead != "" {
		if !options.DeleteRemote {
			return fmt.Errorf("origin/%s still exists; recycle requires explicit --remote retirement", plan.entry.Branch)
		}
		canonical, openErr := openCanonicalRepository(plan.entry.CanonicalDir)
		if openErr != nil {
			return openErr
		}
		deleteErr := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "",
			"push", "--force-with-lease=refs/heads/"+plan.entry.Branch+":"+refreshed.HeadSHA, "origin", ":refs/heads/"+plan.entry.Branch)
		canonical.close()
		if deleteErr != nil {
			return fmt.Errorf("retire old remote branch %s at %s: %w", plan.entry.Branch, refreshed.HeadSHA, deleteErr)
		}
		plan.remoteDeleted = true
		plan.result.OldRemoteDeleted = true
	}

	if !directoryStillMatches(newTaskPath, newTaskDirectory) {
		return fmt.Errorf("destination task path changed before creating owner directory: %s", newTaskPath)
	}
	ownerFD, err := openOrCreateNoFollowDirectory(int(newTaskDirectory.Fd()), owner)
	if err != nil {
		return fmt.Errorf("create destination owner directory: %w", err)
	}
	ownerDirectory := os.NewFile(uintptr(ownerFD), "wb-rename-owner")
	if ownerDirectory == nil {
		_ = unix.Close(ownerFD)
		return fmt.Errorf("wrap destination owner directory %s", owner)
	}
	defer func() { _ = ownerDirectory.Close() }()
	if err := requireAbsentNoFollowChild(ownerFD, repository); err != nil {
		return err
	}

	repaired, err := moveWorktree(ctx, plan.entry.CanonicalDir, plan.entry.WorktreeDir, plan.result.NewWorktreeDir)
	if err != nil {
		return err
	}
	plan.moved = true
	plan.result.Repaired = repaired
	plan.result.PreservedCachePaths = append([]string(nil), options.PreserveCachePaths...)

	if _, checkoutErr := git(ctx, plan.result.NewWorktreeDir, "checkout", "-b", plan.result.NewBranch, plan.baseRevision); checkoutErr != nil {
		return fmt.Errorf("check out new branch %s in %s: %w", plan.result.NewBranch, plan.result.NewWorktreeDir, checkoutErr)
	}
	plan.newBranchCreated = true

	if _, guardErr := Guard(ctx, plan.result.NewWorktreeDir, GuardOptions{ProjectsRoot: options.ProjectsRoot, Base: options.Base}); guardErr != nil {
		return fmt.Errorf("renamed worktree %s failed guard: %w", plan.result.NewWorktreeDir, guardErr)
	}
	deleted, _, deleteErr := deleteOldBranchIfSafe(
		ctx, plan.entry.CanonicalDir, plan.entry.Branch, refreshed.HeadSHA, plan.result.NewBranch, options.Base, options.Force,
	)
	if deleteErr != nil {
		return deleteErr
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
	if _, logErr := recordWorkLog(filepath.Dir(filepath.Dir(newTaskPath)), options.NewTask, CreateResult{
		Repository: plan.entry.Repository, CanonicalDir: plan.entry.CanonicalDir,
		WorktreeDir: plan.result.NewWorktreeDir, Branch: plan.result.NewBranch, Base: options.Base,
		BaseSHA: plan.baseRevision, Action: "recycled",
	}, options.WorkLog); logErr != nil {
		return fmt.Errorf("bind recycled worktree to a new work log: %w", logErr)
	}
	plan.result.Applied = true
	return nil
}

func prepareRenameDestinations(taskDirectory *os.File, taskPath string, plans []renamePlan) error {
	for _, plan := range plans {
		if !plan.result.Eligible {
			continue
		}
		owner, repository, err := splitRepository(plan.entry.Repository)
		if err != nil {
			return err
		}
		if !directoryStillMatches(taskPath, taskDirectory) {
			return fmt.Errorf("destination task path changed before preparing %s", plan.entry.Repository)
		}
		ownerFD, err := openOrCreateNoFollowDirectory(int(taskDirectory.Fd()), owner)
		if err != nil {
			return fmt.Errorf("prepare destination owner %s: %w", owner, err)
		}
		ownerDirectory := os.NewFile(uintptr(ownerFD), "wb-rename-preflight-owner")
		if ownerDirectory == nil {
			_ = unix.Close(ownerFD)
			return fmt.Errorf("wrap destination owner %s", owner)
		}
		if err := requireAbsentNoFollowChild(ownerFD, repository); err != nil {
			_ = ownerDirectory.Close()
			return err
		}
		_ = ownerDirectory.Close()
	}
	return nil
}

// retireEmptyRenameDestination atomically removes a rolled-back destination
// from the active task namespace while its exact lock and directory
// descriptors are still held. The directory is first renamed to an opaque
// retirement name, so even a later cleanup failure cannot strand NewTask or
// block an exact retry. Any unexpected entry makes WB preserve the directory
// and its lock for explicit recovery rather than deleting unknown state.
func retireEmptyRenameDestination(
	worktreesDirectory, taskDirectory *os.File,
	lock *operationLock,
	task string,
	plans []renamePlan,
) error {
	owners := map[string]bool{}
	for _, plan := range plans {
		owner, _, err := splitRepository(plan.entry.Repository)
		if err == nil {
			owners[owner] = true
		}
	}
	for owner := range owners {
		fd, err := unix.Openat(int(taskDirectory.Fd()), owner, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect rollback destination owner %s: %w", owner, err)
		}
		ownerDirectory := os.NewFile(uintptr(fd), "wb-rename-retire-owner")
		if ownerDirectory == nil {
			_ = unix.Close(fd)
			return fmt.Errorf("wrap rollback destination owner %s", owner)
		}
		entries, readErr := ownerDirectory.ReadDir(-1)
		_ = ownerDirectory.Close()
		if readErr != nil {
			return fmt.Errorf("inspect rollback destination owner %s: %w", owner, readErr)
		}
		if len(entries) != 0 {
			return fmt.Errorf("rollback destination owner %s contains unexpected state", owner)
		}
		if err := unix.Unlinkat(int(taskDirectory.Fd()), owner, unix.AT_REMOVEDIR); err != nil {
			return fmt.Errorf("remove empty rollback destination owner %s: %w", owner, err)
		}
	}
	if lock == nil || lock.file == nil || !lockEntryStillMatches(taskDirectory, ".lock", lock.identity) {
		return fmt.Errorf("rollback destination lock changed before retirement")
	}
	if !directoryEntryStillMatches(worktreesDirectory, task, taskDirectory) {
		return fmt.Errorf("rollback destination task changed before retirement")
	}
	retired, retiredName, err := quarantineDirectoryEntryNamed(worktreesDirectory, task, taskDirectory, ".wb-retired-task-")
	if err != nil {
		return fmt.Errorf("retire rollback destination task: %w", err)
	}
	defer func() { _ = retired.Close() }()
	lock.release()
	lock.file = nil
	lock.directory = nil
	if _, err := retired.Seek(0, 0); err != nil {
		return fmt.Errorf("rewind retired destination: %w", err)
	}
	entries, err := retired.ReadDir(-1)
	if err != nil {
		return fmt.Errorf("inspect retired destination: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".wb-retired-lock-") || !lockEntryStillMatches(retired, entry.Name(), lock.identity) {
			return fmt.Errorf("retired destination contains unexpected state %q", entry.Name())
		}
		if err := unix.Unlinkat(int(retired.Fd()), entry.Name(), 0); err != nil {
			return fmt.Errorf("remove retired destination lock: %w", err)
		}
	}
	if !directoryEntryStillMatches(worktreesDirectory, retiredName, retired) {
		return fmt.Errorf("retired destination identity changed before removal")
	}
	if err := unix.Unlinkat(int(worktreesDirectory.Fd()), retiredName, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove retired destination: %w", err)
	}
	return worktreesDirectory.Sync()
}

func rollbackRenamePlan(ctx context.Context, home string, plan *renamePlan) error {
	currentPath := plan.entry.WorktreeDir
	if plan.moved {
		currentPath = plan.result.NewWorktreeDir
	}
	// If a fresh claim was partially or fully bound, archive it before removing
	// its projection. Failure here stops rollback rather than losing evidence.
	if projection, err := readWorkLogProjection(currentPath); err == nil && projection.ClaimID != plan.priorProjection.ClaimID {
		head, headErr := git(ctx, currentPath, "rev-parse", "HEAD")
		if headErr != nil {
			return headErr
		}
		if err := sealWorkLogForRecycle(home, currentPath, head, "recycle_failed"); err != nil {
			return err
		}
		if err := removeWorkLogProjection(currentPath); err != nil {
			return err
		}
	}
	if plan.oldBranchDeleted {
		if _, err := git(ctx, plan.entry.CanonicalDir, "update-ref", "refs/heads/"+plan.entry.Branch, plan.entry.HeadSHA, ""); err != nil {
			return fmt.Errorf("restore old branch %s: %w", plan.entry.Branch, err)
		}
	}
	if plan.remoteDeleted {
		remoteHead, err := remoteBranchHead(ctx, plan.entry.CanonicalDir, plan.entry.Branch)
		if err != nil {
			return fmt.Errorf("inspect remote before recycle rollback: %w", err)
		}
		if remoteHead != "" {
			return fmt.Errorf("refuse to restore remote branch %s: another actor created it at %s", plan.entry.Branch, remoteHead)
		}
		canonical, err := openCanonicalRepository(plan.entry.CanonicalDir)
		if err != nil {
			return err
		}
		restoreErr := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "",
			"push", "--force-with-lease=refs/heads/"+plan.entry.Branch+":", "origin",
			"refs/heads/"+plan.entry.Branch+":refs/heads/"+plan.entry.Branch)
		canonical.close()
		if restoreErr != nil {
			return fmt.Errorf("restore retired remote branch %s at %s: %w", plan.entry.Branch, plan.entry.HeadSHA, restoreErr)
		}
		restoredHead, err := remoteBranchHead(ctx, plan.entry.CanonicalDir, plan.entry.Branch)
		if err != nil {
			return err
		}
		if restoredHead != plan.entry.HeadSHA {
			return fmt.Errorf("restored remote branch %s is %s, expected %s", plan.entry.Branch, restoredHead, plan.entry.HeadSHA)
		}
	}
	if plan.moved {
		branch, err := git(ctx, currentPath, "branch", "--show-current")
		if err != nil {
			return err
		}
		if branch != plan.entry.Branch {
			if _, err := git(ctx, currentPath, "checkout", plan.entry.Branch); err != nil {
				return fmt.Errorf("restore old branch checkout: %w", err)
			}
		}
		if _, err := moveWorktree(ctx, plan.entry.CanonicalDir, plan.result.NewWorktreeDir, plan.entry.WorktreeDir); err != nil {
			return fmt.Errorf("move failed recycle back to source: %w", err)
		}
	}
	if exists, err := localBranchExists(ctx, plan.entry.CanonicalDir, plan.result.NewBranch); err != nil {
		return err
	} else if exists {
		newHead, err := git(ctx, plan.entry.CanonicalDir, "rev-parse", "refs/heads/"+plan.result.NewBranch)
		if err != nil {
			return err
		}
		if newHead != plan.baseRevision {
			return fmt.Errorf("refuse to remove failed recycle branch %s: expected %s, found %s", plan.result.NewBranch, plan.baseRevision, newHead)
		}
		if _, err := git(ctx, plan.entry.CanonicalDir, "update-ref", "-d", "refs/heads/"+plan.result.NewBranch, plan.baseRevision); err != nil {
			return fmt.Errorf("remove failed recycle branch: %w", err)
		}
	}
	if plan.hadProjection {
		if err := recoverFailedRecycleClaim(home, plan.entry.WorktreeDir, plan.entry.HeadSHA, plan.priorProjection); err != nil {
			return fmt.Errorf("bind recovery claim after failed recycle: %w", err)
		}
	}
	return nil
}

func resetRenameResultAfterRollback(plan *renamePlan) {
	plan.result.Applied = false
	plan.result.OldBranchDeleted = false
	plan.result.OldRemoteDeleted = false
	plan.result.Repaired = false
	plan.result.PreservedCachePaths = nil
	plan.sealed = false
	plan.moved = false
	plan.newBranchCreated = false
	plan.oldBranchDeleted = false
	plan.remoteDeleted = false
}

func preflightRename(ctx context.Context, options RenameOptions, plan *renamePlan) error {
	refreshed, err := inspectLifecycleWorktree(ctx, options.ProjectsRoot,
		wbhome.Layout{WorktreesRoot: plan.entry.WorktreesRoot}, options.OldTask,
		plan.entry.WorktreeDir, options.Base, false, false)
	if err != nil {
		return fmt.Errorf("preflight %s: %w", plan.entry.Repository, err)
	}
	if !refreshed.Clean || refreshed.HeadSHA != plan.entry.HeadSHA {
		return fmt.Errorf("preflight %s: worktree/head changed", plan.entry.Repository)
	}
	if err := verifyRecycleState(ctx, refreshed.WorktreeDir, options.PreserveCachePaths); err != nil {
		return fmt.Errorf("preflight %s: %w", plan.entry.Repository, err)
	}
	canonical, err := openCanonicalRepository(plan.entry.CanonicalDir)
	if err != nil {
		return err
	}
	defer canonical.close()
	baseRevision, err := synchronizeCanonical(ctx, canonical, plan.entry.Repository, options.Base)
	if err != nil {
		return fmt.Errorf("fetch base before recycling %s: %w", plan.entry.Repository, err)
	}
	if exists, existsErr := localBranchExistsCanonical(ctx, canonical, plan.result.NewBranch); existsErr != nil {
		return existsErr
	} else if exists {
		return fmt.Errorf("branch %q already exists in %s; choose another --branch", plan.result.NewBranch, plan.entry.Repository)
	}
	merged, err := isAncestor(ctx, plan.entry.CanonicalDir, refreshed.HeadSHA, "origin/"+options.Base)
	if err != nil {
		return err
	}
	if !merged && !options.Force {
		return fmt.Errorf("branch %q is not integrated into origin/%s; use `wb worktree abort --disposition handoff|not_landed`, or explicitly authorize discard before recycle", refreshed.Branch, options.Base)
	}
	remoteHead, err := remoteBranchHead(ctx, plan.entry.CanonicalDir, refreshed.Branch)
	if err != nil {
		return fmt.Errorf("inspect old remote branch before recycling %s: %w", plan.entry.Repository, err)
	}
	if remoteHead != "" && remoteHead != refreshed.HeadSHA {
		return fmt.Errorf("refuse to recycle %s: origin/%s is %s, expected exact old head %s", refreshed.Repository, refreshed.Branch, remoteHead, refreshed.HeadSHA)
	}
	if remoteHead != "" && !options.DeleteRemote {
		return fmt.Errorf("origin/%s remains cleanup backlog; rerun recycle with --remote", refreshed.Branch)
	}
	if err := preflightWorkLogSeal(filepath.Dir(plan.entry.WorktreesRoot), refreshed.WorktreeDir, refreshed.HeadSHA); err != nil {
		return fmt.Errorf("preflight Work Log for %s: %w", plan.entry.Repository, err)
	}
	plan.refreshed = refreshed
	plan.baseRevision = baseRevision
	plan.remoteHead = remoteHead
	return nil
}

// verifyRecycleState proves that only explicitly allow-listed cache paths are
// ignored or untracked. It deliberately refuses rather than deleting unknown
// files: recycle must never turn a stale agent's local evidence into silent
// data loss. The caller can archive or remove that state, then retry.
func verifyRecycleState(ctx context.Context, worktree string, preserve []string) error {
	args := []string{"clean", "-ndx"}
	// The projection is reset after the new branch has been created; it is WB
	// control-plane metadata, not a cache inherited by the new effort.
	args = append(args, "-e", workLogProjectionDirectory, "-e", legacyWorkLogProjectionName)
	for _, path := range preserve {
		args = append(args, "-e", path)
	}
	remaining, err := git(ctx, worktree, args...)
	if err != nil {
		return err
	}
	if remaining != "" {
		return fmt.Errorf("unapproved untracked or ignored state would leak into the new effort: %s; archive/remove it or explicitly preserve a safe cache path", remaining)
	}
	return nil
}

// moveWorktree relocates a worktree using `git worktree move`, which is the
// only operation that keeps Git's own gitdir pointer correct: a bare
// filesystem move leaves `.git/worktrees/<name>/gitdir` in the canonical
// clone pointing at the old location, so Git and `wb worktree guard` both
// treat the worktree as prunable/misplaced afterwards. If Git's own move
// refuses, fall back to a plain move plus `git worktree repair`, then verify
// the registration either way with `git worktree list`.
func moveWorktree(ctx context.Context, canonicalDir, oldPath, newPath string) (repaired bool, err error) {
	if _, moveErr := git(ctx, canonicalDir, "worktree", "move", oldPath, newPath); moveErr != nil {
		info, statErr := os.Stat(oldPath)
		switch {
		case statErr == nil && info.IsDir():
			if renameErr := os.Rename(oldPath, newPath); renameErr != nil {
				return false, fmt.Errorf("git worktree move failed (%v) and fallback move failed: %w", moveErr, renameErr)
			}
		case statErr != nil && errors.Is(statErr, os.ErrNotExist):
			if _, newStatErr := os.Stat(newPath); newStatErr != nil {
				return false, fmt.Errorf("git worktree move left neither %s nor %s in place: %w", oldPath, newPath, moveErr)
			}
			// Git relocated the directory before failing to update its own
			// bookkeeping; the repair below still needs to run.
		default:
			return false, fmt.Errorf("git worktree move failed (%v) and could not inspect %s: %w", moveErr, oldPath, statErr)
		}
		if _, repairErr := git(ctx, canonicalDir, "worktree", "repair", newPath); repairErr != nil {
			return false, fmt.Errorf("git worktree move failed (%v); repair after fallback move failed: %w", moveErr, repairErr)
		}
		repaired = true
	}
	if verifyErr := verifyWorktreeRegistered(ctx, canonicalDir, oldPath, newPath); verifyErr != nil {
		return repaired, verifyErr
	}
	return repaired, nil
}

// verifyWorktreeRegistered proves the move actually took, straight from
// Git's own bookkeeping, rather than trusting a zero exit code alone.
func verifyWorktreeRegistered(ctx context.Context, canonicalDir, oldPath, newPath string) error {
	output, err := git(ctx, canonicalDir, "worktree", "list", "--porcelain")
	if err != nil {
		return fmt.Errorf("verify worktree registration: %w", err)
	}
	found := false
	for _, line := range strings.Split(output, "\n") {
		path, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		switch filepath.Clean(path) {
		case filepath.Clean(newPath):
			found = true
		case filepath.Clean(oldPath):
			return fmt.Errorf("worktree registration still lists the old path %s after moving to %s", oldPath, newPath)
		}
	}
	if !found {
		return fmt.Errorf("worktree registration does not list %s after moving %s", newPath, oldPath)
	}
	return nil
}

// deleteOldBranchIfSafe removes oldBranch once it is safe to lose: merged
// into origin/base, or Force is set. It never touches newBranch even if the
// caller asked for the same name, and it deletes by exact expected SHA (like
// Cleanup's own branch deletion) so a branch that moved after the safety
// check is refused rather than silently discarded.
func deleteOldBranchIfSafe(ctx context.Context, canonicalDir, oldBranch, oldHead, newBranch, base string, force bool) (deleted bool, reason string, err error) {
	if oldBranch == "" || oldBranch == newBranch {
		return false, fmt.Sprintf("old branch %q is unchanged; nothing to delete", oldBranch), nil
	}
	merged, err := isAncestor(ctx, canonicalDir, oldHead, "origin/"+base)
	if err != nil {
		return false, "", err
	}
	if !merged && !force {
		return false, fmt.Sprintf("branch %q is not merged into origin/%s; rerun with --force to delete it anyway", oldBranch, base), nil
	}
	if _, updateErr := git(ctx, canonicalDir, "update-ref", "-d", "refs/heads/"+oldBranch, oldHead); updateErr != nil {
		return false, "", fmt.Errorf("delete old branch %s at %s: %w", oldBranch, oldHead, updateErr)
	}
	return true, "", nil
}

func normalizeRenameOptions(options RenameOptions) (RenameOptions, error) {
	projectsRoot, oldTask, base, filter, err := normalizeListOptions(ListOptions{
		ProjectsRoot: options.ProjectsRoot, Task: options.OldTask, Base: options.Base, Filter: options.Filter,
	})
	if err != nil {
		return RenameOptions{}, err
	}
	if oldTask == "" {
		return RenameOptions{}, fmt.Errorf("old task is required")
	}
	options.ProjectsRoot = projectsRoot
	options.OldTask = oldTask
	options.Base = base
	options.Filter = filter

	options.NewTask = strings.TrimSpace(options.NewTask)
	if !validSafeSegment(options.NewTask) {
		return RenameOptions{}, fmt.Errorf("task %q must be one safe path segment", options.NewTask)
	}
	if options.NewTask == options.OldTask {
		return RenameOptions{}, fmt.Errorf("new task %q must differ from old task %q", options.NewTask, options.OldTask)
	}
	if strings.TrimSpace(options.Branch) == "" {
		options.Branch = "codex/" + options.NewTask
	}
	options.Branch = strings.TrimSpace(options.Branch)
	ctx := context.Background()
	if !validBranch(ctx, options.Branch) {
		return RenameOptions{}, fmt.Errorf("invalid feature branch %q", options.Branch)
	}
	if options.Branch == options.Base {
		return RenameOptions{}, fmt.Errorf("feature branch must differ from base branch %q", options.Base)
	}
	cachePaths, err := normalizePreserveCachePaths(options.PreserveCachePaths)
	if err != nil {
		return RenameOptions{}, err
	}
	options.PreserveCachePaths = cachePaths
	options.DeleteOldBranch = true
	if options.Apply && !options.DeleteRemote {
		return RenameOptions{}, fmt.Errorf("recycle apply requires --remote so an old source branch cannot remain cleanup backlog")
	}
	if strings.TrimSpace(options.WorkLog.RunID) == "" {
		options.WorkLog.RunID = "wb-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	}
	if err := PreflightWorkLogOptions(options.NewTask, options.WorkLog); err != nil {
		return RenameOptions{}, err
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ReportDir != "" {
		options.ReportDir, err = filepath.Abs(options.ReportDir)
		if err != nil {
			return RenameOptions{}, fmt.Errorf("resolve rename report directory: %w", err)
		}
		options.ReportDir = filepath.Clean(options.ReportDir)
	}
	return options, nil
}

func normalizePreserveCachePaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		path = filepath.ToSlash(strings.TrimSpace(path))
		if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "//") {
			return nil, fmt.Errorf("preserve cache path %q must be a non-empty repository-relative path", path)
		}
		parts := strings.Split(path, "/")
		for _, part := range parts {
			if !validSafeSegment(part) || part == "." || part == ".." {
				return nil, fmt.Errorf("preserve cache path %q contains an unsafe segment", path)
			}
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

// DefaultRenameReportDir returns the durable audit directory for one apply,
// below the already-resolved WB write home — see DefaultCleanupReportDir.
func DefaultRenameReportDir(home string, now time.Time) string {
	return filepath.Join(
		home,
		"reports",
		"worktree-rename",
		now.UTC().Format("20060102T150405.000000000Z"),
	)
}

// openExistingTaskDirectory opens an already-registered task directory
// without following a symlink at either segment, mirroring
// acquireCleanupTask's identical two-step open.
func openExistingTaskDirectory(worktreesRoot, task string) (*os.File, error) {
	root, err := openAbsoluteDirectoryNoFollow(worktreesRoot, false)
	if err != nil {
		return nil, fmt.Errorf("open worktrees root %s: %w", worktreesRoot, err)
	}
	defer func() { _ = root.Close() }()
	taskFD, err := unix.Openat(int(root.Fd()), task, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open task %s without following links: %w", task, err)
	}
	directory := os.NewFile(uintptr(taskFD), "wb-rename-old-task")
	if directory == nil {
		_ = unix.Close(taskFD)
		return nil, fmt.Errorf("wrap task directory %s", task)
	}
	return directory, nil
}

// openOrCreateWorktreesRoot opens (creating if absent) <home>/worktrees one
// segment at a time, mirroring the equivalent half of prepareOperationRoot.
func openOrCreateWorktreesRoot(home string) (*os.File, error) {
	homeDirectory, err := openAbsoluteDirectoryNoFollow(home, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = homeDirectory.Close() }()
	if err := wbhome.SeedReadme(home); err != nil {
		return nil, err
	}
	worktreesFD, err := openOrCreateNoFollowDirectory(int(homeDirectory.Fd()), "worktrees")
	if err != nil {
		return nil, err
	}
	worktreesDirectory := os.NewFile(uintptr(worktreesFD), "wb-rename-worktrees-root")
	if worktreesDirectory == nil {
		_ = unix.Close(worktreesFD)
		return nil, fmt.Errorf("wrap secure worktrees root")
	}
	return worktreesDirectory, nil
}

// createNewTaskDirectory creates the destination task directory atomically:
// unix.Mkdirat fails with EEXIST rather than silently reusing an existing
// directory, which is what turns "the destination task already exists" into
// a hard, race-free refusal instead of a plan-time-only check.
func createNewTaskDirectory(worktreesDirectory *os.File, worktreesRootPath, task string) (*os.File, string, error) {
	path := filepath.Join(worktreesRootPath, task)
	if !directoryStillMatches(worktreesRootPath, worktreesDirectory) {
		return nil, "", fmt.Errorf("secure worktrees root path changed before creating task %s", task)
	}
	if err := unix.Mkdirat(int(worktreesDirectory.Fd()), task, 0o755); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return nil, "", fmt.Errorf("destination task already exists: %s", path)
		}
		return nil, "", fmt.Errorf("create task directory %s: %w", path, err)
	}
	fd, err := unix.Openat(int(worktreesDirectory.Fd()), task, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open created task directory %s: %w", path, err)
	}
	directory := os.NewFile(uintptr(fd), "wb-rename-new-task")
	if directory == nil {
		_ = unix.Close(fd)
		return nil, "", fmt.Errorf("wrap created task directory %s", path)
	}
	return directory, path, nil
}

func writeRenameReport(
	options RenameOptions,
	generatedAt time.Time,
	phase string,
	results []RenameResult,
	diagnostics []ListDiagnostic,
) (string, error) {
	if err := os.MkdirAll(options.ReportDir, 0o755); err != nil {
		return "", fmt.Errorf("create rename report directory: %w", err)
	}
	report := renameReport{
		GeneratedAt: generatedAt, Phase: phase, OldTask: options.OldTask, NewTask: options.NewTask,
		Filter: options.Filter, Branch: options.Branch, Base: options.Base,
		DeleteOldBranch: options.DeleteOldBranch, DeleteRemote: options.DeleteRemote,
		Force: options.Force, PreserveCachePaths: options.PreserveCachePaths, Apply: options.Apply,
		Results: results, Diagnostics: diagnostics,
	}
	content, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode rename report: %w", err)
	}
	content = append(content, '\n')
	path := filepath.Join(options.ReportDir, "rename.json")
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, content, 0o644); err != nil {
		return "", fmt.Errorf("write rename report: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return "", fmt.Errorf("activate rename report: %w", err)
	}
	return path, nil
}
