package gitops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// CommitPaths records the working-tree state of paths (additions,
// modifications and deletions) as one new commit on the current branch,
// without ever staging anything in the repository's shared index. It reports
// false without committing when that state already matches HEAD, so callers
// can publish idempotently.
//
// It replaces the old `git add` + `git commit` pair for the remote state
// store (sneat-dev/wb#899). That pair left the files staged in the shared
// index whenever the commit step did not finish — the process was
// interrupted, or `git commit` itself refused, as WB's own pre-commit guard
// does in a canonical clone that has staged changes — and every later
// `git rebase` then refused with "Your index contains uncommitted changes".
// Here the tree is built in a private, throwaway index (GIT_INDEX_FILE),
// turned into a commit with `git commit-tree`, and published with a
// compare-and-swap `git update-ref`; nothing outside the private index is
// touched until the branch already points at the new commit. Only then are
// the shared index entries for paths brought up to date, best effort: a
// failure there leaves index entries that lag HEAD, which the state store's
// self-heal repairs on its next sync, never a lost or half-made commit.
func CommitPaths(repoPath, message string, paths ...string) (bool, error) {
	scratch, err := os.MkdirTemp("", "wb-index-")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(scratch, "index")}

	// An unborn branch (the first commit into an empty store) has no HEAD:
	// the private index starts empty, the commit has no parent, and the CAS
	// below requires the branch to still not exist.
	parentOut, revErr := run(repoPath, "git", "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	parent := strings.TrimSpace(parentOut)
	commitArgs := []string{"commit-tree", "-m", message}
	if revErr == nil {
		if _, err := runEnv(repoPath, env, "git", "read-tree", parent); err != nil {
			return false, err
		}
		commitArgs = append(commitArgs, "-p", parent)
	} else {
		parent = ""
	}
	if _, err := runEnv(repoPath, env, "git", append([]string{"add", "--all", "--"}, paths...)...); err != nil {
		return false, err
	}
	treeOut, err := runEnv(repoPath, env, "git", "write-tree")
	if err != nil {
		return false, err
	}
	tree := strings.TrimSpace(treeOut)
	if parent != "" {
		parentTree, err := run(repoPath, "git", "rev-parse", parent+"^{tree}")
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(parentTree) == tree {
			return false, nil
		}
	}
	commitOut, err := run(repoPath, "git", append(commitArgs, tree)...)
	if err != nil {
		return false, err
	}
	if _, err := run(repoPath, "git", "update-ref", "-m", "commit: "+message, "HEAD", strings.TrimSpace(commitOut), parent); err != nil {
		return false, err
	}
	_, _ = run(repoPath, "git", append([]string{"reset", "-q", "--"}, paths...)...)
	return true, nil
}

// ChangedPath is one entry of `git status --porcelain`: its two-letter XY
// status code and its repository-relative, slash-separated path.
type ChangedPath struct {
	Code string
	Path string
}

// Untracked reports whether the entry is a file Git does not track at all.
func (c ChangedPath) Untracked() bool { return c.Code == "??" }

// ChangedPaths lists every path whose index or working-tree state differs
// from HEAD, one entry per file (untracked directories are expanded and
// renames are reported as a deletion plus an addition). Ignored files are
// not listed.
func ChangedPaths(repoPath string) ([]ChangedPath, error) {
	out, err := run(repoPath, "git", "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return nil, err
	}
	var changed []ChangedPath
	for _, entry := range strings.Split(out, "\x00") {
		if len(entry) > 3 {
			changed = append(changed, ChangedPath{Code: entry[:2], Path: entry[3:]})
		}
	}
	return changed, nil
}

// DiscardPaths returns each of paths (exact repository-relative files) to its
// HEAD state in both the index and the working tree: a staged or unstaged
// change is undone, a deleted tracked file is restored, and a file HEAD does
// not have is removed. It uses plumbing commands only, so no checkout hook
// runs. Works on an unborn branch, where every path is simply removed.
func DiscardPaths(repoPath string, paths ...string) error {
	if _, err := run(repoPath, "git", append([]string{"reset", "-q", "--"}, paths...)...); err != nil {
		return err
	}
	out, err := run(repoPath, "git", append([]string{"ls-files", "-z", "--"}, paths...)...)
	if err != nil {
		return err
	}
	tracked := map[string]bool{}
	restore := []string{"checkout-index", "-f", "-q", "--"}
	for _, path := range strings.Split(out, "\x00") {
		if path != "" {
			tracked[path] = true
			restore = append(restore, path)
		}
	}
	if len(tracked) > 0 {
		if _, err := run(repoPath, "git", restore...); err != nil {
			return err
		}
	}
	for _, path := range paths {
		if tracked[path] {
			continue
		}
		if err := os.Remove(filepath.Join(repoPath, filepath.FromSlash(path))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
