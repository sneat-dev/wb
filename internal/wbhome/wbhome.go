// Package wbhome resolves the single directory WB uses to coordinate work
// across agents and sessions: task worktrees, operation locks, and reports.
//
// That directory used to live at <projects-root>/.wb. A recursive tool that
// doesn't know WB's exclusion rules — a search indexer, backup, an ad-hoc
// grep — walks straight into it and double-counts every in-flight worktree as
// a separate repository. Moving the default to the user's home directory
// makes "don't walk into WB's state" the default for every tool, not a rule
// each one has to learn.
package wbhome

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvOverride names the environment variable that pins WB's home directory,
// overriding both the new default and legacy detection. Tests use it to stay
// hermetic; operators use it for unusual layouts.
const EnvOverride = "WB_HOME"

// Root resolves the directory WB reads and writes its shared state under:
// worktrees, operation locks, and reports.
//
// $WB_HOME wins when set. Otherwise, if projectsRoot already has a populated
// .wb directory, that legacy location is reused: an existing worktree is a
// live coordination point that another agent or session may depend on right
// now, and relocating it out from under active work would strand that work
// with no warning. A fresh install, or this same install once its legacy .wb
// directory is gone, gets the new default: ~/.wb.
//
// The result is always symlink-resolved, matching how the rest of WB resolves
// projectsRoot. On macOS, a temp directory (and so, in tests, $WB_HOME) lives
// under /var, itself a symlink to /private/var; git reports worktree paths
// through the resolved form. An unresolved root here would make WB's own path
// bookkeeping disagree with what `git rev-parse --show-toplevel` reports for
// the exact same directory.
func Root(projectsRoot string) (string, error) {
	if override := strings.TrimSpace(os.Getenv(EnvOverride)); override != "" {
		return resolveAbs(override)
	}
	if root := strings.TrimSpace(projectsRoot); root != "" {
		legacy := filepath.Join(root, ".wb")
		if entries, err := os.ReadDir(legacy); err == nil && len(entries) > 0 {
			return resolveAbs(legacy)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	return resolveAbs(filepath.Join(home, ".wb"))
}

// resolveAbs makes path absolute and resolves symlinks in it. WB's home
// directory usually doesn't exist yet on a first run, and EvalSymlinks cannot
// resolve a path whose final component is missing — so this resolves the
// nearest existing ancestor and rejoins the rest, rather than skipping
// resolution the moment the leaf doesn't exist. Skipping it would leave a
// symlinked ancestor (macOS's /var -> /private/var, which is exactly what
// os.TempDir returns) unresolved precisely when the directory is about to be
// created for the first time.
func resolveAbs(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(absolute)
	if parent == absolute {
		// Reached the filesystem root without it resolving; nothing further
		// to strip. Treat the root itself as already resolved.
		return absolute, nil
	}
	resolvedParent, err := resolveAbs(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(absolute)), nil
}
