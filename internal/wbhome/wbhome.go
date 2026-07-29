// Package wbhome resolves the directories WB uses to coordinate work across
// agents and sessions: task worktrees, operation locks, and reports.
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

// EnvMigrationCompat is written only by a managed hook that pinned the normal
// default home at installation time. Its value must be that resolved default
// home, rather than a generic boolean. This lets the resolver distinguish the
// one migration-compatible hook context from an arbitrary explicit WB_HOME.
const EnvMigrationCompat = "WB_HOME_MIGRATION_COMPAT"

// Layout is one supported on-disk WB state layout. Home is the parent of its
// worktrees, locks, and reports. Legacy is true only for the historic
// <projects-root>/.wb layout that remains readable during the migration.
type Layout struct {
	Home          string
	WorktreesRoot string
	Legacy        bool
}

// Resolution makes the migration policy explicit. Write is the only layout
// where new state may be created; Read contains Write plus a discovered legacy
// layout when the default migration path can safely support it.
//
// An explicit WB_HOME is intentionally authoritative: it is commonly used by
// parallel agents and hermetic tests, neither of which may accidentally scan
// or mutate a neighbouring projects-root legacy directory.
type Resolution struct {
	Write    Layout
	Read     []Layout
	Explicit bool
}

// Resolve returns the write home and every compatible read layout for one
// projects root. New state always belongs under ~/.wb by default; the legacy
// projects-root directory is never selected as a silent write fallback.
func Resolve(projectsRoot string) (Resolution, error) {
	home, explicit, err := writeHome()
	if err != nil {
		return Resolution{}, err
	}
	write := newLayout(home, false)
	resolution := Resolution{Write: write, Read: []Layout{write}, Explicit: explicit}
	if explicit || strings.TrimSpace(projectsRoot) == "" {
		return resolution, nil
	}
	legacy, err := resolveAbs(filepath.Join(projectsRoot, ".wb"))
	if err != nil {
		return Resolution{}, err
	}
	if filepath.Clean(legacy) == filepath.Clean(write.Home) || !hasWorktrees(legacy) {
		return resolution, nil
	}
	resolution.Read = append(resolution.Read, newLayout(legacy, true))
	return resolution, nil
}

// Root resolves WB's authoritative write home. It remains for callers that
// only create state; worktree migration-aware callers must use Resolve.
//
// Every such caller is about to create state below this directory, so Root
// ensures the directory itself exists and carries a README before returning
// it — the home directory stays self-documenting no matter which command
// creates it first.
func Root(projectsRoot string) (string, error) {
	resolution, err := Resolve(projectsRoot)
	if err != nil {
		return "", err
	}
	home := resolution.Write.Home
	if err := os.MkdirAll(home, 0o755); err != nil {
		return "", fmt.Errorf("create WB home %s: %w", home, err)
	}
	if err := writeReadme(home); err != nil {
		return "", err
	}
	return home, nil
}

// readmeContent explains what this directory is to anyone who stumbles into
// it outside of WB itself.
const readmeContent = `# WB home

This directory holds WB's shared state: task worktrees, operation locks, and
command reports. WB manages its own layout below here — it's safe to delete
whenever no WB command is running, since WB recreates whatever it needs.

Learn more about the WB CLI at https://sneat.dev/workbench.
`

// writeReadme seeds home with a README.md the first time WB creates it. An
// existing README — including one an operator customised — is left alone.
func writeReadme(home string) error {
	path := filepath.Join(home, "README.md")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect WB home README %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(readmeContent), 0o644); err != nil {
		return fmt.Errorf("write WB home README %s: %w", path, err)
	}
	return nil
}

func writeHome() (home string, explicit bool, err error) {
	if override := strings.TrimSpace(os.Getenv(EnvOverride)); override != "" {
		root, resolveErr := resolveAbs(override)
		if resolveErr != nil {
			return "", true, resolveErr
		}
		// A generic ambient marker must never make an explicitly selected home
		// migration-compatible. Managed shims pin both WB_HOME and this marker
		// to the exact, resolved default location. Only that exact pairing is
		// allowed to discover the legacy layout.
		return root, !isPinnedDefaultHome(root), nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", false, fmt.Errorf("resolve user home directory: %w", err)
	}
	root, err := resolveAbs(filepath.Join(userHome, ".wb"))
	return root, false, err
}

func isPinnedDefaultHome(home string) bool {
	marker := strings.TrimSpace(os.Getenv(EnvMigrationCompat))
	if marker == "" {
		return false
	}
	pinned, err := resolveAbs(marker)
	if err != nil || filepath.Clean(pinned) != filepath.Clean(home) {
		return false
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	defaultHome, err := resolveAbs(filepath.Join(userHome, ".wb"))
	return err == nil && filepath.Clean(home) == filepath.Clean(defaultHome)
}

func newLayout(home string, legacy bool) Layout {
	return Layout{Home: home, WorktreesRoot: filepath.Join(home, "worktrees"), Legacy: legacy}
}

func hasWorktrees(home string) bool {
	info, err := os.Stat(filepath.Join(home, "worktrees"))
	return err == nil && info.IsDir()
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
