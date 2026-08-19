package worktrees

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CheckpointRef describes one discoverable checkpoint, local or remote.
type CheckpointRef struct {
	Ref     string    `json:"ref"`
	Scope   string    `json:"scope"`
	Commit  string    `json:"commit"`
	Message string    `json:"message,omitempty"`
	Head    string    `json:"head,omitempty"`
	Dirty   bool      `json:"dirty"`
	At      time.Time `json:"at,omitempty"`
	Remote  bool      `json:"remote,omitempty"`
}

// CheckpointListOptions configures wb worktree checkpoint list.
type CheckpointListOptions struct {
	Worktree string
	// All lists every scope in the repository instead of only the current one.
	All bool
	// IncludeRemote additionally queries the remote so a checkpoint can be
	// found after the local worktree that made it is gone.
	IncludeRemote bool
	Remote        string
}

// CheckpointList enumerates checkpoint refs, newest first.
func CheckpointList(ctx context.Context, options CheckpointListOptions) ([]CheckpointRef, error) {
	root, err := resolveWorktreeRoot(ctx, options.Worktree)
	if err != nil {
		return nil, err
	}
	pattern := checkpointRefPrefix
	if !options.All {
		scope, scopeErr := checkpointScope(ctx, root)
		if scopeErr != nil {
			return nil, scopeErr
		}
		pattern = checkpointRefPrefix + scope + "/"
	}
	out, err := git(ctx, root, "for-each-ref", "--sort=-refname", "--format=%(refname) %(objectname)", pattern)
	if err != nil {
		return nil, fmt.Errorf("list checkpoints: %w", err)
	}
	seen := map[string]bool{}
	results := make([]CheckpointRef, 0)
	for _, line := range nonEmptyLines(out) {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		entry, describeErr := describeCheckpointCommit(ctx, root, fields[0], fields[1])
		if describeErr != nil {
			continue
		}
		seen[entry.Ref] = true
		results = append(results, entry)
	}
	if options.IncludeRemote {
		remoteName := strings.TrimSpace(options.Remote)
		if remoteName == "" {
			remoteName = defaultCheckpointRemote
		}
		remoteOut, remoteErr := git(ctx, root, "ls-remote", "--refs", remoteName, pattern+"*")
		if remoteErr == nil {
			for _, line := range nonEmptyLines(remoteOut) {
				fields := strings.Fields(line)
				if len(fields) != 2 || seen[fields[1]] {
					continue
				}
				results = append(results, CheckpointRef{
					Ref: fields[1], Commit: fields[0], Remote: true,
					Scope: checkpointScopeFromRef(fields[1]),
				})
			}
		}
	}
	return results, nil
}

func checkpointScopeFromRef(ref string) string {
	trimmed := strings.TrimPrefix(ref, checkpointRefPrefix)
	idx := strings.LastIndex(trimmed, "/")
	if idx < 0 {
		return trimmed
	}
	return trimmed[:idx]
}

func describeCheckpointCommit(ctx context.Context, root, ref, commit string) (CheckpointRef, error) {
	body, err := git(ctx, root, "show", "-s", "--format=%B", commit)
	if err != nil {
		return CheckpointRef{}, err
	}
	entry := CheckpointRef{Ref: ref, Commit: commit, Scope: checkpointScopeFromRef(ref)}
	lines := strings.Split(body, "\n")
	if len(lines) > 0 {
		entry.Message = strings.TrimPrefix(strings.TrimSpace(lines[0]), checkpointSubjectPrefix)
	}
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "WB-Checkpoint-Head":
			if value != "none" {
				entry.Head = value
			}
		case "WB-Checkpoint-Dirty":
			entry.Dirty = value == "true"
		case "WB-Checkpoint-At":
			if parsed, parseErr := time.Parse(time.RFC3339, value); parseErr == nil {
				entry.At = parsed
			}
		}
	}
	return entry, nil
}

