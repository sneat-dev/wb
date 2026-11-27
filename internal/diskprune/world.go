package diskprune

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/disk"
	"github.com/sneat-dev/wb/internal/runqueue"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// admissionWait bounds how long an admitted prune waits behind heavy jobs.
var admissionWait = 60 * time.Second

// worldDeps are the inventories NewWorld reads, as seams so the protected
// inventory's failure paths are testable.
type worldDeps struct {
	userHome    func() (string, error)
	resolve     func(string) (wbhome.Resolution, error)
	readDir     func(string) ([]os.DirEntry, error)
	git         func(ctx context.Context, dir string, args ...string) (string, error)
	goEnv       func() (GoEnv, error)
	tempDir     func() string
	getenv      func(string) string
	admitPrune  func(ctx context.Context, projectsRoot string) (status string, wait time.Duration, release func())
	readProcess func() Table
}

func realWorldDeps() worldDeps {
	return worldDeps{
		userHome: os.UserHomeDir, resolve: wbhome.Resolve, readDir: os.ReadDir,
		git: runGit, goEnv: ResolveGoEnv, tempDir: os.TempDir, getenv: os.Getenv,
		admitPrune: admitPrune, readProcess: ReadProcessTable,
	}
}

// NewWorld assembles the real machine for one projects root.
func NewWorld(ctx context.Context, projectsRoot string) (*World, error) {
	return newWorld(ctx, projectsRoot, realWorldDeps())
}

func newWorld(ctx context.Context, projectsRoot string, deps worldDeps) (*World, error) {
	home, err := deps.userHome()
	if err != nil {
		return nil, err
	}
	// Every root is compared with resolved protected roots and locked with
	// O_NOFOLLOW, so each is resolved once here.
	home = resolved(home)
	resolution, err := deps.resolve(projectsRoot)
	if err != nil {
		return nil, err
	}
	env, err := deps.goEnv()
	if err != nil {
		return nil, err
	}
	env.GOCACHE, env.GOMODCACHE = resolvedRoot(env.GOCACHE), resolvedRoot(env.GOMODCACHE)
	w := &World{
		Now: time.Now, UID: os.Getuid(), Home: home, ProjectsRoot: resolution.Root,
		StateDir: resolution.Write.Home, Go: env,
		Processes: deps.readProcess, Probe: disk.Probe, Identity: StatIdentity, Scan: scanTree,
		LockDirs: LockDirs, RemoveTree: RemoveTree, RemoveFile: RemoveFileBeneath,
		Admit: func(ctx context.Context) (string, time.Duration, func()) {
			return deps.admitPrune(ctx, resolution.Root)
		},
		categories: []category{scratchCategory{}, buildCacheCategory{}, fullCleanCategory{}},
	}
	if legacy := filepath.Join(home, ".wb"); filepath.Clean(legacy) != filepath.Clean(w.StateDir) {
		if info, err := os.Stat(legacy); err == nil && info.IsDir() {
			w.LegacyHome = legacy
		}
	}
	w.TempDirs, w.TempAliases = tempDirs(deps)
	w.Protected, w.ProtectedGap = protectedInventory(ctx, w, resolution, deps)
	w.ProtectedIDs = protectedIDs(w.Protected)
	w.Scan = scanTreeWithIDs(w.ProtectedIDs, func() bool { return w.runCancelled() })
	w.BindOfSubdirectory = bindOfSubdirectory
	return w, nil
}

// tempDirs are the temporary directories scratch looks in, resolved and
// deduplicated.
func tempDirs(deps worldDeps) ([]string, map[string][]string) {
	var out []string
	aliases := map[string][]string{}
	seen := map[string]bool{}
	for _, original := range []string{deps.tempDir(), deps.getenv("GOTMPDIR")} {
		if original == "" {
			continue
		}
		dir := resolved(original)
		// Every spelling is kept: TMPDIR and GOTMPDIR may reach one
		// directory through different symlinks.
		if spelling := filepath.Clean(original); spelling != dir && !slices.Contains(aliases[dir], spelling) {
			aliases[dir] = append(aliases[dir], spelling)
		}
		if !seen[dir] {
			seen[dir] = true
			out = append(out, dir)
		}
	}
	return out, aliases
}

// resolvedRoot resolves a configured absolute root; a relative or special
// value (GOCACHE=off) is left for the category to refuse.
func resolvedRoot(path string) string {
	if !filepath.IsAbs(path) {
		return path
	}
	return resolved(path)
}

