//go:build linux

package diskprune

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// --auto is the daemon's unattended subset: apply to the floor, scratch and
// the cheap caches only, never modules.
func TestAutoRunsTheUnattendedSubsetToTheFloor(t *testing.T) {
	t.Parallel()
	f := newModuleFixture(t)
	f.add("example.com/a", "v1.0.0", true, true)
	f.tw.entry("go-build1", 8)
	f.tw.categories = []category{scratchCategory{}, moduleCategory{}}
	var events []Event
	f.tw.Progress = func(event Event) { events = append(events, event) }
	report := f.tw.run(Options{Auto: true, Floor: 0.5, Categories: nil})
	if report.Mode != "auto" || !report.Lock.Held || report.Filesystems[0].Target.Value != 0.5 {
		t.Fatalf("auto report = %#v", report)
	}
	if !exists(filepath.Join(f.cache, "example.com", "a@v1.0.0")) || exists(filepath.Join(f.tw.tmp, "go-build1")) {
		t.Fatal("auto pruned modules or kept scratch")
	}
	if cat := categoryByName(t, report, CategoryModules); cat.Status != StatusRequiresCategory {
		t.Fatalf("auto modules = %#v", cat)
	}
	if report.Admission.Status != "bypassed" || len(events) < 2 || events[0].Phase != "plan" {
		t.Fatalf("admission %#v events %#v", report.Admission, events)
	}
}

