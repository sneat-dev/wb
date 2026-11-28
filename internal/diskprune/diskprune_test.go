//go:build linux

package diskprune

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
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
		Measure:     measure,
		RemoveAll:   removeAll,
		MountPoints: func() ([]string, error) { return nil, nil },
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
	t.Cleanup(func() { _ = listener.Close() })
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
	if keep := scratchKeepReasonWith(f.env, Options{ScratchAge: DefaultScratchAge}, rootInfo, filepath.Join(f.tmp, "absent"), func(_ string, info os.FileInfo) uint64 { return deviceOf(info) }); keep != "unreadable" {
		t.Fatalf("absent = %q", keep)
	}
	if os.Geteuid() != 0 {
		locked := f.scratch("go-build2")
		if err := os.Chmod(filepath.Join(locked, "001"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(locked, "001"), 0o755) })
		if keep := scratchKeepReasonWith(f.env, Options{ScratchAge: DefaultScratchAge}, rootInfo, locked, func(_ string, info os.FileInfo) uint64 { return deviceOf(info) }); keep != "unreadable" {
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
	if report := Run(context.Background(), f.env, Options{Categories: []string{CategoryScratch}}); report.Outcome != OutcomeDone {
		t.Fatalf("a temporary directory that does not exist = %#v", report)
	}
	file := filepath.Join(f.tmp, "file")
	mustWrite(t, file, "x")
	f.env.TempDirs = []string{file}
	if report := Run(context.Background(), f.env, Options{Categories: []string{CategoryScratch}}); report.Outcome != OutcomeFailed || !strings.Contains(category(t, report, CategoryScratch).Reason, "cannot be read") {
		t.Fatalf("an unlistable temporary directory = %#v", report)
	}
	f.env.MountPoints = func() ([]string, error) { return nil, errors.New("no mountinfo") }
	if report := Run(context.Background(), f.env, Options{Categories: []string{CategoryScratch}}); report.Outcome != OutcomeFailed {
		t.Fatalf("unreadable mount points = %#v", report)
	}

	g := newFixture(t)
	g.scratch("go-build1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c := category(t, Run(ctx, g.env, Options{Apply: true, Categories: []string{CategoryScratch}}), CategoryScratch); len(c.Items) != 0 || c.Status != StatusFailed || c.Reason != "cancelled" {
		t.Fatalf("a cancelled run = %#v", c)
	}
	// Cancelled after measuring, before removing: nothing is removed.
	h := newFixture(t)
	kept := h.scratch("go-build1")
	measuring, stop := context.WithCancel(context.Background())
	h.env.Measure = func(context.Context, string) int64 { stop(); return 1 }
	if c := category(t, Run(measuring, h.env, Options{Apply: true, Categories: []string{CategoryScratch}}), CategoryScratch); c.Status != StatusFailed || !exists(kept) {
		t.Fatalf("cancelled during measurement = %#v", c)
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
	if dirs := tempDirs(link, dir, "", filepath.Join(dir, "absent")); len(dirs) != 1 {
		t.Fatalf("dirs = %v", dirs)
	}
	for settings, want := range map[goenv.Env]string{
		{GOCACHE: dir, GOCACHEPROG: "x"}:   "GOCACHEPROG",
		{GOCACHE: "off"}:                   "GOCACHE is",
		{GOCACHE: "relative"}:              "GOCACHE is",
		{GOCACHE: filepath.Join(dir, "x")}: "no build cache",
	} {
		if cache, problem, _ := buildCache(settings); cache != "" || !strings.Contains(problem, want) {
			t.Errorf("%#v: %q %q", settings, cache, problem)
		}
	}
	if cache, problem, failure := buildCache(goenv.Env{GOCACHE: dir}); cache != resolvedPath(dir) || problem != "" || failure != "" {
		t.Fatalf("a real cache = %q %q", cache, problem)
	}
	if env := NewEnv(); env.Now == nil || env.GoProcesses == nil || env.RemoveAll == nil || env.Measure == nil {
		t.Fatalf("NewEnv = %#v", env)
	}
	if measure(context.Background(), filepath.Join(dir, "absent")) != 0 {
		t.Fatal("an absent path measured bytes")
	}
	file := filepath.Join(dir, "file")
	mustWrite(t, file, "x")
	if measure(context.Background(), filepath.Join(file, "below")) != 0 {
		t.Fatal("a path through a file measured bytes")
	}
	// GOCACHE through a file cannot be inspected: a finding. A file as
	// GOCACHE is no cache.
	if _, _, failure := buildCache(goenv.Env{GOCACHE: filepath.Join(file, "cache")}); !strings.Contains(failure, "cannot be inspected") {
		t.Fatalf("an uninspectable GOCACHE = %q", failure)
	}
	if cache, problem, _ := buildCache(goenv.Env{GOCACHE: file}); cache != "" || !strings.Contains(problem, "no build cache") {
		t.Fatalf("a file as GOCACHE = %q %q", cache, problem)
	}
	if got := resolvedPath(filepath.Join(dir, "absent", "..", "x")); got != filepath.Join(dir, "x") {
		t.Fatalf("an unresolvable path = %q", got)
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
	if got := strings.TrimSpace(string(raw)); got != "clean -cache GOCACHE=/the/cache GOFLAGS=" && got != "clean -cache GOCACHE=/the/cache GOFLAGS= " {
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
	if env := NewEnv(); !strings.Contains(env.GoCacheError, "unreadable env file") {
		t.Fatalf("unreadable settings = %q", env.GoCacheError)
	}
}

// A scratch tree holding a mount point (a bind mount on its own filesystem
// included), or one that is or holds a Go cache, is kept; a directory
// replaced between the check and the removal is kept; a name outside WB's
// task-name characters is not scratch.
func TestScratchMountsCachesReplacementsAndNames(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mounted := f.scratch("go-build1")
	cache := f.scratch("wb-ci-go-cache")
	f.env.MountPoints = func() ([]string, error) { return []string{"/", filepath.Join(mounted, "001")}, nil }
	f.env.GoCaches = []string{filepath.Join(cache, "mod")}
	report := Run(context.Background(), f.env, Options{Apply: true, Categories: []string{CategoryScratch}})
	kept := category(t, report, CategoryScratch).Kept
	if kept["contains a mount"] != 1 || kept["is or holds a Go cache"] != 1 || !exists(mounted) || !exists(cache) {
		t.Fatalf("kept = %v", kept)
	}

	for _, name := range []string{"wb-personal notes-cache", "wb--cache", "wb-index-x"} {
		if scratchName(name) {
			t.Errorf("%q is scratch", name)
		}
	}
	for _, name := range []string{"wb-pr308-lint-go-cache", "wb-sync.report-cache", "wb-index-12"} {
		if !scratchName(name) {
			t.Errorf("%q is not scratch", name)
		}
	}
}

// The replacement check runs immediately before removal.
func TestScratchReplacedBeforeRemovalIsKept(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	path := f.scratch("go-build3")
	swapped := false
	f.env.Measure = func(context.Context, string) int64 { return 1 }
	f.env.MountPoints = func() ([]string, error) { return nil, nil }
	f.env.RemoveAll = func(p string) error { return os.RemoveAll(p) }
	// The only seam after the check is the clock read inside the age test,
	// so the swap happens in Now on its final call.
	now := f.env.Now
	f.env.Now = func() time.Time {
		at := now()
		if !swapped {
			swapped = true
			_ = os.Rename(path, path+".old")
			mustWrite(t, filepath.Join(path, "fresh"), "new")
		}
		return at
	}
	report := Run(context.Background(), f.env, Options{Apply: true, Categories: []string{CategoryScratch}})
	if c := category(t, report, CategoryScratch); c.Kept["replaced while checked"] != 1 || !exists(filepath.Join(path, "fresh")) {
		t.Fatalf("replaced = %#v", c)
	}
}

// The age covers the whole tree: a nested file changed recently keeps an
// otherwise old tree.
func TestScratchAgeCoversNestedChanges(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.env.Now = time.Now
	path := f.scratch("go-build4")
	old := time.Now().Add(-10 * 24 * time.Hour)
	_ = filepath.WalkDir(path, func(p string, _ os.DirEntry, _ error) error { return os.Chtimes(p, old, old) })
	nested := filepath.Join(path, "001", "data")
	if err := os.WriteFile(nested, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(filepath.Join(path, "001"), old, old)
	_ = os.Chtimes(path, old, old)
	report := Run(context.Background(), f.env, Options{Apply: true, Categories: []string{CategoryScratch}})
	if !exists(path) || category(t, report, CategoryScratch).Kept["changed within 72h"] != 1 {
		t.Fatalf("a recently changed nested file = %#v", report)
	}
}

// The build cache: an inspection failure is a finding, an empty cache has
// nothing to clear.
func TestBuildCacheFailureAndEmptyCache(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.env.GoCacheError = "GOCACHE cannot be inspected: EACCES"
	if report := Run(context.Background(), f.env, Options{Categories: []string{CategoryBuild}}); report.Outcome != OutcomeFailed {
		t.Fatalf("an inspection failure = %#v", report)
	}
	empty := newFixture(t)
	empty.env.GoCache = t.TempDir()
	mustWrite(t, filepath.Join(empty.env.GoCache, "README"), "This directory holds cached build artifacts")
	if err := os.MkdirAll(filepath.Join(empty.env.GoCache, "0a"), 0o755); err != nil {
		t.Fatal(err)
	}
	report := Run(context.Background(), empty.env, Options{Apply: true, Categories: []string{CategoryBuild}})
	if c := category(t, report, CategoryBuild); c.Status != StatusKeep || report.Outcome != OutcomeDone || empty.cleaned != 0 {
		t.Fatalf("an empty cache = %#v", report)
	}
	if _, err := cacheShards(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("an unreadable cache was taken as empty")
	}
	// Only two lowercase hex digits make a shard; an unreadable shard is a
	// failure, never empty.
	odd := newFixture(t)
	odd.env.GoCache = t.TempDir()
	mustWrite(t, filepath.Join(odd.env.GoCache, "zz", "file"), "x")
	if c := category(t, Run(context.Background(), odd.env, Options{Categories: []string{CategoryBuild}}), CategoryBuild); c.Status != StatusKeep {
		t.Fatalf("a non-shard directory = %#v", c)
	}
	if os.Geteuid() != 0 {
		locked := newFixture(t)
		if err := os.Chmod(filepath.Join(locked.env.GoCache, "0a"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(locked.env.GoCache, "0a"), 0o755) })
		if report := Run(context.Background(), locked.env, Options{Categories: []string{CategoryBuild}}); report.Outcome != OutcomeFailed {
			t.Fatalf("an unreadable shard = %#v", report)
		}
	}
}

// Process metadata that cannot be read for a reason other than the process
// exiting means prune cannot tell; mountinfo escapes are decoded.
func TestProcReadErrorsAndMountinfo(t *testing.T) {
	t.Parallel()
	proc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proc, "20", "comm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := goProcessesIn(proc); err == nil {
		t.Fatal("an unreadable comm was read as idle")
	}
	proc2 := t.TempDir()
	mustWrite(t, filepath.Join(proc2, "21", "comm"), "worker\n")
	if err := os.MkdirAll(filepath.Join(proc2, "21", "cmdline"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := goProcessesIn(proc2); err == nil {
		t.Fatal("an unreadable cmdline was read as idle")
	}
	only := t.TempDir()
	mustWrite(t, filepath.Join(only, "22", "comm"), "gopls\n")
	if processes, err := goProcessesIn(only); err != nil || len(processes) != 1 {
		t.Fatalf("a Go tool by comm alone = %v %v", processes, err)
	}
	info := filepath.Join(t.TempDir(), "mountinfo")
	mustWrite(t, info, "1 0 8:1 / / rw - ext4 /dev/sda1 rw\n5 1 8:1 /x /tmp/with\\040space rw - ext4 /dev/sda1 rw\nshort\n")
	points, err := mountPointsIn(info)
	if err != nil || len(points) != 2 || points[1] != "/tmp/with space" {
		t.Fatalf("points = %q %v", points, err)
	}
	if _, err := mountPointsIn(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing mountinfo was read")
	}
	if unescapeOctal(`a\999b\`) != `a\999b\` {
		t.Fatal("an invalid escape was decoded")
	}
	rootInfo, _ := os.Stat(t.TempDir())
	if keep := scratchCheck(Env{}, Options{}, rootInfo, filepath.Join(t.TempDir(), "absent"), nil); keep != "unreadable" {
		t.Fatalf("an absent candidate = %q", keep)
	}
	if _, err := mountPoints(); err != nil {
		t.Fatalf("the real mountinfo: %v", err)
	}
}

// Scratch stops when the Go caches cannot be located, skips a temporary
// directory inside a checkout, re-reads mount points before each removal,
// and reports a cancelled run as failed even with nothing to do.
func TestScratchStopsWithoutCacheLocationsAndInsideCheckouts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.scratch("go-build1")
	f.env.GoCachesUnknown = "Go's settings cannot be read"
	if report := Run(context.Background(), f.env, Options{Apply: true, Categories: []string{CategoryScratch}}); report.Outcome != OutcomeFailed {
		t.Fatalf("unknown cache locations = %#v", report)
	}

	g := newFixture(t)
	inside := g.scratch("go-build2")
	mustWrite(t, filepath.Join(g.tmp, ".git", "HEAD"), "ref")
	report := Run(context.Background(), g.env, Options{Apply: true, Categories: []string{CategoryScratch}})
	if c := category(t, report, CategoryScratch); !exists(inside) || len(c.Kept) != 1 {
		t.Fatalf("a temporary directory inside a checkout = %#v", c)
	}
	if checkoutAround(filepath.Join(t.TempDir(), "x")) != "" && os.Getenv("CI") == "" {
		t.Log("the test temporary directory is inside a checkout")
	}

	h := newFixture(t)
	late := h.scratch("go-build3")
	calls := 0
	h.env.MountPoints = func() ([]string, error) {
		calls++
		if calls > 1 {
			return []string{filepath.Join(late, "001")}, nil
		}
		return nil, nil
	}
	if c := category(t, Run(context.Background(), h.env, Options{Apply: true, Categories: []string{CategoryScratch}}), CategoryScratch); c.Kept["contains a mount"] != 1 || !exists(late) {
		t.Fatalf("a mount added before removal = %#v", c)
	}

	empty := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if report := Run(ctx, empty.env, Options{Apply: true, Categories: []string{CategoryScratch}}); report.Outcome != OutcomeFailed {
		t.Fatalf("a cancelled run with nothing to do = %#v", report)
	}
}

// A relative temporary directory is stored absolute; mountinfo keeps a
// non-breaking space inside a path.
func TestRelativeTempDirAndNonBreakingSpaces(t *testing.T) {
	t.Parallel()
	for _, dir := range tempDirs(".") {
		if !filepath.IsAbs(dir) {
			t.Fatalf("a relative temporary directory was kept relative: %q", dir)
		}
	}
	info := filepath.Join(t.TempDir(), "mountinfo")
	mustWrite(t, info, "7 1 8:1 / /tmp/a\u00a0b rw - ext4 /dev/sda1 rw\n")
	if points, err := mountPointsIn(info); err != nil || len(points) != 1 || points[0] != "/tmp/a\u00a0b" {
		t.Fatalf("points = %q %v", points, err)
	}
}

// With GOFLAGS=-modcache in Go's env file, the real go clean -cache that
// prune runs leaves the module cache alone and clears the build cache.
//
//nolint:paralleltest // sets GOENV, GOMODCACHE and GOFLAGS
func TestRealGoCleanNeverWidensToTheModuleCache(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not installed")
	}
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env")
	mustWrite(t, envFile, "GOFLAGS=-modcache\n")
	modcache := filepath.Join(dir, "mod")
	sentinel := filepath.Join(modcache, "cache", "download", "sentinel")
	mustWrite(t, sentinel, "keep me")
	cache := filepath.Join(dir, "build")
	mustWrite(t, filepath.Join(cache, "0a", "0a"+strings.Repeat("0", 62)+"-a"), "entry")
	t.Setenv("GOENV", envFile)
	t.Setenv("GOMODCACHE", modcache)
	t.Setenv("GOFLAGS", "")
	if err := goClean(context.Background(), cache); err != nil {
		t.Fatal(err)
	}
	if !exists(sentinel) {
		t.Fatal("go clean -cache removed the module cache")
	}
	if exists(filepath.Join(cache, "0a")) {
		t.Fatal("go clean -cache left the build cache")
	}
}

// GOCACHE and GOMODCACHE that overlap: go clean -cache would take the
// module cache with it, so the build cache is not cleared.
//
//nolint:paralleltest // replaces resolveGo
func TestOverlappingGoCachesAreNotCleared(t *testing.T) {
	dir := t.TempDir()
	real := resolveGo
	t.Cleanup(func() { resolveGo = real })
	resolveGo = func() (goenv.Env, error) {
		return goenv.Env{GOCACHE: dir, GOMODCACHE: filepath.Join(dir, "ab")}, nil
	}
	if env := NewEnv(); env.GoCache != "" || !strings.Contains(env.GoCacheProblem, "overlap") {
		t.Fatalf("overlapping caches = %q %q", env.GoCache, env.GoCacheProblem)
	}
}

// An entry that vanishes between the listing and its inspection is kept as
// unreadable; the no-follow chmod never follows a symlink.
//
//nolint:paralleltest // replaces lstatEntry
func TestVanishingEntryAndNoFollowChmod(t *testing.T) {
	f := newFixture(t)
	f.scratch("go-build1")
	real := lstatEntry
	t.Cleanup(func() { lstatEntry = real })
	lstatEntry = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	if c := category(t, Run(context.Background(), f.env, Options{Categories: []string{CategoryScratch}}), CategoryScratch); c.Kept["unreadable"] != 1 {
		t.Fatalf("a vanished entry = %#v", c)
	}
	lstatEntry = real
	target := t.TempDir()
	if err := os.Chmod(target, 0o500); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	chmodDirNoFollow(link)
	if info, _ := os.Stat(target); info.Mode().Perm() != 0o500 {
		t.Fatalf("a symlink was followed: %v", info.Mode())
	}
	_ = os.Chmod(target, 0o755)
}
