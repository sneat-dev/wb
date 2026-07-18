// Package gitops wraps the git and gh commands needed to read the default
// branch of a repo and to land a change on it — pushing directly when allowed,
// or opening an auto-merge PR when the branch is protected. The flow mirrors
// the proven all-repos-codegrapher.sh script.
package gitops

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// run executes a command in dir and returns combined output.
func run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// DefaultBranch returns the remote's default branch (e.g. "main"). It first
// refreshes origin/HEAD from the remote, because a local clone's cached
// origin/HEAD can be stale (e.g. the repo was renamed master -> main after the
// clone was made), which would otherwise yield the wrong branch.
func DefaultBranch(repoPath string) (string, error) {
	_, _ = run(repoPath, "git", "remote", "set-head", "origin", "--auto")
	out, err := run(repoPath, "git", "symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil {
		ref := strings.TrimSpace(out)
		return strings.TrimPrefix(ref, "refs/remotes/origin/"), nil
	}
	out, err = run(repoPath, "git", "remote", "show", "origin")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "HEAD branch") {
			f := strings.Fields(line)
			return f[len(f)-1], nil
		}
	}
	return "", fmt.Errorf("could not determine default branch for %s", repoPath)
}

// Fetch updates remote refs.
func Fetch(repoPath string) error {
	_, err := run(repoPath, "git", "fetch", "--quiet", "origin")
	return err
}

// ShowFile returns the contents of path at ref (e.g. "origin/main"), and a
// false ok when the file does not exist at that ref.
func ShowFile(repoPath, ref, path string) (content string, ok bool, err error) {
	cmd := exec.Command("git", "show", ref+":"+path)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Missing file is an expected, non-fatal outcome.
		return "", false, nil
	}
	return string(out), true, nil
}

// Clone clones cloneURL into dest (creating parent dirs).
func Clone(cloneURL, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	_, err := run(filepath.Dir(dest), "git", "clone", "--quiet", cloneURL, dest)
	return err
}

// LandOptions configures how a change is committed and pushed.
type LandOptions struct {
	DefaultBranch string
	CommitMessage string
	PRBranch      string
	PRTitle       string
	PRBody        string
}

// Mutator edits files inside the worktree and reports whether it changed
// anything along with a human-readable detail.
type Mutator func(worktreePath string) (changed bool, detail string, err error)

// Outcome describes what Land did.
type Outcome struct {
	Changed bool
	Detail  string
	PRURL   string
}

// Land fetches, creates a detached worktree at origin/<default>, runs mutate,
// and — if it changed files — commits and pushes to the default branch, falling
// back to a PR with auto-merge when the push is rejected (protected branch).
func Land(repoPath string, opt LandOptions, mutate Mutator) (Outcome, error) {
	if err := Fetch(repoPath); err != nil {
		return Outcome{}, err
	}
	ref := "origin/" + opt.DefaultBranch

	wt, err := os.MkdirTemp("", "wb-wt-")
	if err != nil {
		return Outcome{}, err
	}
	defer func() {
		_, _ = run(repoPath, "git", "worktree", "remove", "--force", wt)
		_ = os.RemoveAll(wt)
	}()

	if _, err := run(repoPath, "git", "worktree", "add", "--detach", "--quiet", wt, ref); err != nil {
		return Outcome{}, err
	}

	changed, detail, err := mutate(wt)
	if err != nil {
		return Outcome{}, err
	}
	if !changed {
		return Outcome{Changed: false, Detail: detail}, nil
	}

	if _, err := run(wt, "git", "add", "-A"); err != nil {
		return Outcome{}, err
	}
	if _, err := run(wt, "git", "commit", "-m", opt.CommitMessage); err != nil {
		return Outcome{}, err
	}

	// If the local working copy is dirty (uncommitted changes or unpushed
	// commits), never push to the default branch — open a PR so the user's
	// in-progress work is left untouched. Otherwise try a direct push first and
	// fall back to a PR when the branch is protected.
	if dirty, reason, derr := LocalState(repoPath); derr == nil && dirty {
		return openPR(wt, opt, "local has "+reason)
	} else if _, perr := run(wt, "git", "push", "origin", "HEAD:"+opt.DefaultBranch); perr == nil {
		return Outcome{Changed: true, Detail: "pushed to " + opt.DefaultBranch}, nil
	}
	return openPR(wt, opt, "protected branch")
}

