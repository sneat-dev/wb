package worktrees

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CloneMoveWorktree is one linked worktree of a canonical clone being moved,
// named by its path before and after the clone's move. A worktree nested under
// the clone (for example <clone>/.worktrees/<task>) moves with the clone
// itself, so Source and Destination differ only by the rebased prefix. A
// worktree registered anywhere else keeps its path: Source and Destination are
// identical, and only its Git administration is repaired.
type CloneMoveWorktree struct {
	Source      string
	Destination string
	Head        string
}

// CloneMoveResult reports the outcome of moving one canonical clone and every
// worktree linked to it.
type CloneMoveResult struct {
	Source      string
	Destination string
	Worktrees   []CloneMoveWorktree
}

// PlanCloneMove computes the destination of a canonical clone at source and of
// every worktree Git has registered against it, without changing anything on
// disk. It is the single source of truth `ApplyCloneMove` also uses, so a
// dry-run plan and the move it describes can never disagree.
func PlanCloneMove(ctx context.Context, source, destination string) (CloneMoveResult, error) {
	entries, err := repositoryRelocateWorktrees(ctx, source, destination)
	if err != nil {
		return CloneMoveResult{}, err
	}
	result := CloneMoveResult{Source: source, Destination: destination}
	for _, entry := range entries {
		result.Worktrees = append(result.Worktrees, CloneMoveWorktree{
			Source: entry.source, Destination: entry.destination, Head: entry.head,
		})
	}
	return result, nil
}

// ApplyCloneMove moves the canonical clone at source to destination — a
// same-filesystem, no-replace rename that refuses an existing destination and
// a cross-device destination rather than degrading to a copy — then repoints
// every linked worktree with Git's own repair and verifies the result: the
// worktree registry has no missing or prunable entry, and every worktree's
// common Git directory resolves to the clone's new `.git`.
//
// A worktree nested under source moves with the directory rename itself and
// needs no separate move; a worktree registered anywhere else keeps its path
// and only has its Git administration repaired. Dirty working trees are never
// a refusal: the rename preserves uncommitted changes exactly as they were.
//
// On any failure after the clone has moved, ApplyCloneMove rolls the rename
// back and repairs the original registration before returning the error, so a
// failed move never leaves the clone stranded at its destination without a
// working worktree registry.
func ApplyCloneMove(ctx context.Context, source, destination string) (CloneMoveResult, error) {
	plan, err := PlanCloneMove(ctx, source, destination)
	if err != nil {
		return CloneMoveResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return plan, fmt.Errorf("prepare destination parent directory: %w", err)
	}
	moved, err := moveRenameDirectory(source, destination, nil)
	if err != nil {
		return plan, fmt.Errorf("move canonical clone: %w", err)
	}
	_ = moved.Close()

	destinationPaths := make([]string, 0, len(plan.Worktrees))
	sourcePaths := make([]string, 0, len(plan.Worktrees))
	for _, worktree := range plan.Worktrees {
		destinationPaths = append(destinationPaths, worktree.Destination)
		sourcePaths = append(sourcePaths, worktree.Source)
	}
	rollback := func(cause error) error {
		if _, moveBackErr := moveRenameDirectory(destination, source, nil); moveBackErr == nil {
			_, _ = git(ctx, source, append([]string{"worktree", "repair"}, sourcePaths...)...)
		}
		return cause
	}
	if _, err := git(ctx, destination, append([]string{"worktree", "repair"}, destinationPaths...)...); err != nil {
		return plan, rollback(fmt.Errorf("repair worktree registration: %w", err))
	}
	if err := VerifyClonePlacement(ctx, destination, destinationPaths); err != nil {
		return plan, rollback(err)
	}
	return plan, nil
}

// VerifyClonePlacement checks that the clone's worktree registry has no
// missing or prunable entry, that every named worktree path exists and its
// common Git directory resolves to the clone's `.git`, and that `git status`
// succeeds in each — the same verification `ApplyCloneMove` performs, exposed
// so a caller (undo, tests) can re-check a placement independently.
func VerifyClonePlacement(ctx context.Context, clonePath string, worktreePaths []string) error {
	out, err := gitRawOutput(ctx, clonePath, "worktree", "list", "--porcelain")
	if err != nil {
		return fmt.Errorf("list worktree registration for %s: %w", clonePath, err)
	}
	if strings.Contains(out, "\nprunable") || strings.HasPrefix(out, "prunable") {
		return fmt.Errorf("worktree registration for %s reports a prunable entry", clonePath)
	}
	listed := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			listed[filepath.Clean(path)] = true
		}
	}
	commonDir := filepath.Clean(filepath.Join(clonePath, ".git"))
	for _, path := range worktreePaths {
		if !listed[filepath.Clean(path)] {
			return fmt.Errorf("worktree registration for %s does not list %s", clonePath, path)
		}
		if _, statErr := os.Stat(path); statErr != nil {
			return fmt.Errorf("worktree path missing after move: %s: %w", path, statErr)
		}
		common, err := gitRawOutput(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return fmt.Errorf("resolve git common directory for %s: %w", path, err)
		}
		if filepath.Clean(strings.TrimSpace(common)) != commonDir {
			return fmt.Errorf("worktree %s common directory does not match %s", path, commonDir)
		}
		if _, err := gitRawOutput(ctx, path, "status"); err != nil {
			return fmt.Errorf("git status failed in %s: %w", path, err)
		}
	}
	return nil
}

// GitOperationInProgress reports, in one short label, whether a Git operation
// is in progress at path — a merge, cherry-pick, revert, rebase, or held index
// lock — or "" when none is. It resolves path's own private administrative
// directory first, so it reads the correct state for a linked worktree (its
// own admin directory under the clone's `.git/worktrees/<name>`) and not the
// clone's.
func GitOperationInProgress(ctx context.Context, path string) (string, error) {
	gitDir, err := git(ctx, path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", fmt.Errorf("resolve git directory for %s: %w", path, err)
	}
	markers := []struct{ name, label string }{
		{"MERGE_HEAD", "a merge is in progress (MERGE_HEAD present)"},
		{"CHERRY_PICK_HEAD", "a cherry-pick is in progress (CHERRY_PICK_HEAD present)"},
		{"REVERT_HEAD", "a revert is in progress (REVERT_HEAD present)"},
		{"rebase-merge", "a rebase is in progress (rebase-merge present)"},
		{"rebase-apply", "a rebase is in progress (rebase-apply present)"},
		{"index.lock", "an index lock is held (index.lock present)"},
	}
	for _, marker := range markers {
		if _, statErr := os.Stat(filepath.Join(gitDir, marker.name)); statErr == nil {
			return marker.label, nil
		}
	}
	return "", nil
}
