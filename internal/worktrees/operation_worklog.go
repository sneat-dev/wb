package worktrees

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// OperationWorkLogBase authenticates an existing operation claim. Only a genuinely
// missing projection returns present=false; corrupt and terminal records refuse.
func OperationWorkLogBase(home, task string, result CreateResult, options WorkLogOptions) (string, bool, error) {
	claim, _, _, err := activeWorkLogClaim(home, result.WorktreeDir)
	if errors.Is(err, errWorkLogProjectionNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	result.BaseSHA = claim.BaseSHA
	if err := validateOperationWorkLogClaim(claim, task, result, options); err != nil {
		return "", false, err
	}
	return claim.BaseSHA, true, nil
}

func validateOperationWorkLogClaim(claim workLogClaim, task string, result CreateResult, options WorkLogOptions) error {
	effort, run, err := normalizeWorkLogOptions(task, options, time.Now().UTC())
	if err != nil {
		return err
	}
	want := workLogClaimID(effort, result)
	if claim.EffortID != effort || claim.RunID != run || claim.Task != task || claim.Repository != result.Repository ||
		filepath.Clean(claim.Worktree) != filepath.Clean(result.WorktreeDir) || claim.Branch != result.Branch ||
		claim.Base != result.Base || claim.BaseSHA != result.BaseSHA || claim.ClaimID != want {
		return fmt.Errorf("existing active Work Log claim does not match the operation checkout identity")
	}
	// NormalizeOptions already validated this pure summary; only its
	// whitespace normalization is needed for the comparison.
	requestedSummary := strings.TrimSpace(options.TaskSummary)
	if requestedSummary != "" && claim.TaskSummary != requestedSummary {
		return errors.New("existing active Work Log claim has a different immutable task summary")
	}
	return nil
}

// RecoverLegacyWorktreeBase preserves the same native merge-base recovery used
// by Create for a retained branch without an authenticated claim.
func RecoverLegacyWorktreeBase(ctx context.Context, canonical, repository, branch, baseRevision string) (string, error) {
	return recoverLegacyWorktreeBase(repository, branch, baseRevision, func(args ...string) (string, error) {
		return git(ctx, canonical, args...)
	})
}

func recoverLegacyWorktreeBase(repository, branch, baseRevision string, query func(...string) (string, error)) (string, error) {
	mergeBase, mergeErr := query("merge-base", "refs/heads/"+branch, baseRevision)
	if mergeErr != nil || !isGitObjectID(mergeBase) {
		return "", fmt.Errorf("recover legacy worktree base for %s: %w", repository, mergeErr)
	}
	return mergeBase, nil
}