// Apply re-probes before each item: a target met during apply makes later
// items beyond-target; a filesystem that stopped returning space skips the
// rest of the run on it; a probe failure keeps the plan's figure; nothing to
// do needs no admission; state directories are reported.
func TestApplyReprobesAndReportsState(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	tw.entry("go-build1", 8)
	tw.entry("go-build2", 8)
	tw.returned = float64(tw.total)
	report := tw.run(Options{Apply: true, Target: Target{Kind: TargetRatio, Ratio: 0.2}, Floor: 0.01})
	items := itemsByName(report, CategoryScratch)
	if len(tw.removed) != 1 || (items["go-build1"].Status != StatusBeyondTarget && items["go-build2"].Status != StatusBeyondTarget) {
		t.Fatalf("removed %v items %#v", tw.removed, items)
	}

	stopped := newTestWorld(t)
	stopped.entry("go-build1", 300<<20)
	root := buildCacheFixture(t, stopped, map[string]time.Duration{"0a01-a": 100 * time.Hour})
	stopped.categories = []category{scratchCategory{}, buildCacheCategory{}}
	stopped.returned = 0
	identity := stopped.Identity
	stopped.Identity = func(path string) (Identity, error) {
		id, err := identity(path)
		if strings.HasPrefix(path, root) {
			id.Dev++
		}
		return id, err
	}
	report = stopped.run(Options{Apply: true, Floor: 0.01})
	if len(report.Filesystems) != 2 || report.Filesystems[0].Path > report.Filesystems[1].Path {
		t.Fatalf("filesystems = %#v", report.Filesystems)
	}

	sameFS := newTestWorld(t)
	sameFS.entry("go-build1", 300<<20)
	buildCacheFixture(t, sameFS, map[string]time.Duration{"0a01-a": 100 * time.Hour})
	sameFS.categories = []category{scratchCategory{}, buildCacheCategory{}}
	sameFS.returned = 0
	report = sameFS.run(Options{Apply: true, Floor: 0.01})
	if shard := categoryByName(t, report, CategoryBuild).Shards[0]; shard.Status != StatusSkipped {
		t.Fatalf("a later category ran on a filesystem that returned nothing: %#v", shard)
	}

	flaky := newTestWorld(t)
	flaky.entry("go-build1", 8)
	probe, calls := flaky.Probe, 0
	flaky.Probe = func(path string) (diskFilesystem, error) {
		calls++
		if calls > 1 {
			return diskFilesystem{}, errors.New("statfs")
		}
		return probe(path)
	}
	if err := os.MkdirAll(flaky.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	report = flaky.run(Options{Apply: true, Floor: 0.01})
	if itemsByName(report, CategoryScratch)["go-build1"].Status != StatusDeleted || len(report.State) != 1 {
		t.Fatalf("flaky probe report = %#v", report)
	}

	idle := newTestWorld(t)
	report = idle.run(Options{Apply: true, Floor: 0.01})
	if idle.admitted != 0 || report.Admission.Status != "not-needed" || report.IsFinding() {
		t.Fatalf("idle run = %#v", report)
	}
}

// A full clean requested without the build cache selected is reported, not
// run.
func TestFullCleanWithoutTheBuildCacheSelected(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	buildCacheFixture(t, tw, map[string]time.Duration{"0a01-a": time.Hour})
	report := tw.run(Options{Apply: true, Categories: []string{CategoryScratch}, AllowFullClean: true, Floor: 0.01})
	if cat := categoryByName(t, report, categoryFullClean); cat.Reason != "requires --allow-full-clean" {
		t.Fatalf("full clean = %#v", cat)
	}
}

// Shards skip symlinks and unreadable entries; at apply, an entry that
// vanished or became young is left alone.
func TestBuildCacheShardEdges(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	root := buildCacheFixture(t, tw, map[string]time.Duration{"0a01-a": 100 * time.Hour, "0a02-a": 100 * time.Hour})
	if err := os.Symlink(filepath.Join(root, "0a", "0a01-a"), filepath.Join(root, "0a", "0a03-a")); err != nil {
		t.Fatal(err)
	}
	tw.categories = []category{hookedCategory{inner: buildCacheCategory{}, after: func() {
		_ = os.Remove(filepath.Join(root, "0a", "0a01-a"))
		now := tw.Now()
		_ = os.Chtimes(filepath.Join(root, "0a", "0a02-a"), now, now)
	}}}
	report := tw.run(Options{Apply: true, Categories: []string{CategoryBuild}, Floor: 0.01})
	if !exists(filepath.Join(root, "0a", "0a02-a")) || categoryByName(t, report, CategoryBuild).Shards[0].Entries != 2 {
		t.Fatalf("report = %#v", categoryByName(t, report, CategoryBuild))
	}
}

// Module cache walking skips the sum database, stray files and names Go
// would never write, and fails on what it cannot read.
func TestModuleCacheWalkEdges(t *testing.T) {
	t.Parallel()
	f := newModuleFixture(t)
	f.add("example.com/a", "v1.0.0", true, true)
	dl := filepath.Join(f.cache, "cache", "download")
	mustWrite(t, filepath.Join(dl, "sumdb", "sum.golang.org", "lookup", "x"), "", 0o644)
	mustWrite(t, filepath.Join(dl, "stray.txt"), "", 0o644)
	mustWrite(t, filepath.Join(dl, "Bad", "@v", "v1.0.0.mod"), "", 0o644)
	mustWrite(t, filepath.Join(dl, "example.com", "x", "@v", ".mod"), "", 0o644)
	versions, err := readModuleCache(f.tw.World, f.cache)
	if err != nil || len(versions) != 1 || versions[0].path != "example.com/a" {
		t.Fatalf("versions = %#v, %v", versions, err)
	}
	f.tw.Identity = func(path string) (Identity, error) {
		if strings.Contains(path, "@v") {
			return Identity{}, errors.New("statx")
		}
		return StatIdentity(path)
	}
	if _, err := readModuleCache(f.tw.World, f.cache); err == nil {
		t.Fatal("an identity failure was ignored")
	}
	if cat := categoryByName(t, f.tw.run(Options{Categories: []string{CategoryModules}}), CategoryModules); !strings.HasPrefix(cat.Reason, "partial-scan") {
		t.Fatalf("walk failure = %#v", cat)
	}
	f.tw.Identity = func(path string) (Identity, error) {
		if path == f.cache {
			return Identity{}, errors.New("statx")
		}
		return StatIdentity(path)
	}
	if cat := categoryByName(t, f.tw.run(Options{Categories: []string{CategoryModules}}), CategoryModules); !strings.HasPrefix(cat.Reason, "partial-scan") {
		t.Fatalf("root identity failure = %#v", cat)
	}
	if _, err := readModuleCache(f.tw.World, filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing download directory was read")
	}
	missing := filepath.Join(t.TempDir(), "go.sum")
	if err := readSumFile(missing, map[string]bool{}); err == nil {
		t.Fatal("a missing sum file was read")
	}
	vanished := newModuleFixture(t)
	vanished.add("example.com/a", "v1.0.0", true, false)
	lstatTarget := filepath.Join(vanished.cache, "cache", "download", "example.com", "a", "@v", "v1.0.0.info")
	if err := os.Remove(lstatTarget); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "absent"), lstatTarget); err != nil {
		t.Fatal(err)
	}
	vanished.tw.Identity = func(path string) (Identity, error) {
		if path == lstatTarget {
			return Identity{}, fs.ErrNotExist
		}
		return StatIdentity(path)
	}
	if _, err := readModuleCache(vanished.tw.World, vanished.cache); err == nil {
		t.Fatal("a vanished download file was ignored")
	}
}

