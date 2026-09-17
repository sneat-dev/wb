// Package wbhome resolves the directories WB uses to coordinate work across
// agents and sessions: task worktrees, operation locks, and reports.
//
// Every one of those paths derives from a single projects root.
// --projects-root (the flag) wins over WB_PROJECTS_ROOT (the environment),
// which wins over the default root, ~/projects. Private state lives at
// <root>/.wb and the checkout store at <root>/.worktrees — both dot-named
// direct children of the root, siblings of the {host} directories, so a
// single-level enumeration of the root (a `*` glob, a bare `ls`) yields host
// directories only. WB_HOME no longer selects anything.
package wbhome

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvOverride names the environment variable that selects WB's projects root.
// The --projects-root flag wins over it. WB_HOME is deliberately not consulted:
// it used to name the state directory directly, which made the state directory
// and the checkout store independent knobs.
const EnvOverride = "WB_PROJECTS_ROOT"

// EnvMigrationCompat is written only by a managed hook installed by an earlier
// release. It has no effect on path derivation any more; the constant remains
// until the hook shims stop emitting it.
const EnvMigrationCompat = "WB_HOME_MIGRATION_COMPAT"

// Layout is one supported on-disk WB location set. Home is the private
// coordination state directory (claims, locks, Work Logs, reports) and
// WorktreesRoot is where checkouts physically land for that layout. Legacy is
// true only for a retired home whose checkouts remain readable in place.
type Layout struct {
	Home          string
	WorktreesRoot string
	Legacy        bool
	// Local is true only for a canonical repository's default
	// <canonical>/.worktrees root. Home remains the state-directory authority.
	Local bool
}

// StateWorktreesRoot is the logical task namespace inside the private state
// directory: <Home>/worktrees. It holds coordination state — task shells,
// locks, retired locks — and is deliberately distinct from WorktreesRoot,
// which is a physical checkout store and may sit outside the state directory.
func (layout Layout) StateWorktreesRoot() string {
	return filepath.Join(layout.Home, "worktrees")
}

// Resolution is the layout for one projects root. Write is where new state may
// be created; Read contains Write plus any additional readable layout.
type Resolution struct {
	Write Layout
	Read  []Layout
	// Root is the projects root every path above derives from.
	Root string
}

// Resolve returns the write layout and every compatible read layout for one
// projects root. An empty projectsRoot falls back to WB_PROJECTS_ROOT and then
// to the default root.
//
// The write layout derives both paths from that one root: private state at
// <root>/.wb and the checkout store at <root>/.worktrees. A retired default
// state directory ($HOME/.wb) is added as a read-only legacy layout when it
// still holds checkouts, so guard, inventory, cleanup and relocate keep
// operating on existing placements in place instead of going blind to them.
func Resolve(projectsRoot string) (Resolution, error) {
	root, err := projectsRootAbs(projectsRoot)
	if err != nil {
		return Resolution{}, err
	}
	write := newLayout(root)
	resolution := Resolution{Write: write, Read: []Layout{write}, Root: root}
	// Coordination state — task shells, locks, retired stages — lives in the
	// logical task namespace under the state directory, not in the checkout
	// store. Register it as its own readable layout so every sweep still sees
	// it; the store layout above carries the physical checkouts.
	resolution.Read = append(resolution.Read, Layout{Home: write.Home, WorktreesRoot: write.StateWorktreesRoot()})
	legacy, found, err := legacyUserLayout()
	if err != nil {
		return Resolution{}, err
	}
	if found && filepath.Clean(legacy.Home) != filepath.Clean(write.Home) {
		resolution.Read = append(resolution.Read, legacy)
	}
	return resolution, nil
}

// legacyUserLayout describes the retired default state directory $HOME/.wb as a
// read-only layout. It is reported only when that directory actually holds a
// worktrees root: WB must stay able to validate and operate on checkouts left
// there by earlier releases, and an empty-but-present directory must not become
// an implicit configuration bit that changes every command's read set.
func legacyUserLayout() (Layout, bool, error) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return Layout{}, false, nil
	}
	home, err := resolveAbs(filepath.Join(userHome, ".wb"))
	if err != nil {
		return Layout{}, false, err
	}
	if !hasWorktrees(home) {
		return Layout{}, false, nil
	}
	return Layout{Home: home, WorktreesRoot: filepath.Join(home, "worktrees"), Legacy: true}, true, nil
}

// StoreRoot returns the central checkout store for a projects root:
// <root>/.worktrees. It is the write layout's physical checkout root — the
// same value as Resolve(root).Write.WorktreesRoot — and the path a later
// store-mode implementation places task checkouts under. It does not create
// anything.
func StoreRoot(projectsRoot string) (string, error) {
	resolution, err := Resolve(projectsRoot)
	if err != nil {
		return "", err
	}
	return resolution.Write.WorktreesRoot, nil
}

// Root resolves WB's authoritative write home: <root>/.wb. It remains for
// callers that only create state.
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

Learn more about the WB CLI at https://sneat.work/bench.
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

// projectsRootAbs resolves the projects root with the AC's precedence: the
// caller-supplied value (the --projects-root flag, already parsed by the CLI)
// wins, then WB_PROJECTS_ROOT, then the default root ~/projects.
func projectsRootAbs(projectsRoot string) (string, error) {
	root := strings.TrimSpace(projectsRoot)
	if root == "" {
		root = strings.TrimSpace(os.Getenv(EnvOverride))
	}
	if root == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home directory: %w", err)
		}
		root = filepath.Join(userHome, "projects")
	}
	return resolveAbs(root)
}

func newLayout(root string) Layout {
	return Layout{Home: filepath.Join(root, ".wb"), WorktreesRoot: filepath.Join(root, ".worktrees")}
}

// hasWorktrees reports whether a home directory carries a worktrees root. It is
// the marker that a retired home actually holds checkouts worth reading.
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