// CheckpointRestoreOptions configures wb worktree checkpoint restore.
type CheckpointRestoreOptions struct {
	Worktree string
	// Ref selects a checkpoint: a full refs/wb/checkpoints/... name, a bare
	// timestamp leaf resolved against the current scope, a raw commit SHA, or
	// "latest" (the default) for the current scope's newest checkpoint.
	Ref    string
	Branch string
	Apply  bool
	Force  bool
}

// CheckpointRestoreResult is the receipt for a restore attempt.
type CheckpointRestoreResult struct {
	Worktree string `json:"worktree"`
	Ref      string `json:"ref"`
	Commit   string `json:"commit"`
	Branch   string `json:"branch,omitempty"`
	Applied  bool   `json:"applied"`
	DiffStat string `json:"diffstat,omitempty"`
}

// CheckpointRestore never touches the caller's current branch, working tree,
// or index. Applying creates exactly one new local branch at the checkpoint
// commit; recovery is always an explicit, separate, inspectable branch.
func CheckpointRestore(ctx context.Context, options CheckpointRestoreOptions) (CheckpointRestoreResult, error) {
	root, err := resolveWorktreeRoot(ctx, options.Worktree)
	if err != nil {
		return CheckpointRestoreResult{}, err
	}
	ref, commit, err := resolveCheckpointRef(ctx, root, options.Ref)
	if err != nil {
		return CheckpointRestoreResult{}, err
	}
	diffStat, _ := git(ctx, root, "diff", "--stat", "HEAD", commit)
	result := CheckpointRestoreResult{Worktree: root, Ref: ref, Commit: commit, DiffStat: diffStat}
	if !options.Apply {
		return result, nil
	}
	branch := strings.TrimSpace(options.Branch)
	if branch == "" {
		return result, fmt.Errorf("--branch is required with --apply")
	}
	if _, err := git(ctx, root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil && !options.Force {
		return result, fmt.Errorf("branch %q already exists; pass --force to overwrite it", branch)
	}
	args := []string{"branch"}
	if options.Force {
		args = append(args, "-f")
	}
	args = append(args, branch, commit)
	if _, err := git(ctx, root, args...); err != nil {
		return result, fmt.Errorf("create recovery branch %q: %w", branch, err)
	}
	result.Branch = branch
	result.Applied = true
	return result, nil
}

func resolveCheckpointRef(ctx context.Context, root, requested string) (ref, commit string, err error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == "latest" {
		scope, scopeErr := checkpointScope(ctx, root)
		if scopeErr != nil {
			return "", "", scopeErr
		}
		ref, commit, _, _, findErr := latestCheckpointRef(ctx, root, scope)
		if findErr != nil {
			return "", "", findErr
		}
		if ref == "" {
			return "", "", fmt.Errorf("no checkpoint exists yet for %q", scope)
		}
		return ref, commit, nil
	}
	if strings.HasPrefix(requested, checkpointRefPrefix) {
		sha, verifyErr := git(ctx, root, "rev-parse", "--verify", "--quiet", requested)
		if verifyErr != nil {
			return "", "", fmt.Errorf("checkpoint ref %q not found", requested)
		}
		return requested, strings.TrimSpace(sha), nil
	}
	// A bare timestamp leaf, resolved against the current scope.
	if !strings.Contains(requested, "/") {
		scope, scopeErr := checkpointScope(ctx, root)
		if scopeErr == nil {
			candidate := checkpointRefPrefix + scope + "/" + requested
			if sha, verifyErr := git(ctx, root, "rev-parse", "--verify", "--quiet", candidate); verifyErr == nil {
				return candidate, strings.TrimSpace(sha), nil
			}
		}
	}
	// Fall back to treating it as a raw commit-ish.
	sha, verifyErr := git(ctx, root, "rev-parse", "--verify", "--quiet", requested)
	if verifyErr != nil {
		return "", "", fmt.Errorf("checkpoint %q not found", requested)
	}
	return requested, strings.TrimSpace(sha), nil
}
