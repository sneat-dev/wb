package orchestrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
)

// performPullRequestCreateCommit commits the worktree's own change before
// the usual dirty-worktree refusal ever sees it, so a single command can
// carry an agent from an edited working tree to an open pull request. It
// never bypasses hooks: the commit it issues is a plain `git commit`, so a
// failing hook stops it before anything is pushed.
func performPullRequestCreateCommit(ctx context.Context, worktree string, options PullRequestCreateOptions) (refusal *createRefusal, committed []string, err error) {
	if strings.TrimSpace(options.Message) == "" {
		return nil, nil, fmt.Errorf("--commit-staged/--commit-all/--add require -m/--message")
	}
	if len(options.Add) > 0 {
		paths, pathErr := resolveAddPaths(ctx, options.resolveRunner(), worktree, options.Add)
		if pathErr != nil {
			return &createRefusal{
				code:    CreateRefusalInvalidPath,
				reason:  pathErr.Error(),
				command: "pass an --add path inside the worktree that names an existing path, or a tracked path's deletion",
			}, nil, nil
		}
		// .worktree.md is git-ignored; naming it explicitly would make the
		// `git add` below fail outright, so this is refused before staging,
		// on the requested paths themselves rather than the staged diff.
		var named []string
		for _, path := range paths {
			if strings.EqualFold(filepath.Base(path), ".worktree.md") {
				named = append(named, path)
			}
		}
		if len(named) > 0 {
			return &createRefusal{
				code:    CreateRefusalSecretPath,
				reason:  "refusing to stage .worktree.md: " + strings.Join(named, ", "),
				command: "remove the listed paths from --add",
			}, nil, nil
		}
		if options.Land {
			status, statusErr := pullRequestCreateDirtyPathsWithRunner(ctx, options.resolveRunner(), worktree)
			if statusErr != nil {
				return nil, nil, statusErr
			}
			if leftover := leftoverBeyondAddedPaths(status, paths); len(leftover) > 0 {
				return &createRefusal{
					code: CreateRefusalLeftoverBeforeLanding,
					reason: "changes outside --add would remain, and --land retires the worktree: " +
						strings.Join(leftover, ", "),
					command: "name every remaining path in --add, or drop --land and pass --keep",
				}, nil, nil
			}
		}
		addArgs := append([]string{"add", "--"}, paths...)
		if _, _, addErr := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, worktree, "git", addArgs...); addErr != nil {
			return nil, nil, fmt.Errorf("git add -- %s: %w", strings.Join(paths, " "), addErr)
		}
		// Checking the STAGED diff, not the requested paths, is what catches a
		// secret inside a named directory (`--add config` where config/.env
		// exists): the requested path is a directory name that never looks
		// like a secret by itself, but the files `git add` actually staged do.
		staged, stagedErr := stagedFileList(ctx, options.resolveRunner(), worktree, options.Timeout, options.Retry, paths...)
		if stagedErr != nil {
			return nil, nil, stagedErr
		}
		secrets := pullRequestCreateSecretPaths(staged)
		if len(secrets) > 0 {
			unstageArgs := append([]string{"reset", "-q", "--"}, paths...)
			_, _, _ = runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, worktree, "git", unstageArgs...)
			return &createRefusal{
				code:    CreateRefusalSecretPath,
				reason:  "refusing to stage what looks like a secret: " + strings.Join(secrets, ", "),
				command: "remove the listed paths from --add",
			}, nil, nil
		}
		commitArgs := append([]string{"commit", "-m", options.Message, "--"}, paths...)
		if _, _, commitErr := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, worktree, "git", commitArgs...); commitErr != nil {
			if isNothingToCommit(commitErr) {
				// A rerun with HEAD already ahead (the previous invocation's
				// commit already landed the named paths) finds nothing left to
				// commit here; that is success, not a refusal, so this
				// continues rather than erroring on a change that already
				// happened.
				return nil, nil, nil
			}
			return nil, nil, fmt.Errorf("git commit: %w", commitErr)
		}
		committed, err := pullRequestCreateCommittedPaths(ctx, options.resolveRunner(), worktree, options.Timeout, options.Retry)
		return nil, committed, err
	}
	if options.CommitStaged {
		staged, _, stagedErr := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, worktree, "git", "diff", "--cached", "--name-only")
		if stagedErr != nil {
			return nil, nil, fmt.Errorf("read staged changes: %w", stagedErr)
		}
		if len(splitNonEmptyLines(staged)) == 0 {
			return &createRefusal{
				code:    CreateRefusalNothingStaged,
				reason:  "nothing is staged; --commit-staged commits exactly the index",
				command: "git add <paths>, then retry, or pass --commit-all",
			}, nil, nil
		}
		if options.Land {
			status, statusErr := pullRequestCreateDirtyPathsWithRunner(ctx, options.resolveRunner(), worktree)
			if statusErr != nil {
				return nil, nil, statusErr
			}
			if leftover := leftoverAfterStagedCommit(status); len(leftover) > 0 {
				return &createRefusal{
					code: CreateRefusalLeftoverBeforeLanding,
					reason: "unstaged or untracked changes would remain after --commit-staged, and --land retires the worktree: " +
						strings.Join(leftover, ", "),
					command: "pass --commit-all instead, or drop --land and pass --keep",
				}, nil, nil
			}
		}
	}
	if options.CommitAll {
		if _, _, addErr := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, worktree, "git", "add", "-A"); addErr != nil {
			return nil, nil, fmt.Errorf("git add -A: %w", addErr)
		}
		// .worktree.md is untracked and git-ignored on purpose (see CLAUDE.md),
		// so `git add -A` never stages it in the first place. This unstages it
		// defensively anyway, and is a no-op — never an error — when it was
		// never staged to begin with.
		_, _, _ = runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, worktree, "git", "reset", "-q", "--", ".worktree.md")
		// Checking the STAGED diff, not `git status --porcelain`, is what
		// catches a secret inside an untracked directory: porcelain collapses
		// an untracked directory to its own name ("?? config/"), never
		// listing config/.env, while the staged diff lists every file `git
		// add -A` actually staged.
		staged, stagedErr := stagedFileList(ctx, options.resolveRunner(), worktree, options.Timeout, options.Retry)
		if stagedErr != nil {
			return nil, nil, stagedErr
		}
		secrets := pullRequestCreateSecretPaths(staged)
		if len(secrets) > 0 {
			_, _, _ = runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, worktree, "git", "reset", "-q")
			return &createRefusal{
				code:    CreateRefusalSecretPath,
				reason:  "refusing to stage what looks like a secret: " + strings.Join(secrets, ", "),
				command: "remove the listed paths from the change, or stage the safe paths explicitly and use --commit-staged",
			}, nil, nil
		}
	}
	if _, _, commitErr := runCommand(ctx, options.resolveRunner(), options.Timeout, options.Retry, worktree, "git", "commit", "-m", options.Message); commitErr != nil {
		if isNothingToCommit(commitErr) {
			// A rerun with HEAD already ahead: the previous invocation's
			// commit already happened, --commit-staged/--commit-all now find
			// nothing left to add, and that is success, not a refusal.
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("git commit: %w", commitErr)
	}
	committed, err = pullRequestCreateCommittedPaths(ctx, options.resolveRunner(), worktree, options.Timeout, options.Retry)
	return nil, committed, err
}