// openPR pushes the change on a named branch and opens an auto-merge PR.
// reason is appended to the outcome detail for visibility. The worktree is
// detached, so we create a real local branch first: branch-name pre-push hooks
// read the local ref, and pushing from detached HEAD shows up as "HEAD" and is
// rejected by hooks that enforce a naming convention.
func openPR(wt string, opt LandOptions, reason string) (Outcome, error) {
	if _, err := run(wt, "git", "checkout", "-B", opt.PRBranch); err != nil {
		return Outcome{}, fmt.Errorf("create branch %s failed: %w", opt.PRBranch, err)
	}
	if _, err := run(wt, "git", "push", "--force-with-lease", "origin", opt.PRBranch); err != nil {
		return Outcome{}, fmt.Errorf("push to %s failed: %w", opt.PRBranch, err)
	}
	prOut, err := run(wt, "gh", "pr", "create",
		"--title", opt.PRTitle,
		"--body", opt.PRBody,
		"--base", opt.DefaultBranch,
		"--head", opt.PRBranch)
	if err != nil {
		return Outcome{}, fmt.Errorf("PR creation failed: %w", err)
	}
	prURL := lastLine(prOut)
	// Best-effort auto-merge; ignore failure (checks may not be configured).
	_, _ = run(wt, "gh", "pr", "merge", "--auto", "--squash", prURL)
	return Outcome{Changed: true, Detail: "PR " + prURL + " (" + reason + ")", PRURL: prURL}, nil
}

// LocalState reports whether repoPath has uncommitted changes (including
// untracked files) or local commits not present on any remote. The reason
// string names the first condition found.
func LocalState(repoPath string) (dirty bool, reason string, err error) {
	out, err := run(repoPath, "git", "status", "--porcelain")
	if err != nil {
		return false, "", err
	}
	if strings.TrimSpace(out) != "" {
		return true, "uncommitted changes", nil
	}
	out, err = run(repoPath, "git", "log", "--branches", "--not", "--remotes", "--oneline")
	if err != nil {
		return false, "", err
	}
	if strings.TrimSpace(out) != "" {
		return true, "unpushed commits", nil
	}
	return false, "", nil
}

// WorktreeChanged reports whether the worktree at dir has any uncommitted
// changes (tracked edits or untracked files), via `git status --porcelain`.
func WorktreeChanged(dir string) (bool, error) {
	out, err := run(dir, "git", "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// Pull runs `git pull --quiet` on the currently checked-out branch of
// repoPath.
func Pull(repoPath string) error {
	_, err := run(repoPath, "git", "pull", "--quiet")
	return err
}

// RepoStatus is git working-tree/history state relevant to sync decisions
// and to reporting why a repo needs attention.
type RepoStatus struct {
	Modified   []string // tracked files with changes
	Untracked  []string // untracked files
	Conflicted []string // merge-conflict paths
	Unpushed   []string // `git log --branches --not --remotes --oneline` lines
	Stashed    []string // `git stash list` lines
}

// WorkingTreeDirty reports whether the working tree itself has changes
// (modified, untracked, or conflicted files) — the check used to decide
// whether it is safe to `git pull`.
func (s RepoStatus) WorkingTreeDirty() bool {
	return len(s.Modified) > 0 || len(s.Untracked) > 0 || len(s.Conflicted) > 0
}

// Dirty reports whether s represents any state — working tree, stash, or
// unpushed commits — that should block automatically removing an archived
// repo's local clone.
func (s RepoStatus) Dirty() bool {
	return s.WorkingTreeDirty() || len(s.Unpushed) > 0 || len(s.Stashed) > 0
}

// Summary renders a short human-readable description of s, e.g. "3 modified
// files, 1 untracked file". Empty when nothing is set.
func (s RepoStatus) Summary() string {
	var parts []string
	if n := len(s.Modified); n > 0 {
		parts = append(parts, plural(n, "modified file"))
	}
	if n := len(s.Untracked); n > 0 {
		parts = append(parts, plural(n, "untracked file"))
	}
	if n := len(s.Conflicted); n > 0 {
		parts = append(parts, plural(n, "conflict"))
	}
	if n := len(s.Unpushed); n > 0 {
		parts = append(parts, plural(n, "unpushed commit"))
	}
	if n := len(s.Stashed); n > 0 {
		parts = append(parts, plural(n, "stash entry"))
	}
	return strings.Join(parts, ", ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// isConflictCode reports whether a two-character `git status --porcelain`
// status code indicates an unresolved merge conflict.
func isConflictCode(code string) bool {
	switch code {
	case "UU", "AA", "DD", "AU", "UA", "UD", "DU":
		return true
	default:
		return false
	}
}

// Status reads repoPath's working tree, stash, and unpushed-commit state.
func Status(repoPath string) (RepoStatus, error) {
	var s RepoStatus

	out, err := run(repoPath, "git", "status", "--porcelain")
	if err != nil {
		return s, err
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 3 {
			continue
		}
		code := line[:2]
		path := strings.TrimSpace(line[3:])
		switch {
		case isConflictCode(code):
			s.Conflicted = append(s.Conflicted, path)
		case code == "??":
			s.Untracked = append(s.Untracked, path)
		default:
			s.Modified = append(s.Modified, path)
		}
	}

	out, err = run(repoPath, "git", "stash", "list")
	if err != nil {
		return s, err
	}
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			s.Stashed = append(s.Stashed, line)
		}
	}

	out, err = run(repoPath, "git", "log", "--branches", "--not", "--remotes", "--oneline")
	if err != nil {
		return s, err
	}
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			s.Unpushed = append(s.Unpushed, line)
		}
	}

	return s, nil
}
