package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
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
		if _, moveBackErr := moveRenameDirectory(destination, source, nil); moveBackErr != nil {
			return errors.Join(cause, fmt.Errorf("restore clone to %s after failed move: %w", source, moveBackErr))
		}
		if _, repairErr := git(ctx, source, append([]string{"worktree", "repair"}, sourcePaths...)...); repairErr != nil {
			return errors.Join(cause, fmt.Errorf("repair worktree registration at %s after rollback: %w", source, repairErr))
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

// VerifyClonePlacement checks that every named worktreePath is registered
// against clonePath, exists, has its common Git directory resolving to the
// clone's `.git`, and that `git status` succeeds in it — the same
// verification `ApplyCloneMove` performs, exposed so a caller (undo, tests)
// can re-check a placement independently.
//
// This deliberately checks only worktreePaths, not every entry the clone's
// registry happens to hold: a caller re-verifying just the worktree(s) it
// itself moved or repaired (ReconcileClonePlacement's apply path) must not
// fail because some OTHER, unrelated worktree of the same clone is prunable
// or missing for a reason this migration did not cause and cannot fix.
func VerifyClonePlacement(ctx context.Context, clonePath string, worktreePaths []string) error {
	out, err := gitRawOutput(ctx, clonePath, "worktree", "list", "--porcelain")
	if err != nil {
		return fmt.Errorf("list worktree registration for %s: %w", clonePath, err)
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

// ReconcileClonePlacement inspects a clone already at its expected location
// clonePath for administrative breakage a clone migration itself can leave
// behind, without running `git status` — or any other check — on a worktree
// migration cannot have broken. legacyClonePath is the clone's pre-migration
// <root>/<owner>/<repository> path, always derivable from its current
// host-level location whether or not it was ever actually migrated through
// here; it is used only to recognise a linked worktree that physically moved
// along with an earlier directory rename (a manual `mv`, or an interrupted
// migration) whose Git registration was never repaired to match.
//
// For each worktree Git has registered against clonePath: if the recorded
// path is missing on disk but rebasing it from legacyClonePath onto clonePath
// names a directory that DOES exist, that worktree moved with the clone and
// its `.git` pointer file is read directly (no subprocess) and compared
// against clonePath's own `.git`; a mismatch means the migration left it
// stranded. A worktree whose recorded path is missing and does not rebase to
// something that exists, or whose `.git` pointer cannot be read at all, is
// breakage this migration cannot explain: it is reported back as
// informational, never repaired automatically and never treated as a
// failure or reflected in a caller's exit code.
//
// Without apply, ReconcileClonePlacement only detects: it returns
// "needs_repair" when migration-caused breakage was found, so a dry run never
// changes anything on disk — the repair itself, and the lock that must be
// held while performing it, are entirely the caller's responsibility, taken
// only when apply is true. With apply, that breakage is repaired with `git
// worktree repair`, using the CURRENT (rebased, existing) path rather than
// the stale recorded one — passing the stale, nonexistent path is exactly
// what makes `git worktree repair` fail with "invalid path" against a
// worktree nested inside the clone — and the repair is verified, including
// `git status`, only for the worktree(s) just repaired.
func ReconcileClonePlacement(ctx context.Context, clonePath, legacyClonePath string, apply bool) (status string, informational []string, err error) {
	recorded, err := registeredWorktreePaths(ctx, clonePath)
	if err != nil {
		return "", nil, fmt.Errorf("list worktree registration for %s: %w", clonePath, err)
	}
	commonDir := filepath.Clean(filepath.Join(clonePath, ".git"))
	var toRepair []string
	for _, path := range recorded {
		current := path
		if _, statErr := os.Stat(path); statErr != nil {
			rebased, ok := rebaseUnderNewClone(path, legacyClonePath, clonePath)
			if !ok {
				informational = append(informational, path)
				continue
			}
			current = rebased
		}
		common, readErr := readWorktreeCommonDir(current)
		if readErr != nil {
			informational = append(informational, path)
			continue
		}
		if filepath.Clean(common) == commonDir {
			continue
		}
		toRepair = append(toRepair, current)
	}
	if len(toRepair) == 0 {
		return "verified", informational, nil
	}
	if !apply {
		return "needs_repair", informational, nil
	}
	if _, err := git(ctx, clonePath, append([]string{"worktree", "repair"}, toRepair...)...); err != nil {
		return "", informational, fmt.Errorf("repair worktree registration for %s: %w", clonePath, err)
	}
	if err := VerifyClonePlacement(ctx, clonePath, toRepair); err != nil {
		return "", informational, fmt.Errorf("clone at %s is stranded: %w", clonePath, err)
	}
	return "repaired", informational, nil
}

// rebaseUnderNewClone reports whether path — which does not currently exist —
// is nested under legacyClonePath (the clone's pre-migration location) and
// whether rebasing it under clonePath (the clone's current location) names a
// directory that does exist, meaning it physically moved there along with an
// earlier, unrepaired clone rename.
func rebaseUnderNewClone(path, legacyClonePath, clonePath string) (string, bool) {
	if legacyClonePath == "" {
		return "", false
	}
	legacyClonePath = filepath.Clean(legacyClonePath)
	path = filepath.Clean(path)
	if path != legacyClonePath && !strings.HasPrefix(path, legacyClonePath+string(filepath.Separator)) {
		return "", false
	}
	relative, err := filepath.Rel(legacyClonePath, path)
	if err != nil {
		return "", false
	}
	rebased := filepath.Join(clonePath, relative)
	if _, statErr := os.Stat(rebased); statErr != nil {
		return "", false
	}
	return rebased, true
}

// readWorktreeCommonDir reads a linked worktree's own `.git` file — a plain
// text pointer, not a directory — and returns the common Git directory it
// currently names, without spawning Git: exactly the check needed to tell a
// worktree whose pointer still names a clone's pre-move location from a
// healthy one, at the cost of a single small file read rather than a process,
// which is what makes checking every worktree of every host-level clone on
// every run affordable.
func readWorktreeCommonDir(worktreeDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(worktreeDir, ".git"))
	if err != nil {
		return "", err
	}
	content := strings.TrimSpace(string(data))
	target := strings.TrimSpace(strings.TrimPrefix(content, "gitdir:"))
	if target == "" {
		return "", fmt.Errorf("worktree %s has an empty .git pointer", worktreeDir)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(worktreeDir, target)
	}
	// target names <clone>/.git/worktrees/<id>; its common directory is its
	// own grandparent.
	return filepath.Dir(filepath.Dir(filepath.Clean(target))), nil
}

// registeredWorktreePaths lists every worktree Git has registered against
// clonePath other than the clone's own entry.
func registeredWorktreePaths(ctx context.Context, clonePath string) ([]string, error) {
	out, err := gitRawOutput(ctx, clonePath, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	self := filepath.Clean(clonePath)
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		path, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		if clean := filepath.Clean(path); clean != self {
			paths = append(paths, clean)
		}
	}
	return paths, nil
}

// cloneMoveRelocationEntry pairs one worktree of a clone move with the
// durable relocation intent recorded for it, so the receipt appended after
// the move can be bound to the exact home and claim the intent was recorded
// against.
type cloneMoveRelocationEntry struct {
	home        string
	claim       workLogClaim
	intent      *workLogRelocationIntent
	destination string
}

// RecordCloneMoveRelocationIntents finds the active Work Log claim for every
// worktree named in moves whose Source differs from its Destination —
// searching every home wbhome.Resolve reports for projectsRoot, since a claim
// recorded before the projects-root layout existed still lives under a
// retired legacy home — and records a durable relocation intent for each one
// it finds, before the clone physically moves. A worktree whose Source equals
// its Destination (registered outside the clone; only its Git administration
// is repaired) needs no intent: its claim's frozen path already matches.
//
// This is the same relocation-receipt journal RelocateRepository and `wb
// worktree relocate` use: a claim's Worktree field is an immutable absolute
// path frozen at claim-creation time, and repo-local worktree discovery
// (lifecycle.go) resolves a claim's current location by following this
// journal rather than comparing against that frozen path directly. Recording
// an intent here, and a receipt once the move is verified, is what lets that
// resolution — and so `wb worktree land`'s claim-authority check — keep
// working after a clone migration moves the checkout.
//
// The repository identity does not change during a clone-placement
// migration, so this uses the plain (non-repository) relocation record shape
// — the same one `wb worktree relocate` uses for a placement-mode change —
// rather than the "repository" record type RelocateRepository uses, which
// requires the destination repository to actually differ from the source.
func RecordCloneMoveRelocationIntents(projectsRoot string, moves []CloneMoveWorktree, now time.Time) ([]cloneMoveRelocationEntry, error) {
	resolution, err := wbhome.Resolve(projectsRoot)
	if err != nil {
		return nil, err
	}
	var entries []cloneMoveRelocationEntry
	for _, move := range moves {
		if filepath.Clean(move.Source) == filepath.Clean(move.Destination) {
			continue
		}
		for _, layout := range resolution.Read {
			home := filepath.Clean(layout.Home)
			if home == "" {
				continue
			}
			claim, _, _, claimErr := activeWorkLogClaim(home, move.Source)
			if claimErr != nil {
				continue
			}
			intent, _, intentErr := appendRelocationIntent(home, claim, move.Source, move.Destination, "local", move.Head, relocationPlacementRecord{}, now)
			if intentErr != nil {
				return entries, fmt.Errorf("record clone-move relocation intent for %s: %w", move.Source, intentErr)
			}
			entries = append(entries, cloneMoveRelocationEntry{home: home, claim: claim, intent: intent, destination: move.Destination})
			break
		}
	}
	return entries, nil
}

// FinalizeCloneMoveRelocationReceipts appends the completion receipt for
// every intent RecordCloneMoveRelocationIntents recorded, once the clone move
// is verified. Called after ApplyCloneMove succeeds; entries recorded for a
// move that was rolled back are deliberately left as pending intents (the
// same recovery shape `wb worktree relocate` leaves an interrupted move in),
// since nothing ever resolves a claim's location from an intent alone — only
// a receipt does.
func FinalizeCloneMoveRelocationReceipts(entries []cloneMoveRelocationEntry, now time.Time) error {
	for _, entry := range entries {
		if _, _, err := appendRelocationReceipt(entry.home, entry.claim, entry.intent, now); err != nil {
			return fmt.Errorf("record clone-move relocation receipt for %s: %w", entry.destination, err)
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