// resolveAddPaths validates --add's own paths against worktree: each must
// resolve inside it (no ".." escape, and no absolute path outside it), and
// must match something real — an existing path, or a tracked path this
// worktree's own `git status` still reports, which is how a deleted tracked
// file is named without existing on disk any more. It returns each path
// relative to worktree, using forward slashes, in the order named.
func resolveAddPaths(ctx context.Context, run runner.Runner, worktree string, raw []string) ([]string, error) {
	return resolveAddPathsWithPathResolver(ctx, run, worktree, raw, filepath.Abs)
}

// resolveAddPathsWithPathResolver keeps the native path boundary observable on
// platforms that can still resolve an unlinked current directory.
func resolveAddPathsWithPathResolver(ctx context.Context, run runner.Runner, worktree string, raw []string, resolve func(string) (string, error)) ([]string, error) {
	absWorktree, err := resolve(worktree)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree %s: %w", worktree, err)
	}
	var invalid []string
	var missing []string
	resolved := make([]string, 0, len(raw))
	for _, path := range raw {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		var absPath string
		if filepath.IsAbs(trimmed) {
			absPath = filepath.Clean(trimmed)
		} else {
			absPath = filepath.Clean(filepath.Join(absWorktree, trimmed))
		}
		rel, relErr := filepath.Rel(absWorktree, absPath)
		if relErr != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			invalid = append(invalid, path)
			continue
		}
		rel = filepath.ToSlash(rel)
		if _, statErr := os.Stat(absPath); statErr != nil {
			statusOutput, _, gitErr := runCommand(ctx, run, 0, 0, worktree, "git", "status", "--porcelain", "--", rel)
			if gitErr != nil {
				return nil, fmt.Errorf("inspect --add path %s: %w", path, gitErr)
			}
			if strings.TrimSpace(statusOutput) == "" {
				missing = append(missing, path)
				continue
			}
		}
		resolved = append(resolved, rel)
	}
	if len(invalid) > 0 {
		return nil, fmt.Errorf("--add paths must resolve inside the worktree: %s", strings.Join(invalid, ", "))
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("--add names paths that match nothing: %s", strings.Join(missing, ", "))
	}
	return resolved, nil
}

