package hooks

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func gitOutput(repoPath string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", repoPath}, args...)
	cmd := exec.Command("git", commandArgs...)
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
	return filepath.Clean(dir), nil
}

func currentHooksPath(repoRoot string) (string, error) {
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

func setHooksPath(repoRoot, path string) error {
	_, err := gitOutput(repoRoot, "config", "--local", "core.hooksPath", path)
	return err
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