// Go's version lock: a symlinked lock is refused, and a flock failure other
// than contention is an error.
//
//nolint:paralleltest // replaces the flock seam
func TestLockModuleVersionFailures(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), filepath.Join(dir, "v1.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := LockModuleVersion(filepath.Join(dir, "v1.lock")); err == nil {
		t.Fatal("a symlinked lock was opened")
	}
	mustWrite(t, filepath.Join(dir, "v2.lock"), "", 0o644)
	realFlock := lockSyscalls.flock
	t.Cleanup(func() { lockSyscalls.flock = realFlock })
	lockSyscalls.flock = func(int, int) error { return unix.ENOLCK }
	if _, err := LockModuleVersion(filepath.Join(dir, "v2.lock")); err == nil || errors.Is(err, ErrLocked) {
		t.Fatalf("unsupported flock = %v", err)
	}
}

// Shape checks refuse anything that is not exactly what its producer writes.
func TestScratchShapeChecks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if hasNumberedSubdirs(filepath.Join(dir, "absent")) || hasNumberedSubdirs(dir) {
		t.Fatal("missing or empty test directories have numbered subdirectories")
	}
	mustWrite(t, filepath.Join(dir, "TestX1", "notnumbered", "f"), "x", 0o644)
	if hasNumberedSubdirs(filepath.Join(dir, "TestX1")) {
		t.Fatal("a non-numbered subdirectory passed")
	}
	if isGoBuildCacheShape(filepath.Join(dir, "absent")) {
		t.Fatal("a missing cache has a cache shape")
	}
	noReadme := filepath.Join(dir, "c1")
	mustWrite(t, filepath.Join(noReadme, "0a", "0a01-a"), "x", 0o644)
	if isGoBuildCacheShape(noReadme) {
		t.Fatal("a cache without Go's README passed")
	}
	foreign := filepath.Join(dir, "c2")
	mustWrite(t, filepath.Join(foreign, "README"), "", 0o644)
	mustWrite(t, filepath.Join(foreign, "0a", "sub", "f"), "x", 0o644)
	if isGoBuildCacheShape(foreign) || onlyEntryFiles(filepath.Join(dir, "absent")) {
		t.Fatal("a shard with a subdirectory passed")
	}
	if planScratchEntry(newTestWorld(t).World, Options{}, Table{}, dir, "go-build404", newReclaimAccumulator()).Reason[:12] != "partial-scan" {
		t.Fatal("a vanished entry must be partial-scan")
	}
	if scanVerdict("/tmp/x", nil, scanResult{Protected: "/tmp/x/clone"}) != "protected: /tmp/x/clone" || scanVerdict("/tmp/x", nil, scanResult{ContainsMount: true}) != "contains-mount" {
		t.Fatal("scan verdicts")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tw := newTestWorld(t)
	tw.entry("go-build1", 8)
	if plan := (scratchCategory{}).plan(ctx, tw.World, Options{}); len(plan.items) != 0 {
		t.Fatalf("a cancelled plan listed %d items", len(plan.items))
	}
	if diskBytes(fakeInfo{}) != 7 || !changeTime(fakeInfo{}).IsZero() {
		t.Fatal("file info without a stat")
	}
}

// fakeInfo is a FileInfo with no system stat.
type fakeInfo struct{}

func (fakeInfo) Name() string       { return "f" }
func (fakeInfo) Size() int64        { return 7 }
func (fakeInfo) Mode() fs.FileMode  { return 0 }
func (fakeInfo) ModTime() time.Time { return time.Time{} }
func (fakeInfo) IsDir() bool        { return false }
func (fakeInfo) Sys() any           { return nil }