// pullRequestCreateDirtyPathsWithRunner lists every uncommitted entry in worktree, or
// nil for a clean one.
func pullRequestCreateDirtyPathsWithRunner(ctx context.Context, run runner.Runner, worktree string) ([]worktreeStatusEntry, error) {
	entries, err := readPorcelainStatus(ctx, run, 0, worktree)
	if err != nil {
		return nil, fmt.Errorf("read worktree status: %w", err)
	}
	return entries, nil
}

// stagedFileList lists every path currently staged (the diff between HEAD and
// the index), optionally scoped to pathspecs, using NUL-separated output so a
// path holding whitespace or a shell-special character is read as exactly the
// bytes Git staged rather than through `git status --porcelain`'s quoting —
// and so a secret inside an untracked DIRECTORY is seen as the individual
// file it actually is, never collapsed to the directory's own name.
func stagedFileList(ctx context.Context, run runner.Runner, worktree string, timeout time.Duration, retry int, pathspecs ...string) ([]string, error) {
	args := []string{"diff", "--cached", "--name-only", "-z"}
	if len(pathspecs) > 0 {
		args = append(args, "--")
		args = append(args, pathspecs...)
	}
	output, _, err := runCommand(ctx, run, timeout, retry, worktree, "git", args...)
	if err != nil {
		return nil, fmt.Errorf("read staged paths: %w", err)
	}
	var paths []string
	for _, path := range strings.Split(output, "\x00") {
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// isNothingToCommit reports whether a failed `git commit` failed only
// because there was nothing left to commit, the way a rerun of
// --commit-staged/--commit-all/--add finds the worktree already exactly at
// the state the previous, successful invocation already committed.
func isNothingToCommit(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "nothing to commit") || strings.Contains(message, "no changes added to commit")
}

// leftoverAfterStagedCommit names every entry that a plain commit of the
// index would still leave dirty: an unstaged modification (worktree status
// column is not blank) or an untracked file ("??").
func leftoverAfterStagedCommit(entries []worktreeStatusEntry) []string {
	var leftover []string
	for _, entry := range entries {
		if entry.worktree != ' ' {
			leftover = append(leftover, entry.path)
		}
	}
	return leftover
}

// leftoverBeyondAddedPaths names every status path that --add does not cover:
// with --land, any such change — staged, unstaged, or untracked — would be
// left behind when the worktree is retired. An --add path covers itself and,
// when it names a directory, everything beneath it (Git reports an untracked
// directory as "dir/" and a modified tracked one file by file).
func leftoverBeyondAddedPaths(entries []worktreeStatusEntry, paths []string) []string {
	var leftover []string
	for _, entry := range entries {
		if !addedPathCovers(paths, entry.path) {
			leftover = append(leftover, entry.path)
		}
	}
	return leftover
}

func addedPathCovers(added []string, statusPath string) bool {
	statusPath = strings.TrimSuffix(statusPath, "/")
	for _, path := range added {
		path = strings.TrimSuffix(path, "/")
		if statusPath == path || strings.HasPrefix(statusPath, path+"/") {
			return true
		}
	}
	return false
}

// looksLikeSecretPath is a defensive filename heuristic, not a content scan:
// it exists so `--commit-all`'s blanket `git add -A` cannot silently stage a
// credential a reviewer would have caught by eye. Matching is
// case-insensitive: a forge's own case-folding, or a careless rename, must
// not be the difference between refused and silently committed.
func looksLikeSecretPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	if strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".p12") {
		return true
	}
	return strings.HasPrefix(base, "id_rsa") || strings.HasPrefix(base, "id_ed25519") || strings.HasPrefix(base, "id_ecdsa")
}
func pullRequestCreateSecretPaths(staged []string) []string {
	var secrets []string
	for _, path := range staged {
		if looksLikeSecretPath(path) {
			secrets = append(secrets, path)
		}
	}
	return secrets
}

func pullRequestCreateCommittedPaths(ctx context.Context, run runner.Runner, worktree string, timeout time.Duration, retry int) ([]string, error) {
	committedRaw, _, treeErr := runCommand(ctx, run, timeout, retry, worktree, "git", "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	if treeErr != nil {
		return nil, fmt.Errorf("read committed paths: %w", treeErr)
	}
	return splitNonEmptyLines(committedRaw), nil
}
