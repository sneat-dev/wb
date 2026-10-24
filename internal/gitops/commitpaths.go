package gitops

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/console"
)

// runStdout is runEnv returning stdout alone, for commands whose output is
// parsed: git reports warnings such as an unreadable directory on stderr, and
// those must never be read as data. Stderr still goes into the error.
func runStdout(dir string, extra []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(console.Env(), extra...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// CommitPaths stages paths (additions, modifications and deletions) and
// commits them with an ordinary `git commit`, so the repository's hooks run
// exactly as they would for `git add` + `git commit` — but it stages them in a
// private copy of the index (GIT_INDEX_FILE), never in the shared one. It
// reports false without committing when nothing changed, so callers can
// publish idempotently.
//
// It replaces the old `git add` + `git commit` pair (sneat-dev/wb#899). That
// pair left the files staged in the shared index whenever the commit did not
// finish — the process was interrupted, or a pre-commit hook refused (WB's own
// canonical-clean guard did, in the remote-store clone) — and every later
// `git rebase` then refused with "Your index contains uncommitted changes".
// The private index starts as a copy of the shared one, so git reuses its
// stat cache instead of re-hashing every tracked file. A failed or refused
// commit leaves the shared index exactly as it was. Only after the commit has
// landed are the shared index entries for paths brought up to date, best
// effort: a failure there leaves entries that lag HEAD, never a lost or
// half-made commit.
func CommitPaths(repoPath, message string, paths ...string) (bool, error) {
	scratch, err := os.MkdirTemp("", "wb-index-")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	private := filepath.Join(scratch, "index")
	env := []string{"GIT_INDEX_FILE=" + private}

	sharedOut, err := runStdout(repoPath, nil, "git", "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return false, err
	}
	// An unborn branch may have no index yet; the private index then starts
	// empty, exactly as git's own would.
	data, err := os.ReadFile(strings.TrimSpace(sharedOut))
	if err == nil {
		err = os.WriteFile(private, data, 0o600)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if _, err := runEnv(repoPath, env, "git", append([]string{"add", "--all", "--"}, paths...)...); err != nil {
		return false, err
	}
	_, err = runEnv(repoPath, env, "git", "diff", "--cached", "--quiet")
	if err == nil {
		return false, nil
	}
	if exitCode(err) != 1 {
		return false, fmt.Errorf("inspect staged changes: %w", err)
	}
	if _, err := runEnv(repoPath, env, "git", "commit", "--quiet", "-m", message); err != nil {
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
	out, err := runStdout(repoPath, nil, "git", "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames")
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
	out, err := runStdout(repoPath, nil, "git", append([]string{"ls-files", "-z", "--"}, paths...)...)
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
