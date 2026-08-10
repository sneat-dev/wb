package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
)

// RenameOptions controls re-homing every worktree below one task to a new
// task name. Recycling a worktree this way — rather than deleting it and
// running `wb worktree create` again — is the whole point of the verb: it
// preserves working-tree contents such as node_modules, build caches, and a
// Python .venv, which are often the expensive part of setting a task up.
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
	// DeleteOldBranch removes the branch each worktree was on once its move
	// succeeds. An unmerged branch is refused unless Force is set.
	DeleteOldBranch bool
	Force           bool
	// Apply performs the rename. The default is a dry-run plan, exactly like
	// `wb worktree cleanup`.
	Apply     bool
	ReportDir string
	Now       func() time.Time
}

// RenameResult records one repository's rename decision and outcome.
type RenameResult struct {
	OldTask          string `json:"old_task"`
	NewTask          string `json:"new_task"`
	Repository       string `json:"repository"`
	CanonicalDir     string `json:"canonical_dir"`
	OldWorktreeDir   string `json:"old_worktree_dir"`
	NewWorktreeDir   string `json:"new_worktree_dir"`
	OldBranch        string `json:"old_branch"`
	NewBranch        string `json:"new_branch"`
	Base             string `json:"base"`
	Eligible         bool   `json:"eligible"`
	Applied          bool   `json:"applied"`
	Repaired         bool   `json:"repaired,omitempty"`
	OldBranchDeleted bool   `json:"old_branch_deleted"`
	Reason           string `json:"reason,omitempty"`
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
	GeneratedAt     time.Time        `json:"generated_at"`
	Phase           string           `json:"phase"`
	OldTask         string           `json:"old_task"`
	NewTask         string           `json:"new_task"`
	Filter          string           `json:"filter,omitempty"`
	Branch          string           `json:"branch,omitempty"`
	Base            string           `json:"base"`
	DeleteOldBranch bool             `json:"delete_old_branch"`
	Force           bool             `json:"force"`
	Apply           bool             `json:"apply"`
	Results         []RenameResult   `json:"results"`
	Diagnostics     []ListDiagnostic `json:"diagnostics,omitempty"`
}

// renamePlan bundles the validated local inventory (entry) with the public
// decision/result (result) so apply can use the former without exposing it.
type renamePlan struct {
	entry  ListResult
	result RenameResult
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
			if _, reportErr := writeRenameReport(normalized, now, "failed", outcome.Results, outcome.Diagnostics); reportErr != nil {
				return outcome, fmt.Errorf("%w; write failed rename report: %v", renameErr, reportErr)
			}
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
	defer newLock.release()

	for index := range plans {
		if !plans[index].result.Eligible {
			continue
		}
		if applyErr := applyRename(ctx, newTaskDirectory, newTaskPath, normalized, &plans[index]); applyErr != nil {
			outcome.Results = collectRenameResults(plans)
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
func applyRename(ctx context.Context, newTaskDirectory *os.File, newTaskPath string, options RenameOptions, plan *renamePlan) error {
	owner, repository, err := splitRepository(plan.entry.Repository)
	if err != nil {
		return err
	}

	// Recheck safety immediately before mutating: dirty/locked could have
	// changed between planning and this apply. false/false matches Cleanup's
	// own refresh call exactly — GitHub state is irrelevant here, and the task
	// is already locked by this very operation.
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

	canonical, err := openCanonicalRepository(plan.entry.CanonicalDir)
	if err != nil {
		return fmt.Errorf("open canonical repository %s: %w", plan.entry.CanonicalDir, err)
	}
	defer canonical.close()
	// Pulling here, exactly like Create's synchronizeCanonical, guarantees the
	// freshly created branch starts from the latest remote base rather than a
	// possibly stale local one.
	if err := synchronizeCanonical(ctx, canonical, plan.entry.Repository, options.Base); err != nil {
		return fmt.Errorf("update base before renaming %s: %w", plan.entry.Repository, err)
	}
	if exists, existsErr := localBranchExistsCanonical(ctx, canonical, plan.result.NewBranch); existsErr != nil {
		return existsErr
	} else if exists {
		return fmt.Errorf("branch %q already exists in %s; choose another --branch", plan.result.NewBranch, plan.entry.Repository)
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
	plan.result.Repaired = repaired

	if _, checkoutErr := git(ctx, plan.result.NewWorktreeDir, "checkout", "-b", plan.result.NewBranch, "origin/"+options.Base); checkoutErr != nil {
		return fmt.Errorf("check out new branch %s in %s: %w", plan.result.NewBranch, plan.result.NewWorktreeDir, checkoutErr)
	}

	if _, guardErr := Guard(ctx, plan.result.NewWorktreeDir, GuardOptions{ProjectsRoot: options.ProjectsRoot, Base: options.Base}); guardErr != nil {
		return fmt.Errorf("renamed worktree %s failed guard: %w", plan.result.NewWorktreeDir, guardErr)
	}

	plan.result.Applied = true

	if options.DeleteOldBranch {
		deleted, reason, deleteErr := deleteOldBranchIfSafe(
			ctx, plan.entry.CanonicalDir, plan.entry.Branch, refreshed.HeadSHA, plan.result.NewBranch, options.Base, options.Force,
		)
		if deleteErr != nil {
			return deleteErr
		}
		plan.result.OldBranchDeleted = deleted
		if !deleted {
			plan.result.Reason = reason
		}
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
		DeleteOldBranch: options.DeleteOldBranch, Force: options.Force, Apply: options.Apply,
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
