package hooks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/console"
	"golang.org/x/sys/unix"
)

// SecureHooksGitHelperArgument selects the private WB child-process path that
// enters an inherited repository descriptor before writing core.hooksPath.
// It is handled before normal CLI parsing and is not a user command.
const SecureHooksGitHelperArgument = "--wb-internal-hooks-git"

func gitOutput(repoPath string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	// Run from the requested repository instead of spelling it with `git -C`.
	// Git invokes hooks with relative GIT_DIR/GIT_WORK_TREE values. `-C` changes
	// directory *before* resolving those values, so a hook already running from
	// `.git` would incorrectly resolve GIT_DIR=. as `<repo>/.git/.git`. Setting
	// the child working directory preserves the invoking Git context and yields
	// the same behavior for ordinary callers.
	cmd.Dir = repoPath
	cmd.Env = console.Env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// RepositoryRoot resolves path to the enclosing non-bare Git worktree.
func RepositoryRoot(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	root, err := gitOutput(absolute, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%s is not a Git worktree: %w", absolute, err)
	}
	return filepath.Clean(root), nil
}

func gitCommonDir(repoRoot string) (string, error) {
	dir, err := gitOutput(repoRoot, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repoRoot, dir)
	}
	// Preserve the path Git reported. Callers that are about to create managed
	// files must inspect this pathname before resolving it: resolving first
	// would hide a canonical clone whose .git directory is a symlink and could
	// make WB write hook files outside the repository.
	return filepath.Clean(dir), nil
}

func currentHooksPath(repoRoot string) (string, error) {
	path, err := configuredHooksPath(repoRoot)
	if err != nil || path == "" {
		return path, err
	}
	return resolveGitPath(path), nil
}

// configuredHooksPath preserves the lexical core.hooksPath spelling that Git
// stores. Installers use this before resolving symlinks so a configured
// .git/wb-hooks symlink cannot be misclassified as an unrelated external
// hooks directory and skipped by a safety check.
func configuredHooksPath(repoRoot string) (string, error) {
	value, err := gitOutput(repoRoot, "config", "--local", "--get", "core.hooksPath")
	if err != nil {
		// git config exits 1 when the key is absent.
		return "", nil
	}
	if value == "" {
		return "", nil
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(repoRoot, value)
	}
	return filepath.Clean(value), nil
}

func resolveGitPath(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// setHooksPathAt runs the config update from the retained repository directory
// descriptor. Git's lexical -C form would re-resolve a swapped repo pathname
// after hooks have already been validated and installed.
func setHooksPathAt(repo, common *os.File, path string) error {
	if repo == nil || common == nil {
		return fmt.Errorf("repository or Git common-directory descriptor is unavailable")
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate WB hooks helper: %w", err)
	}
	gitExecutable, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("locate Git for hooks configuration: %w", err)
	}
	gitExecutable, err = filepath.Abs(gitExecutable)
	if err != nil {
		return fmt.Errorf("make Git path absolute for hooks configuration: %w", err)
	}
	cmd := exec.Command(executable, SecureHooksGitHelperArgument, path, gitExecutable)
	cmd.Env = console.Env()
	cmd.ExtraFiles = []*os.File{repo, common}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git config core.hooksPath through retained repository: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RunSecureHooksGitHelper is the child-side handoff for configuring
// core.hooksPath. The parent supplies the already-open repository as fd 3;
// only this short-lived child changes directory via fchdir before executing an
// absolute Git path, so a path substitution cannot redirect the config write.
func RunSecureHooksGitHelper(args []string) int {
	if len(args) != 2 {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure hooks helper: expected hooks path and Git executable")
		return 1
	}
	repo := os.NewFile(uintptr(3), "wb-hooks-repository")
	if repo == nil {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure hooks helper: inherited repository directory is unavailable")
		return 1
	}
	defer func() { _ = repo.Close() }()
	common := os.NewFile(uintptr(4), "wb-hooks-common-directory")
	if common == nil {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure hooks helper: inherited Git common directory is unavailable")
		return 1
	}
	defer func() { _ = common.Close() }()
	if err := unix.Fchdir(int(common.Fd())); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure hooks helper: enter inherited Git common directory: %v\n", err)
		return 1
	}
	cmd := exec.Command(args[1], "--git-dir=.", "config", "--local", "core.hooksPath", args[0])
	cmd.Env = console.Env()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure hooks helper: configure core.hooksPath: %v\n", err)
		return 1
	}
	return 0
}

func originSlug(repoRoot string) string {
	remote, err := gitOutput(repoRoot, "remote", "get-url", "origin")
	if err != nil || remote == "" {
		return filepath.Base(repoRoot)
	}
	remote = strings.TrimSuffix(remote, ".git")
	remote = strings.TrimSuffix(remote, "/")
	if i := strings.Index(remote, "github.com:"); i >= 0 {
		return strings.TrimPrefix(remote[i+len("github.com:"):], "/")
	}
	if i := strings.Index(remote, "github.com/"); i >= 0 {
		return strings.TrimPrefix(remote[i+len("github.com/"):], "/")
	}
	parts := strings.Split(remote, "/")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], "/")
	}
	return filepath.Base(repoRoot)
}
