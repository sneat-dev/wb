package locallink

import (
	"fmt"
	"path/filepath"
	"strings"
)

// workspacePath resolves a recorded repository-relative workspace without
// permitting an absolute path, `..`, or a symlink to escape the worktree.
func workspacePath(worktree, relative string) (string, error) {
	if strings.TrimSpace(relative) == "" || relative == "." {
		return filepath.Clean(worktree), nil
	}
	fromSlash := filepath.FromSlash(relative)
	clean := filepath.Clean(fromSlash)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("npm workspace %q is not inside worktree %s", relative, worktree)
	}
	candidate := filepath.Join(worktree, clean)
	resolvedRoot, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return "", fmt.Errorf("resolve worktree %s: %w", worktree, err)
	}
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve npm workspace %s: %w", candidate, err)
	}
	within, err := filepath.Rel(resolvedRoot, resolvedCandidate)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("npm workspace %q resolves outside worktree %s", relative, worktree)
	}
	return candidate, nil
}
