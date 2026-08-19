package worktrees

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// RetireTaskShells retires pre-existing task-namespace shells left behind by
// terminal cleanups that predate the residue fix in Cleanup: an empty
// owner-namespace directory (for example <task>/sneat-co/) and/or a
// `.wb-retired-lock-*` file, with no real checkout anywhere underneath. A
// live cleanup no longer creates this residue (see removeEmptyParent and
// purgeTerminalTaskLockDebris), but it does not retroactively clean up state
// written before that fix existed.
//
// This is deliberately narrow: it never inspects, let alone removes, a
// worktree that still has a real Git checkout under it, an active or
// interrupted operation lock, a reserved .wb-stage-*/.wb-retired-stage-*
// entry (that is Cleanup's own explicit blocking backlog — see
// #req:internal-stage-terminalization), or anything else it cannot prove is
// empty, WB-owned, and terminal. Dry run is the default; --apply is required
// to remove anything, matching every other WB mutation.
type RetireShellsOptions struct {
	ProjectsRoot string
	Filter       string
	Apply        bool
}

// RetiredShell is one task directory's shell-retirement plan and, under
// --apply, its outcome.
type RetiredShell struct {
	WorktreesRoot string `json:"worktrees_root"`
	Task          string `json:"task"`
	Path          string `json:"path"`
	Eligible      bool   `json:"eligible"`
	Applied       bool   `json:"applied"`
	Reason        string `json:"reason"`
	Error         string `json:"error,omitempty"`
}

// RetireShellsOutcome is the full result of one plan or apply sweep.
type RetireShellsOutcome struct {
	Apply   bool           `json:"apply"`
	Results []RetiredShell `json:"results"`
	Totals  map[string]int `json:"totals"`
}

// RetireTaskShells sweeps every resolver-recognized worktrees root for task
// directories that are provably empty shells and, under --apply, retires
// them. It is read-only unless Apply is explicit.
func RetireTaskShells(ctx context.Context, options RetireShellsOptions) (RetireShellsOutcome, error) {
	_ = ctx // no network or subprocess I/O: this is a pure local filesystem sweep.
	resolution, err := wbhome.Resolve(options.ProjectsRoot)
	if err != nil {
		return RetireShellsOutcome{}, err
	}
	seenRoots := map[string]bool{}
	var results []RetiredShell
	for _, layout := range resolution.Read {
		root := filepath.Clean(layout.WorktreesRoot)
		if seenRoots[root] {
			continue // Write and a discovered legacy layout may resolve to the same root.
		}
		seenRoots[root] = true
		entries, readErr := os.ReadDir(root)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return RetireShellsOutcome{}, fmt.Errorf("read worktrees root %s: %w", root, readErr)
		}
		for _, entry := range entries {
			task := entry.Name()
			if options.Filter != "" && !strings.Contains(task, options.Filter) {
				continue
			}
			info, infoErr := entry.Info()
			if infoErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				continue // never treat a non-directory or symlinked entry as a task.
			}
			result := inspectTaskShell(root, task)
			if options.Apply && result.Eligible {
				applyTaskShellRetirement(&result)
			}
			results = append(results, result)
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].WorktreesRoot != results[j].WorktreesRoot {
			return results[i].WorktreesRoot < results[j].WorktreesRoot
		}
		return results[i].Task < results[j].Task
	})
	totals := map[string]int{}
	for _, result := range results {
		switch {
		case result.Applied:
			totals["retired"]++
		case result.Eligible:
			totals["would_retire"]++
		default:
			totals["skipped"]++
		}
	}
	return RetireShellsOutcome{Apply: options.Apply, Results: results, Totals: totals}, nil
}

// inspectTaskShell decides, from filesystem structure alone, whether a task
// directory is provably an empty shell: every direct entry is either a
// retired operation lock or an owner-namespace directory whose own contents
// (directly, or one level deeper at <owner>/<repository>) are entirely
// empty. Anything else — a live or interrupted `.lock`, a reserved stage
// entry, a real file, a symlink, or a repository directory that still holds
// anything (most importantly a real Git checkout) — refuses eligibility and
// names the reason, exactly like every other WB cleanup diagnostic.
func inspectTaskShell(worktreesRoot, task string) RetiredShell {
	taskPath := filepath.Join(worktreesRoot, task)
	eligible, reason := taskShellIsEmpty(taskPath, task, false)
	return RetiredShell{WorktreesRoot: worktreesRoot, Task: task, Path: taskPath, Eligible: eligible, Reason: reason}
}