// protectedInventory lists every root no category may delete inside or
// around: state, the worktree stores, every canonical clone and every
// worktree a clone registers (by path, whether or not it can be read). A gap
// in the inventory is reported: every category then fails closed.
func protectedInventory(ctx context.Context, w *World, resolution wbhome.Resolution, deps worldDeps) (protected []string, gap string) {
	add := func(paths ...string) {
		for _, path := range paths {
			if path != "" {
				protected = append(protected, resolved(path))
			}
		}
	}
	noteGap := func(reason string) {
		if gap == "" {
			gap = reason
		}
	}
	add(w.StateDir, w.LegacyHome, resolution.Write.WorktreesRoot)
	stores := []string{resolution.Write.WorktreesRoot}
	for _, layout := range resolution.Read {
		add(layout.WorktreesRoot)
		stores = append(stores, layout.WorktreesRoot)
	}
	clones, err := findCheckouts(deps.readDir, resolution.Root, true)
	if err != nil {
		noteGap("canonical clones could not be listed: " + err.Error())
	}
	for _, clone := range clones {
		add(clone, filepath.Join(clone, ".worktrees"))
		stores = append(stores, filepath.Join(clone, ".worktrees"))
		out, err := deps.git(ctx, clone, "worktree", "list", "--porcelain", "-z")
		if err != nil {
			noteGap("git worktree list failed in " + clone + ": " + err.Error())
			continue
		}
		worktrees, err := parseWorktreeList(out)
		if err == nil && !listsClone(worktrees, clone) {
			// Git always lists the repository itself first; output without
			// it is incomplete, and registrations may be missing.
			err = errors.New("the clone itself is not listed")
		}
		if err != nil {
			noteGap("git worktree list in " + clone + ": " + err.Error())
			continue
		}
		add(worktrees...)
	}
	// A checkout a worktrees root holds is protected even when no listed
	// clone registers it: its canonical clone may live elsewhere, or its
	// registration may be broken.
	for _, store := range stores {
		held, err := findCheckouts(deps.readDir, store, false)
		if err != nil {
			noteGap("worktrees root " + store + " could not be listed: " + err.Error())
		}
		add(held...)
	}
	return protected, gap
}

// listsClone reports whether git's list names the clone it was run in.
func listsClone(worktrees []string, clone string) bool {
	for _, worktree := range worktrees {
		if resolved(worktree) == resolved(clone) {
			return true
		}
	}
	return false
}

// findCheckouts lists directories holding .git at any depth below root,
// without descending into a checkout, and walks each directory once even
// through a symlink loop. Any unreadable directory is an error: a silently
// skipped clone would not be protected. skipDot leaves out WB's own
// directories at root (.wb, .worktrees), which are not clone placements;
// every other dot-directory may hold a clone and is walked.
func findCheckouts(readDir func(string) ([]os.DirEntry, error), root string, skipDot bool) ([]string, error) {
	var found []string
	visited := map[string]bool{}
	var walk func(dir string, level int) error
	walk = func(dir string, level int) error {
		if key, ok := dirKey(dir); ok {
			if visited[key] {
				return nil
			}
			visited[key] = true
		}
		entries, err := readDir(dir)
		if errors.Is(err, os.ErrNotExist) && dir == root {
			return nil
		}
		if errors.Is(err, os.ErrPermission) && dir != root && !ownedByUserPath(dir) && !traversable(dir) {
			// Another user's directory this user can neither list nor enter
			// (a root-owned lost+found) cannot hold this user's checkout.
			return nil
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() == ".git" {
				found = append(found, dir)
				return nil
			}
		}
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			isDir := entry.IsDir()
			if entry.Type()&os.ModeSymlink != 0 {
				// A symlinked clone or org directory is a placement like any
				// other; one that cannot be followed is an inventory gap.
				info, err := os.Stat(path)
				if err != nil {
					return fmt.Errorf("%s: %w", path, err)
				}
				isDir = info.IsDir()
			}
			if !isDir || (skipDot && level == 0 && (entry.Name() == ".wb" || entry.Name() == ".worktrees")) || entry.Name() == "node_modules" {
				continue
			}
			if err := walk(path, level+1); err != nil {
				return err
			}
		}
		return nil
	}
	err := walk(root, 0)
	return found, err
}

// parseWorktreeList reads `git worktree list --porcelain`.
// parseWorktreeList reads `git worktree list --porcelain -z`: NUL-terminated
// fields, so a path holding a newline is read whole. Output that is not
// NUL-terminated is refused rather than guessed at.
func parseWorktreeList(out string) ([]string, error) {
	if out == "" {
		return nil, nil
	}
	if !strings.HasSuffix(out, "\x00") {
		return nil, errors.New("output is not NUL-terminated")
	}
	var paths []string
	for _, field := range strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") {
		if path, ok := strings.CutPrefix(field, "worktree "); ok {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// A hook context's GIT_DIR or GIT_WORK_TREE would make every clone list
	// the wrong repository without an error.
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "GIT_") {
			cmd.Env = append(cmd.Env, variable)
		}
	}
	out, err := cmd.Output()
	return string(out), err
}

// admitPrune waits at most admissionWait for one unit of WB's admission
// queue. A wait that times out proceeds: admission is courtesy to WB's own
// heavy jobs, not the safety check.
func admitPrune(ctx context.Context, projectsRoot string) (string, time.Duration, func()) {
	bounded, cancel := context.WithTimeout(ctx, admissionWait)
	defer cancel()
	started := time.Now()
	admission, err := runqueue.AdmitExplicit(bounded, projectsRoot, 1, runqueue.Participant{PID: os.Getpid(), Summary: "wb disk prune"})
	if err != nil {
		return "timed-out", time.Since(started), func() {}
	}
	return "admitted", admission.Waited, admission.Lease.Release
}

// protectedIDs are the device and inode of every protected root that exists.
func protectedIDs(roots []string) map[[2]uint64]string {
	ids := map[[2]uint64]string{}
	for _, root := range roots {
		if identity, err := StatIdentity(root); err == nil && identity.IsDir {
			ids[[2]uint64{identity.Dev, identity.Ino}] = root
		}
	}
	return ids
}

// resolved is path with symlinks resolved when it exists, so it compares
// against the resolved protected roots.
func resolved(path string) string {
	if path == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}
