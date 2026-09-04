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
	// Local is true only for a canonical repository's default
	// <canonical>/.worktrees root. Home remains WB_HOME authority.
	Local bool
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
// Root never creates the directory itself: worktrees.Create opens this same
// path through its own descriptor-anchored, symlink-rejecting check, and
// that check needs the first, unhardened look at whether anything already
// sits there. Callers with no hardening of their own should call EnsureRoot
// instead, so the home directory still stays self-documenting.
func Root(projectsRoot string) (string, error) {
	resolution, err := Resolve(projectsRoot)
	if err != nil {
		return "", err
	}
	return resolution.Write.Home, nil
}

// EnsureRoot resolves WB's authoritative write home exactly like Root, then
// ensures the directory exists and carries a README explaining what it is.
// Use this for callers about to create state under home with no
// home-directory hardening of their own; see Root's doc for the one caller
// that must not.
func EnsureRoot(projectsRoot string) (string, error) {
	home, err := Root(projectsRoot)
	if err != nil {
		return "", err
	}
	if err := EnsureHome(home); err != nil {
		return "", err
	}
	return home, nil
}

// EnsureHome creates home (and any missing ancestor, e.g. a projects root a
// test fixture hasn't created yet) if it doesn't exist — refusing to accept a
// symlink already planted at that exact path — and seeds it with a README.
// It does not guard ancestor components against a symlink the way the
// worktree-create path's descriptor-anchored open does; its callers never had
// that guarantee before this function existed either.
func EnsureHome(home string) error {
	info, err := os.Lstat(home)
	if errors.Is(err, os.ErrNotExist) {
		if mkdirErr := os.MkdirAll(home, 0o755); mkdirErr != nil {
			return fmt.Errorf("create WB home %s: %w", home, mkdirErr)
		}
		info, err = os.Lstat(home)
	}
	if err != nil {
		return fmt.Errorf("inspect WB home %s: %w", home, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("WB home %s is symlinked; refusing to use it", home)
	}
	if !info.IsDir() {
		return fmt.Errorf("WB home path is not a directory: %s", home)
	}
	return SeedReadme(home)
}

// readmeContent explains what this directory is to anyone who stumbles into
// it outside of WB itself.
const readmeContent = `# WB home

This directory holds WB-managed state: task worktrees and operation locks,
plus durable private Work Logs, prompt archives, cleanup backlogs, recovery
evidence, and command reports. Do not delete this directory or its contents
manually, even when no WB command is running. Use WB lifecycle commands so
recovery and audit evidence is preserved.

Learn more about the WB CLI at https://sneat.dev/workbench.
`

// SeedReadme writes README.md into home if one isn't already there. An
// existing README — including one an operator customised — is left alone.
// Callers must have already established that home is a real directory.
func SeedReadme(home string) error {
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