// taskShellIsEmpty is the shared structural check behind two callers with
// one difference: inspectTaskShell is a read-only scan where any `.lock` at
// all disqualifies, but applyTaskShellRetirement's recheck runs after it has
// already reclaimed and holds that exact lock itself — lockHeldByThisCall
// tells the check to treat that one expected `.lock` as ignorable residue
// rather than mistaking its own lock for someone else's live operation.
func taskShellIsEmpty(taskPath, task string, lockHeldByThisCall bool) (bool, string) {
	taskInfo, err := os.Lstat(taskPath)
	if err != nil {
		return false, fmt.Sprintf("stat task directory: %v", err)
	}
	if taskInfo.Mode()&os.ModeSymlink != 0 || !taskInfo.IsDir() {
		return false, "task path is not an ordinary directory"
	}

	entries, err := os.ReadDir(taskPath)
	if err != nil {
		return false, fmt.Sprintf("read task directory: %v", err)
	}

	var ownerDirs []string
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case name == ".lock":
			if lockHeldByThisCall {
				continue // the lock this same transaction just reclaimed, not a live operation's.
			}
			return false, fmt.Sprintf("an active or interrupted operation lock is present; run `wb worktree cleanup %s --resume-interrupted` first", task)
		case strings.HasPrefix(name, ".wb-retired-lock-"):
			info, infoErr := entry.Info()
			if infoErr != nil || !info.Mode().IsRegular() {
				return false, fmt.Sprintf("unexpected retired-lock entry %s", name)
			}
			continue // expected residue from a prior terminal cleanup; retired alongside the shell.
		case isWorktreeStagingDirectory(name) || isRetiredWorktreeStagingDirectory(name):
			return false, fmt.Sprintf("reserved stage entry %s is explicit cleanup backlog, not a shell", name)
		default:
			info, infoErr := entry.Info()
			if infoErr != nil {
				return false, fmt.Sprintf("stat %s: %v", name, infoErr)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return false, fmt.Sprintf("unexpected non-directory entry %s", name)
			}
			ownerDirs = append(ownerDirs, name)
		}
	}

	for _, owner := range ownerDirs {
		empty, err := ownerDirectoryIsProvablyEmpty(filepath.Join(taskPath, owner))
		if err != nil {
			return false, fmt.Sprintf("inspect %s/%s: %v", task, owner, err)
		}
		if !empty {
			return false, fmt.Sprintf("%s/%s is not empty; a real checkout may still live under it", task, owner)
		}
	}

	return true, "empty WB task shell with no live checkout"
}

// ownerDirectoryIsProvablyEmpty reports whether an owner-namespace directory
// holds nothing but empty repository directories (or is itself empty),
// exactly the two shapes WB's own layout resolver recognizes
// (<task>/<owner> and <task>/<owner>/<repository>; see
// locateManagedWorktree). Anything else — a file, a symlink, or a
// repository directory that still holds something, most importantly a real
// `.git` — is conservatively "not empty" so it is left untouched.
func ownerDirectoryIsProvablyEmpty(ownerPath string) (bool, error) {
	info, err := os.Lstat(ownerPath)
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, nil
	}
	entries, err := os.ReadDir(ownerPath)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		repositoryPath := filepath.Join(ownerPath, entry.Name())
		repositoryInfo, err := os.Lstat(repositoryPath)
		if err != nil {
			return false, err
		}
		if repositoryInfo.Mode()&os.ModeSymlink != 0 || !repositoryInfo.IsDir() {
			return false, nil
		}
		children, err := os.ReadDir(repositoryPath)
		if err != nil {
			return false, err
		}
		if len(children) != 0 {
			return false, nil
		}
	}
	return true, nil
}

// applyTaskShellRetirement removes an already-eligible shell. It acquires the
// exact same per-task operation lock a live `wb worktree cleanup` would, so a
// task a concurrent WB operation is actively touching is refused rather than
// raced: acquireCleanupTaskAt fails closed on any live or interrupted `.lock`
// and only ever reclaims an inert `.wb-retired-lock-*`, which is proof by
// construction that no operation holds it. Everything is rechecked fresh
// under that lock before anything is removed.
func applyTaskShellRetirement(result *RetiredShell) {
	task, err := acquireCleanupTaskAt(result.WorktreesRoot, result.Task)
	if err != nil {
		result.Error = err.Error()
		return
	}
	// mutationStarted distinguishes "nothing was touched, release the lock
	// exactly as an ordinary no-op inspection would" from "a directory was
	// already removed, preserve the lock as an interrupted-recovery record
	// instead of guessing at a rollback." Only the latter ever calls
	// task.preserveLock(): a defensive recheck failing before anything moved
	// must not itself convert a harmless empty shell into a needs-recovery
	// task on every repeated dry sweep.
	mutationStarted := false
	settled := false
	defer func() {
		if settled {
			task.close()
			return
		}
		if mutationStarted {
			task.preserveLock()
		} else if releaseErr := task.lock.release(); releaseErr == nil {
			purgeTerminalTaskLockDebris(task)
		}
		task.close()
	}()

	// Recheck under the lock: nothing may have changed between the read-only
	// scan above and acquiring exclusive access, but this is the same
	// recheck-before-mutate discipline every other WB deletion uses. The
	// `.lock` acquireCleanupTaskAt just reclaimed is this same transaction's
	// own, so the recheck must not mistake it for a live operation's.
	eligible, reason := taskShellIsEmpty(result.Path, result.Task, true)
	if !eligible {
		result.Reason = reason
		return
	}

	entries, err := os.ReadDir(result.Path)
	if err != nil {
		result.Error = fmt.Sprintf("re-read task directory: %v", err)
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == ".lock" || strings.HasPrefix(name, ".wb-retired-lock-") {
			continue // the lock this transaction holds, purged (as a fresh retirement) below.
		}
		mutationStarted = true
		ownerPath := filepath.Join(result.Path, name)
		repositoryEntries, err := os.ReadDir(ownerPath)
		if err != nil {
			result.Error = fmt.Sprintf("re-read owner directory %s: %v", ownerPath, err)
			return
		}
		for _, repository := range repositoryEntries {
			if rmErr := os.Remove(filepath.Join(ownerPath, repository.Name())); rmErr != nil {
				result.Error = fmt.Sprintf("remove empty repository directory: %v", rmErr)
				return
			}
		}
		if rmErr := os.Remove(ownerPath); rmErr != nil {
			result.Error = fmt.Sprintf("remove empty owner directory: %v", rmErr)
			return
		}
	}

	if err := task.lock.release(); err != nil {
		result.Error = fmt.Sprintf("quarantine reclaimed operation lock: %v", err)
		return
	}
	purgeTerminalTaskLockDebris(task)
	settled = true // the deferred cleanup above still closes the descriptors.
	result.Applied = true
	result.Reason = "retired empty WB task shell"
}
