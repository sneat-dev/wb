//go:build linux

package diskprune

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/goenv"
)

// fixture is a temporary directory, a clock four days ahead, and a fake Go
// toolchain.
type fixture struct {
	t        *testing.T
	tmp      string
	env      Env
	cleaned  int
	cleanErr error
	running  []Process
	procErr  error
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, tmp: t.TempDir()}
	cache := t.TempDir()
	mustWrite(t, filepath.Join(cache, "0a", "entry-a"), "x")
	f.env = Env{
		TempDirs: []string{f.tmp}, UID: os.Getuid(),
		Now:     func() time.Time { return time.Now().Add(96 * time.Hour) },
		GoCache: cache,
		GoProcesses: func() ([]Process, error) {
			return f.running, f.procErr
		},
		GoClean: func(context.Context) error {
			f.cleaned++
			if f.cleanErr == nil {
				_ = os.RemoveAll(filepath.Join(cache, "0a"))
			}
			return f.cleanErr
		},
		Measure:   measure,
		RemoveAll: removeAll,
	}
	return f
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) scratch(name string) string {
	f.t.Helper()
	path := filepath.Join(f.tmp, name)
	mustWrite(f.t, filepath.Join(path, "001", "data"), strings.Repeat("x", 4096))
	return path
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func category(t *testing.T, report Report, name string) CategoryReport {
	t.Helper()
	for _, c := range report.Categories {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no category %s in %#v", name, report)
	return CategoryReport{}
}

// A plan changes nothing and lists the known producers' old directories;
// apply removes exactly those.
func TestScratchPlanThenApplyRemovesOnlyKnownOldProducers(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	removable := []string{f.scratch("go-build123"), f.scratch("TestFoo4567"), f.scratch("wb-index-77"), f.scratch("wb-pr308-lint-go-cache")}
	unknown := f.scratch("my-notes")
	plan := Run(context.Background(), f.env, Options{Categories: []string{CategoryScratch}})
	scratch := category(t, plan, CategoryScratch)
	if plan.Mode != "plan" || plan.Outcome != OutcomePlanned || scratch.Status != StatusDelete || len(scratch.Items) != 4 || scratch.Bytes <= 0 {
		t.Fatalf("plan = %#v", plan)
	}
	for _, path := range append(removable, unknown) {
		if !exists(path) {
			t.Fatalf("the plan removed %s", path)
		}
	}
	applied := Run(context.Background(), f.env, Options{Apply: true, Categories: []string{CategoryScratch}})
	if applied.Outcome != OutcomeDone || category(t, applied, CategoryScratch).Status != StatusDeleted {
		t.Fatalf("apply = %#v", applied)
	}
	for _, path := range removable {
		if exists(path) {
			t.Fatalf("%s survived", path)
		}
	}
	if !exists(unknown) {
		t.Fatal("a directory of no known producer was removed")
	}
}

// Kept: a recently changed tree, a symlink, a plain file, a checkout, a
// socket, another user's or another filesystem's directory.
func TestScratchKeepsWhatIsNotSafelyOld(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.env.Now = time.Now
	young := f.scratch("go-build1")
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(f.tmp, "go-build2")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(f.tmp, "go-build3"), "a file")
	report := Run(context.Background(), f.env, Options{Apply: true, Categories: []string{CategoryScratch}})
	kept := category(t, report, CategoryScratch).Kept
	if kept["changed within 72h"] != 1 || kept["not a directory"] != 2 || !exists(young) || !exists(target) {
		t.Fatalf("kept = %v", kept)
	}

	g := newFixture(t)
	checkout := g.scratch("go-build4")
	mustWrite(t, filepath.Join(checkout, ".git", "HEAD"), "ref")
	socketDir := g.scratch("go-build5")
	listener, err := net.Listen("unix", filepath.Join(socketDir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	g.env.UID = os.Getuid() + 1
	foreign := Run(context.Background(), g.env, Options{Categories: []string{CategoryScratch}})
	if category(t, foreign, CategoryScratch).Kept["owned by another user"] != 2 {
		t.Fatalf("foreign = %#v", foreign)
	}
	g.env.UID = os.Getuid()
	report = Run(context.Background(), g.env, Options{Apply: true, Categories: []string{CategoryScratch}})
	kept = category(t, report, CategoryScratch).Kept
	if kept["contains a checkout"] != 1 || kept["in use (socket or FIFO)"] != 1 || !exists(checkout) || !exists(socketDir) {
		t.Fatalf("kept = %v", kept)
	}
	if report.Outcome != OutcomeDone {
		t.Fatalf("outcome = %q", report.Outcome)
	}
}

// --older-than raises the quiet time scratch needs.
func TestScratchAgeCanBeRaised(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	old := f.scratch("go-build1")
	report := Run(context.Background(), f.env, Options{Apply: true, ScratchAge: 10 * 24 * time.Hour})
	if !exists(old) || category(t, report, CategoryScratch).Kept["changed within 240h"] != 1 {
		t.Fatalf("report = %#v", report)
	}
}

// A mount inside a candidate, or a candidate on another filesystem than its
// temporary directory, is kept; an unreadable directory is kept too.
func TestScratchKeepsMountsAndUnreadableTrees(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	path := f.scratch("go-build1")
	rootInfo, _ := os.Stat(f.tmp)
	if keep := scratchKeepReasonWith(f.env, Options{ScratchAge: DefaultScratchAge}, rootInfo, path, func(p string, info os.FileInfo) uint64 {
		if strings.HasSuffix(p, "001") {
			return 99
		}
		return deviceOf(info)
	}); keep != "contains a mount" {
		t.Fatalf("a nested mount = %q", keep)
	}
	if keep := scratchKeepReasonWith(f.env, Options{ScratchAge: DefaultScratchAge}, rootInfo, path, func(p string, info os.FileInfo) uint64 {
		if p == path {
			return 99
		}
		return deviceOf(info)
	}); keep != "another filesystem" {
		t.Fatalf("another filesystem = %q", keep)
	}
	if keep := scratchKeepReason(f.env, Options{ScratchAge: DefaultScratchAge}, rootInfo, filepath.Join(f.tmp, "absent")); keep != "unreadable" {
		t.Fatalf("absent = %q", keep)
	}
	if os.Geteuid() != 0 {
		locked := f.scratch("go-build2")
		if err := os.Chmod(filepath.Join(locked, "001"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(locked, "001"), 0o755) })
		if keep := scratchKeepReason(f.env, Options{ScratchAge: DefaultScratchAge}, rootInfo, locked); keep != "unreadable" {
			t.Fatalf("an unreadable tree = %q", keep)
		}
	}
}

// A temporary directory that cannot be read is reported, a cancelled run
// stops, and a removal that fails is a failure.
func TestScratchFailures(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.env.TempDirs = []string{filepath.Join(f.tmp, "absent")}
	if kept := category(t, Run(context.Background(), f.env, Options{Categories: []string{CategoryScratch}}), CategoryScratch).Kept; kept["temporary directory unreadable"] != 1 {
		t.Fatalf("kept = %v", kept)
	}
	file := filepath.Join(f.tmp, "file")
	mustWrite(t, file, "x")
	f.env.TempDirs = []string{file}
	if kept := category(t, Run(context.Background(), f.env, Options{Categories: []string{CategoryScratch}}), CategoryScratch).Kept; kept["temporary directory unreadable"] != 1 {
		t.Fatalf("a file as the temporary directory: %v", kept)
	}

	g := newFixture(t)
	g.scratch("go-build1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if items := category(t, Run(ctx, g.env, Options{Apply: true}), CategoryScratch).Items; len(items) != 0 {
		t.Fatalf("a cancelled run acted on %v", items)
	}
	g.env.RemoveAll = func(string) error { return errors.New("EIO") }
	report := Run(context.Background(), g.env, Options{Apply: true, Categories: []string{CategoryScratch}})
	if report.Outcome != OutcomeFailed || category(t, report, CategoryScratch).Items[0].Error != "EIO" {
		t.Fatalf("a failed removal = %#v", report)
	}
}

// A tree Go wrote read-only is still removed.
func TestRemoveAllHandlesReadOnlyTrees(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "wb-x-go-cache")
	mustWrite(t, filepath.Join(dir, "mod", "example.com", "a@v1", "go.mod"), "module a")
	if err := os.Chmod(filepath.Join(dir, "mod", "example.com", "a@v1"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := removeAll(dir); err != nil || exists(dir) {
		t.Fatalf("read-only tree: %v", err)
	}
	if err := removeAll(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Fatalf("an absent tree: %v", err)
	}
}

// The build cache is cleared only while no Go process runs; a plan never
// clears it.
func TestBuildCacheClearedOnlyWhileGoIsIdle(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	plan := Run(context.Background(), f.env, Options{Categories: []string{CategoryBuild}})
	if c := category(t, plan, CategoryBuild); c.Status != StatusDelete || c.Bytes <= 0 || f.cleaned != 0 || plan.Outcome != OutcomePlanned {
		t.Fatalf("plan = %#v", plan)
	}
	f.running = []Process{{PID: 7, Command: "go"}}
	busy := Run(context.Background(), f.env, Options{Apply: true, Categories: []string{CategoryBuild}})
	if c := category(t, busy, CategoryBuild); c.Status != StatusSkipped || !strings.Contains(c.Reason, "7 go") || busy.Outcome != OutcomeSkipped || f.cleaned != 0 {
		t.Fatalf("busy = %#v", busy)
	}
	f.running, f.procErr = nil, errors.New("no /proc")
	if c := category(t, Run(context.Background(), f.env, Options{Apply: true}), CategoryBuild); !strings.HasPrefix(c.Reason, "cannot tell") || f.cleaned != 0 {
		t.Fatalf("unknown = %#v", c)
	}
	f.procErr = nil
	applied := Run(context.Background(), f.env, Options{Apply: true, Categories: []string{CategoryBuild}})
	if c := category(t, applied, CategoryBuild); c.Status != StatusDeleted || c.Bytes <= 0 || f.cleaned != 1 || applied.Outcome != OutcomeDone {
		t.Fatalf("applied = %#v", applied)
	}
}

// A Go process that starts between the plan check and the clear stops it;
// a failing go clean is a failure; an unsupported cache is not a finding.
func TestBuildCacheRecheckFailureAndUnsupported(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	calls := 0
	f.env.GoProcesses = func() ([]Process, error) {
		calls++
		if calls == 2 {
			return []Process{{PID: 8, Command: "gopls"}}, nil
		}
		return nil, nil
	}
	late := Run(context.Background(), f.env, Options{Apply: true, Categories: []string{CategoryBuild}})
	if c := category(t, late, CategoryBuild); c.Status != StatusSkipped || len(c.Items) != 0 || f.cleaned != 0 {
		t.Fatalf("late = %#v", late)
	}
	f.env.GoProcesses = func() ([]Process, error) { return nil, nil }
	f.cleanErr = errors.New("go: not found")
	failed := Run(context.Background(), f.env, Options{Apply: true, Categories: []string{CategoryBuild}})
	if c := category(t, failed, CategoryBuild); c.Status != StatusFailed || c.Items[0].Error != "go: not found" || failed.Outcome != OutcomeFailed {
		t.Fatalf("failed = %#v", failed)
	}
	f.env.GoCacheProblem = "GOCACHEPROG is set"
	unsupported := Run(context.Background(), f.env, Options{Apply: true, Categories: []string{CategoryBuild}})
	if c := category(t, unsupported, CategoryBuild); !strings.HasPrefix(c.Reason, "unsupported") || unsupported.Outcome != OutcomeDone {
		t.Fatalf("unsupported = %#v", unsupported)
	}
}

// Go tools are found by comm or by argv[0], including a long pkg.test.
func TestGoProcessesFromProc(t *testing.T) {
	t.Parallel()
	proc := t.TempDir()
	for pid, files := range map[string]map[string]string{
		"10":   {"comm": "go\n", "cmdline": "go\x00build\x00"},
		"11":   {"comm": "averylongpackag\n", "cmdline": "/tmp/go-build1/b001/averylongpackage.test\x00-test.v\x00"},
		"12":   {"comm": "bash\n", "cmdline": "bash\x00"},
		"13":   {"comm": "node\n"},
		"self": {"comm": "x\n"},
	} {
		for name, content := range files {
			mustWrite(t, filepath.Join(proc, pid, name), content)
		}
	}
	if err := os.MkdirAll(filepath.Join(proc, "14"), 0o755); err != nil {
		t.Fatal(err)
	}
	processes, err := goProcessesIn(proc)
	if err != nil || len(processes) != 2 {
		t.Fatalf("processes = %v %v", processes, err)
	}
	if _, err := goProcessesIn(filepath.Join(proc, "absent")); err == nil {
		t.Fatal("an unlistable /proc was read as no processes")
	}
	if _, err := goProcesses(); err != nil {
		t.Fatalf("the real /proc: %v", err)
	}
}

// The real environment: temporary directories resolved and deduplicated,
// GOCACHE only when prune may clear it, and go clean against that cache.
func TestRealEnvironment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if dirs := tempDirs(link, dir, "", filepath.Join(dir, "absent")); len(dirs) != 2 {
		t.Fatalf("dirs = %v", dirs)
	}
	for settings, want := range map[goenv.Env]string{
		{GOCACHE: dir, GOCACHEPROG: "x"}:   "GOCACHEPROG",
		{GOCACHE: "off"}:                   "GOCACHE is",
		{GOCACHE: "relative"}:              "GOCACHE is",
		{GOCACHE: filepath.Join(dir, "x")}: "no build cache",
	} {
		if cache, problem := buildCache(settings); cache != "" || !strings.Contains(problem, want) {
			t.Errorf("%#v: %q %q", settings, cache, problem)
		}
	}
	if cache, problem := buildCache(goenv.Env{GOCACHE: dir}); cache != dir || problem != "" {
		t.Fatalf("a real cache = %q %q", cache, problem)
	}
	if env := NewEnv(); env.Now == nil || env.GoProcesses == nil || env.RemoveAll == nil || env.Measure == nil {
		t.Fatalf("NewEnv = %#v", env)
	}
	if measure(context.Background(), filepath.Join(dir, "absent")) != 0 {
		t.Fatal("an absent path measured bytes")
	}
}

// go clean runs against exactly the cache prune measured, never widened by
// GOFLAGS.
//
//nolint:paralleltest // sets PATH and GOFLAGS
func TestGoCleanRunsAgainstTheMeasuredCache(t *testing.T) {
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "record")
	script := "#!/bin/sh\necho \"$@ GOCACHE=$GOCACHE GOFLAGS=$GOFLAGS\" > " + record + "\n[ \"$FAIL\" = 1 ] && { echo boom; exit 3; }\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("GOFLAGS", "-modcache")
	t.Setenv("GOCACHE", t.TempDir())
	if err := goClean(context.Background(), "/the/cache"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(record)
	if got := strings.TrimSpace(string(raw)); got != "clean -cache GOCACHE=/the/cache GOFLAGS=" {
		t.Fatalf("go ran as %q", got)
	}
	t.Setenv("FAIL", "1")
	if err := goClean(context.Background(), "/the/cache"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("a failing go clean: %v", err)
	}
	// The real environment's GoClean is this function, bound to its cache.
	env := NewEnv()
	if env.GoCacheProblem != "" {
		t.Fatalf("a real GOCACHE was refused: %s", env.GoCacheProblem)
	}
	if err := env.GoClean(context.Background()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("NewEnv's go clean: %v", err)
	}
	real := resolveGo
	t.Cleanup(func() { resolveGo = real })
	resolveGo = func() (goenv.Env, error) { return goenv.Env{}, errors.New("unreadable env file") }
	if env := NewEnv(); !strings.Contains(env.GoCacheProblem, "unreadable env file") {
		t.Fatalf("unreadable settings = %q", env.GoCacheProblem)
	}
}
