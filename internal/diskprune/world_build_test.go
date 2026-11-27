package diskprune

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runqueue"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func fakeWorldDeps(t *testing.T) (worldDeps, string, string) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "projects")
	clone := filepath.Join(root, "github.com", "acme", "app")
	external := filepath.Join(t.TempDir(), "elsewhere")
	for _, dir := range []string{filepath.Join(clone, ".git"), external, filepath.Join(home, ".wb")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	deps := worldDeps{
		userHome: func() (string, error) { return home, nil },
		resolve: func(string) (wbhome.Resolution, error) {
			write := wbhome.Layout{Home: filepath.Join(root, ".wb"), WorktreesRoot: filepath.Join(root, ".worktrees")}
			return wbhome.Resolution{Root: root, Write: write, Read: []wbhome.Layout{write, {Home: filepath.Join(home, ".wb"), WorktreesRoot: filepath.Join(home, ".wb", "worktrees"), Legacy: true}}}, nil
		},
		readDir: os.ReadDir,
		git: func(ctx context.Context, dir string, args ...string) (string, error) {
			return "worktree " + clone + "\x00HEAD abc\x00\x00worktree " + external + "\x00branch refs/heads/x\x00\x00worktree /gone/away\x00\x00", nil
		},
		goEnv:       func() (GoEnv, error) { return GoEnv{GOCACHE: "/c", GOMODCACHE: "/m"}, nil },
		tempDir:     func() string { return home },
		getenv:      func(key string) string { return map[string]string{"GOTMPDIR": filepath.Join(home, ".")}[key] },
		admitPrune:  func(context.Context, string) (string, time.Duration, func()) { return "admitted", 0, func() {} },
		readProcess: func() Table { return Table{} },
	}
	return deps, clone, external
}

// The real world protects state, stores, clones and every registered
// worktree.
func TestNewWorldInventory(t *testing.T) {
	t.Parallel()
	deps, clone, external := fakeWorldDeps(t)
	w, err := newWorld(context.Background(), "/ignored", deps)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(w.Protected, " ")
	for _, want := range []string{w.StateDir, w.LegacyHome, clone, external, "/gone/away", filepath.Join(clone, ".worktrees")} {
		if !strings.Contains(joined, want) {
			t.Errorf("protected lacks %s: %v", want, w.Protected)
		}
	}
	if w.ProtectedGap != "" || len(w.TempDirs) != 1 || w.LegacyHome == "" {
		t.Fatalf("world = gap %q temp %v legacy %q", w.ProtectedGap, w.TempDirs, w.LegacyHome)
	}
	if status, _, release := w.Admit(context.Background()); status != "admitted" {
		t.Fatal("admission not wired")
	} else {
		release()
	}
	if len(w.categories) != 3 {
		t.Fatalf("categories = %d", len(w.categories))
	}
}

// Every inventory failure is an error or a reported gap, never a silently
// smaller protected set.
func TestNewWorldFailures(t *testing.T) {
	t.Parallel()
	failure := errors.New("injected")
	for name, mutate := range map[string]func(*worldDeps){
		"home": func(d *worldDeps) { d.userHome = func() (string, error) { return "", failure } },
		"resolve": func(d *worldDeps) {
			d.resolve = func(string) (wbhome.Resolution, error) { return wbhome.Resolution{}, failure }
		},
		"go env": func(d *worldDeps) { d.goEnv = func() (GoEnv, error) { return GoEnv{}, failure } },
	} {
		deps, _, _ := fakeWorldDeps(t)
		mutate(&deps)
		if _, err := newWorld(context.Background(), "/r", deps); !errors.Is(err, failure) {
			t.Errorf("%s: %v", name, err)
		}
	}
	deps, _, _ := fakeWorldDeps(t)
	deps.readDir = func(string) ([]os.DirEntry, error) { return nil, failure }
	if w, _ := newWorld(context.Background(), "/r", deps); !strings.Contains(w.ProtectedGap, "canonical clones") {
		t.Fatalf("scan gap = %q", w.ProtectedGap)
	}
	deps, _, _ = fakeWorldDeps(t)
	deps.git = func(context.Context, string, ...string) (string, error) { return "", failure }
	if w, _ := newWorld(context.Background(), "/r", deps); !strings.Contains(w.ProtectedGap, "git worktree list") {
		t.Fatalf("git gap = %q", w.ProtectedGap)
	}
	deps, _, _ = fakeWorldDeps(t)
	deps.tempDir = func() string { return "" }
	deps.getenv = func(string) string { return "/nonexistent-tmp" }
	deps.userHome = func() (string, error) { return filepath.Join(t.TempDir(), "no-legacy"), nil }
	if w, _ := newWorld(context.Background(), "/r", deps); len(w.TempDirs) != 1 || w.TempDirs[0] != "/nonexistent-tmp" || w.LegacyHome != "" {
		t.Fatalf("temp dirs %v legacy %q", w.TempDirs, w.LegacyHome)
	}
	if NewWorldDepsAreReal := realWorldDeps(); NewWorldDepsAreReal.git == nil {
		t.Fatal("real deps incomplete")
	}
}

// The real helpers: git, HTTP probes and admission.
//
//nolint:paralleltest // shortens the package's admission bound
func TestRealWorldHelpers(t *testing.T) {
	repo := t.TempDir()
	if err := exec.Command("git", "init", "-q", repo).Run(); err != nil {
		t.Skip("git unavailable")
	}
	out, err := runGit(context.Background(), repo, "worktree", "list", "--porcelain", "-z")
	if paths, perr := parseWorktreeList(out); err != nil || perr != nil || len(paths) != 1 {
		t.Fatalf("git worktree list = %q, %v", out, err)
	}

	root := t.TempDir()
	admitted, _, release := admitPrune(context.Background(), root)
	if admitted != "admitted" {
		t.Fatalf("admission = %q", admitted)
	}
	release()
	full, err := runqueue.AdmitExplicit(context.Background(), root, runqueue.Budget(), runqueue.Participant{PID: os.Getpid(), Summary: "holds every unit"})
	if err != nil {
		t.Fatal(err)
	}
	defer full.Lease.Release()
	realWait := admissionWait
	admissionWait = 50 * time.Millisecond
	t.Cleanup(func() { admissionWait = realWait })
	if status, wait, release := admitPrune(context.Background(), root); status != "timed-out" || wait < admissionWait {
		t.Fatalf("full queue admission = %q after %v", status, wait)
	} else {
		release()
	}
	if _, err := NewWorld(context.Background(), root); err != nil {
		t.Fatalf("real world: %v", err)
	}
}
