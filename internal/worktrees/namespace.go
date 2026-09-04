package worktrees

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
)

// A task namespace is the `<worktrees-root>/<task>` directory every repository
// of one effort lives under. It is the last level of residue a terminal
// cleanup leaves: removeEmptyParent retires `<task>/<owner>` once its final
// repository is gone and purgeTerminalTaskLockDebris clears the retired locks,
// after which nothing removed the directory itself. One empty shell per
// finished task then accumulates forever, invisible to `wb worktree list` —
// a task directory with no repositories under it produces no candidate and no
// diagnostic, so the count only shows up to someone running `ls`.
//
// Retiring it is the same operation removeEmptyParent already performs one
// level down, with the same guarantee: AT_REMOVEDIR is atomic against any
// other writer, refusing with ENOTEMPTY rather than destroying content. Empty
// therefore means empty — a stray file somebody left in a task directory keeps
// it, and so does a repository still present.

const (
	lifecycleArtifactKindStage         = "secure_worktree_stage"
	lifecycleArtifactKindTaskNamespace = "task_namespace"
	taskNamespaceEmptyState            = "empty"
	dispositionRetireEmptyNamespace    = "retire_empty_namespace"
)

// removeEmptyTaskDirectory retires the held task directory when nothing is
// left in it. Best-effort by the same reasoning as purgeTerminalTaskLockDebris,
// which it follows: a cleanup whose branch and worktree removal already applied
// is not failed by housekeeping, and any unexpected outcome — a concurrent
// writer, a replacement swapped in — leaves the directory untouched.
func removeEmptyTaskDirectory(task *cleanupTaskHandle) bool {
	if task == nil || task.worktrees == nil || task.task == nil {
		return false
	}
	name := filepath.Base(task.taskPath)
	if !validSafeSegment(name) {
		return false
	}
	// The retirement is by name under the retained worktrees-root descriptor,
	// so prove the name still resolves to the very directory this transaction
	// held before unlinking it.
	if !directoryStillMatches(task.taskPath, task.task) {
		return false
	}
	return unix.Unlinkat(int(task.worktrees.Fd()), name, unix.AT_REMOVEDIR) == nil
}

// emptyTaskNamespaces reports the namespaces earlier releases left behind:
// directories no cleanup will ever run for again, because the repositories
// that would have selected them are long gone. Discovery is read-only and
// happens before any apply, so a namespace a concurrent `wb worktree create`
// makes after this scan can never appear in the list an apply acts on.
func emptyTaskNamespaces(layouts []wbhome.Layout, tasks map[string]bool, filter string, homes ...string) ([]LifecycleArtifact, error) {
	logicalRoot := ""
	if len(homes) > 0 && homes[0] != "" {
		logicalRoot = filepath.Join(filepath.Clean(homes[0]), "worktrees")
	}
	artifacts := make([]LifecycleArtifact, 0)
	seen := make(map[string]bool, len(layouts))
	for _, layout := range layouts {
		root := filepath.Clean(layout.WorktreesRoot)
		// New local and relocated shared placements use this directory only for
		// logical locks/claims. Its empty task shell is not proof that every
		// physical member is terminal, so only a transaction that inspected the
		// physical members may retire it.
		if logicalRoot != "" && root == logicalRoot {
			continue
		}
		if seen[root] {
			continue
		}
		seen[root] = true
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read worktree tasks under %s: %w", root, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			if !taskSelectionMatches(tasks, entry.Name()) {
				continue
			}
			// A malformed task directory name is already reported as its own
			// diagnostic and is never something WB acts on by name.
			if !validSafeSegment(entry.Name()) {
				continue
			}
			path := filepath.Join(root, entry.Name())
			contents, err := os.ReadDir(path)
			if err != nil || len(contents) != 0 {
				continue
			}
			artifact := LifecycleArtifact{
				Task: entry.Name(), WorktreesRoot: root, Path: path,
				Kind: lifecycleArtifactKindTaskNamespace, State: taskNamespaceEmptyState,
				Disposition: dispositionRetireEmptyNamespace, Eligible: true,
				Reason: "task namespace holds nothing; every repository under it is already terminal",
			}
			if filter != "" {
				// --filter selects by owner/repository slug. An empty namespace
				// has no repository to match, so a filtered run must report it
				// rather than act outside the selection it was given.
				artifact.Eligible = false
				artifact.Reason = "empty task namespace has no repository identity for --filter; rerun without --filter to retire it"
			}
			artifacts = append(artifacts, artifact)
		}
	}
	return artifacts, nil
}

// retireEmptyTaskNamespaces applies what emptyTaskNamespaces planned, under the
// same per-task lock every other WB operation on that task takes. That lock is
// what serializes this against a `wb worktree create` for the same task name:
// the creator holds it while it works, so the acquisition here fails and the
// namespace is left alone.
func retireEmptyTaskNamespaces(artifacts []LifecycleArtifact) {
	for index := range artifacts {
		artifact := &artifacts[index]
		if artifact.Kind != lifecycleArtifactKindTaskNamespace || !artifact.Eligible || artifact.Applied {
			continue
		}
		// A namespace the terminal cleanup pass already retired needs no second
		// removal, only an honest report of what happened to it.
		if _, err := os.Lstat(artifact.Path); errors.Is(err, os.ErrNotExist) {
			artifact.Applied = true
			continue
		}
		task, err := acquireCleanupTaskAt(artifact.WorktreesRoot, artifact.Task)
		if err != nil {
			artifact.Reason = "task namespace is in use: " + err.Error()
			continue
		}
		if releaseErr := task.lock.release(); releaseErr != nil {
			task.close()
			artifact.Reason = "release task namespace lock: " + releaseErr.Error()
			continue
		}
		purgeTerminalTaskLockDebris(task)
		artifact.Applied = removeEmptyTaskDirectory(task)
		if !artifact.Applied {
			artifact.Reason = "task namespace was no longer empty at retirement"
		}
		task.close()
	}
}
