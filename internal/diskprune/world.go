package diskprune

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	runPnpm     func(ctx context.Context, args ...string) (string, error)
	fetchURL    func(ctx context.Context, method, url string) (int, []byte, error)
	readProcess func() Table
}

func realWorldDeps() worldDeps {
	return worldDeps{
		userHome: os.UserHomeDir, resolve: wbhome.Resolve, readDir: os.ReadDir,
		git: runGit, goEnv: ResolveGoEnv, tempDir: os.TempDir, getenv: os.Getenv,
		admitPrune: admitPrune, runPnpm: runPnpm, fetchURL: fetchURL, readProcess: ReadProcessTable,
	}
}

// NewWorld assembles the real machine for one projects root.
func NewWorld(ctx context.Context, projectsRoot string, ignoreSumFiles []string) (*World, error) {
	return newWorld(ctx, projectsRoot, ignoreSumFiles, realWorldDeps())
}

func newWorld(ctx context.Context, projectsRoot string, ignoreSumFiles []string, deps worldDeps) (*World, error) {
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
		StateDir: resolution.Write.Home, Go: env, IgnoreSumFiles: ignoreSumFiles,
		Processes: deps.readProcess, Probe: disk.Probe, Identity: StatIdentity, Scan: scanTree,
		LockDirs: LockDirs, RemoveTree: RemoveTree, RemoveFile: RemoveFileAt, LockModuleVersion: LockModuleVersion,
		PnpmStores: []string{filepath.Join(home, ".local", "share", "pnpm", "store"), filepath.Join(home, "Library", "pnpm", "store")},
		Pnpm:       deps.runPnpm, HTTP: deps.fetchURL, LookupHost: net.DefaultResolver.LookupHost,
		Admit: func(ctx context.Context) (string, time.Duration, func()) {
			return deps.admitPrune(ctx, resolution.Root)
		},
		categories: []category{scratchCategory{}, buildCacheCategory{}, pnpmCategory{}, moduleCategory{}, fullCleanCategory{}},
	}
	if legacy := filepath.Join(home, ".wb"); filepath.Clean(legacy) != filepath.Clean(w.StateDir) {
		if info, err := os.Stat(legacy); err == nil && info.IsDir() {
			w.LegacyHome = legacy
		}
	}
	w.TempDirs, w.TempAliases = tempDirs(deps)
	w.Protected, w.Checkouts, w.ProtectedGap = protectedInventory(ctx, w, resolution, deps)
	return w, nil
}

// tempDirs are the temporary directories scratch looks in, resolved and
// deduplicated.
func tempDirs(deps worldDeps) ([]string, map[string]string) {
	var out []string
	aliases := map[string]string{}
	seen := map[string]bool{}
	for _, original := range []string{deps.tempDir(), deps.getenv("GOTMPDIR")} {
		if original == "" {
			continue
		}
		dir := resolved(original)
		if dir != filepath.Clean(original) {
			aliases[dir] = filepath.Clean(original)
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
// around, and the checkouts whose sum files form the module reference set.
// A gap in the inventory is reported: every category that deletes
// directories then fails closed.
func protectedInventory(ctx context.Context, w *World, resolution wbhome.Resolution, deps worldDeps) (protected, checkouts []string, gap string) {
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
	clones, err := findCheckouts(deps.readDir, resolution.Root, 3, true)
	if err != nil {
		noteGap("canonical clones could not be listed: " + err.Error())
	}
	seen := map[string]bool{}
	addCheckout := func(path string) {
		path = resolved(path)
		if !seen[path] {
			seen[path] = true
			checkouts = append(checkouts, path)
		}
	}
	for _, clone := range clones {
		add(clone, filepath.Join(clone, ".worktrees"))
		addCheckout(clone)
		stores = append(stores, filepath.Join(clone, ".worktrees"))
		out, err := deps.git(ctx, clone, "worktree", "list", "--porcelain")
		if err != nil {
			noteGap("git worktree list failed in " + clone + ": " + err.Error())
			continue
		}
		for _, worktree := range parseWorktreeList(out) {
			add(worktree)
			if info, err := os.Stat(worktree); err == nil && info.IsDir() {
				addCheckout(worktree)
			}
		}
	}
	// A checkout a worktrees root holds counts even when no listed clone
	// registers it: its canonical clone may live elsewhere, or its
	// registration may be broken.
	for _, store := range stores {
		held, err := findCheckouts(deps.readDir, store, 5, false)
		if err != nil {
			noteGap("worktrees root " + store + " could not be listed: " + err.Error())
		}
		for _, checkout := range held {
			add(checkout)
			addCheckout(checkout)
		}
	}
	return protected, checkouts, gap
}

// findCheckouts lists directories holding .git, at most depth levels below
// root, without descending into a checkout. Any unreadable directory is an
// error: a silently skipped clone would be neither protected nor read for
// references. skipDot leaves out root's dot-directories (.wb, .worktrees),
// which are not clone placements.
func findCheckouts(readDir func(string) ([]os.DirEntry, error), root string, depth int, skipDot bool) ([]string, error) {
	var found []string
	var walk func(dir string, level int) error
	walk = func(dir string, level int) error {
		entries, err := readDir(dir)
		if errors.Is(err, os.ErrNotExist) && dir == root {
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
		if level == depth {
			return nil
		}
		for _, entry := range entries {
			if !entry.IsDir() || (skipDot && level == 0 && strings.HasPrefix(entry.Name(), ".")) || entry.Name() == "node_modules" {
				continue
			}
			if err := walk(filepath.Join(dir, entry.Name()), level+1); err != nil {
				return err
			}
		}
		return nil
	}
	err := walk(root, 0)
	return found, err
}

// parseWorktreeList reads `git worktree list --porcelain`.
func parseWorktreeList(out string) []string {
	var paths []string
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		if path, ok := strings.CutPrefix(scanner.Text(), "worktree "); ok {
			paths = append(paths, path)
		}
	}
	return paths
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

// runPnpm runs pnpm from / under pnpmEnv, so corepack can neither download
// nor prompt.
func runPnpm(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "pnpm", args...)
	cmd.Dir = "/"
	cmd.Env = append(os.Environ(), pnpmEnv...)
	out, err := cmd.Output()
	return string(out), err
}

var probeClient = &http.Client{Timeout: 10 * time.Second}

// fetchURL performs one re-fetch probe, reading at most 1 MiB of body.
func fetchURL(ctx context.Context, method, url string) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return 0, nil, err
	}
	response, err := probeClient.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return response.StatusCode, body, err
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
