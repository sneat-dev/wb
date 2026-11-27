package diskprune

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
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
	scanLocal   func(string) ([]discover.Repo, error)
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
		userHome: os.UserHomeDir, resolve: wbhome.Resolve, scanLocal: discover.ScanLocal,
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
	resolution, err := deps.resolve(projectsRoot)
	if err != nil {
		return nil, err
	}
	env, err := deps.goEnv()
	if err != nil {
		return nil, err
	}
	w := &World{
		Now: time.Now, UID: os.Getuid(), Home: home, ProjectsRoot: resolution.Root,
		StateDir: resolution.Write.Home, Go: env, IgnoreSumFiles: ignoreSumFiles,
		Processes: deps.readProcess, Probe: disk.Probe, Identity: StatIdentity, Scan: scanTree,
		LockDirs: LockDirs, RemoveTree: RemoveTree, RemoveFile: RemoveFileAt, LockModuleVersion: LockModuleVersion,
		PnpmStores: []string{filepath.Join(home, ".local", "share", "pnpm", "store"), filepath.Join(home, "Library", "pnpm", "store")},
		Pnpm:       deps.runPnpm, HTTP: deps.fetchURL,
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
	w.TempDirs = tempDirs(deps)
	w.Protected, w.Checkouts, w.ProtectedGap = protectedInventory(ctx, w, resolution, deps)
	return w, nil
}

// tempDirs are the temporary directories scratch looks in, resolved and
// deduplicated.
func tempDirs(deps worldDeps) []string {
	var out []string
	seen := map[string]bool{}
	for _, dir := range []string{deps.tempDir(), deps.getenv("GOTMPDIR")} {
		if dir == "" {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			dir = resolved
		}
		if !seen[dir] {
			seen[dir] = true
			out = append(out, dir)
		}
	}
	return out
}

// protectedInventory lists every root no category may delete inside or
// around, and the checkouts whose sum files form the module reference set.
// A gap in the inventory is reported: every category that deletes
// directories then fails closed.
func protectedInventory(ctx context.Context, w *World, resolution wbhome.Resolution, deps worldDeps) (protected, checkouts []string, gap string) {
	add := func(paths ...string) {
		for _, path := range paths {
			if path == "" {
				continue
			}
			if resolved, err := filepath.EvalSymlinks(path); err == nil {
				path = resolved
			}
			protected = append(protected, filepath.Clean(path))
		}
	}
	add(w.StateDir, w.LegacyHome, resolution.Write.WorktreesRoot)
	for _, layout := range resolution.Read {
		add(layout.WorktreesRoot)
	}
	repos, err := deps.scanLocal(resolution.Root)
	if err != nil {
		return protected, checkouts, "canonical clones could not be listed: " + err.Error()
	}
	for _, repo := range repos {
		if repo.Path == "" {
			continue
		}
		add(repo.Path, filepath.Join(repo.Path, ".worktrees"))
		checkouts = append(checkouts, repo.Path)
		out, err := deps.git(ctx, repo.Path, "worktree", "list", "--porcelain")
		if err != nil {
			gap = "git worktree list failed in " + repo.Path + ": " + err.Error()
			continue
		}
		for _, worktree := range parseWorktreeList(out) {
			if filepath.Clean(worktree) == filepath.Clean(repo.Path) {
				continue
			}
			add(worktree)
			if info, err := os.Stat(worktree); err == nil && info.IsDir() {
				checkouts = append(checkouts, worktree)
			}
		}
	}
	return protected, checkouts, gap
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
	defer response.Body.Close()
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
