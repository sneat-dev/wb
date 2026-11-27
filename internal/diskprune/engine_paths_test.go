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
)

// --auto is the daemon's unattended subset: scratch and the build-cache
// trim applied to the floor, never the full clean.
func TestAutoRunsTheUnattendedSubsetToTheFloor(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	tw.entry("go-build1", 8)
	root := buildCacheFixture(t, tw, map[string]time.Duration{"0a01-a": time.Hour})
	tw.categories = []category{scratchCategory{}, fullCleanCategory{}}
	var events []Event
	tw.Progress = func(event Event) { events = append(events, event) }
	report := tw.run(Options{Auto: true, AllowFullClean: true, Floor: 0.5})
	if report.Mode != "auto" || !report.Lock.Held || report.Filesystems[0].Target.Value != 0.5 {
		t.Fatalf("auto report = %#v", report)
	}
	if !exists(filepath.Join(root, "0a", cacheName("0a01-a"))) || exists(filepath.Join(tw.tmp, "go-build1")) {
		t.Fatal("auto ran the full clean or kept scratch")
	}
	if cat := categoryByName(t, report, categoryFullClean); cat.Reason != "never under --auto" {
		t.Fatalf("auto full clean = %#v", cat)
	}
	sawPlan := false
	for _, event := range events {
		sawPlan = sawPlan || event.Phase == "plan"
	}
	if report.Admission.Status != "bypassed" || len(events) < 2 || events[0].Phase != "measure" || !sawPlan {
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
	if it := itemsByName(report, CategoryScratch)["go-build1"]; it.Status != StatusSkipped || it.Reason != "probe-failed" || report.Outcome != OutcomeFailClosed || len(report.State) != 1 || report.Filesystems[0].Note != "probe-failed" {
		t.Fatalf("a probe failure must stop deletion: %#v", report)
	}
	if !exists(filepath.Join(flaky.tmp, "go-build1")) {
		t.Fatal("deleted on a filesystem that could not be measured")
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
	if err := os.Symlink(filepath.Join(root, "0a", cacheName("0a01-a")), filepath.Join(root, "0a", cacheName("0a03-a"))); err != nil {
		t.Fatal(err)
	}
	tw.categories = []category{hookedCategory{inner: buildCacheCategory{}, after: func() {
		_ = os.Remove(filepath.Join(root, "0a", cacheName("0a01-a")))
		now := tw.Now()
		_ = os.Chtimes(filepath.Join(root, "0a", cacheName("0a02-a")), now, now)
	}}}
	report := tw.run(Options{Apply: true, Categories: []string{CategoryBuild}, Floor: 0.01})
	if !exists(filepath.Join(root, "0a", cacheName("0a02-a"))) || categoryByName(t, report, CategoryBuild).Shards[0].Entries != 2 {
		t.Fatalf("report = %#v", categoryByName(t, report, CategoryBuild))
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
	if it := planScratchEntry(newTestWorld(t).World, Options{}, Table{}, dir, "go-build404", newReclaimAccumulator()); it != nil {
		t.Fatalf("a vanished entry was listed: %#v", it)
	}
	locked := filepath.Join(t.TempDir(), "locked")
	mustWrite(t, filepath.Join(locked, "go-build1", "f"), "x", 0o644)
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if it := planScratchEntry(newTestWorld(t).World, Options{}, Table{}, locked, "go-build1", newReclaimAccumulator()); os.Geteuid() != 0 && (it == nil || !strings.HasPrefix(it.Reason, "partial-scan")) {
		t.Fatalf("an unreadable entry = %#v", it)
	}
	if scanVerdict("/tmp/x", nil, nil, scanResult{Protected: "/tmp/x/clone"}) != "protected: /tmp/x/clone" || scanVerdict("/tmp/x", nil, nil, scanResult{ContainsMount: true}) != "contains-mount" {
		t.Fatal("scan verdicts")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tw := newTestWorld(t)
	tw.entry("go-build1", 8)
	if plan := (scratchCategory{}).plan(ctx, tw.World, Options{}); len(plan.items) != 0 {
		t.Fatalf("a cancelled plan listed %d items", len(plan.items))
	}
	if diskBytes(fakeInfo{}) != 7 {
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

// lockless is a category with no resource directory, so its filesystem is
// labelled by its item's own path.
type lockless struct{ path string }

func (lockless) name() string { return "lockless" }

func (l lockless) plan(context.Context, *World, Options) *categoryPlan {
	it := &item{ItemReport: ItemReport{Path: l.path, Status: StatusDelete}, estimate: 1}
	return &categoryPlan{applicable: true, items: []*item{it}}
}

func TestFilesystemLabelFallsBackToTheItemPath(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	tw.categories = []category{lockless{path: tw.tmp}}
	report := tw.run(Options{Categories: []string{"lockless"}})
	if len(report.Filesystems) != 1 || report.Filesystems[0].Path != tw.tmp {
		t.Fatalf("filesystems = %#v", report.Filesystems)
	}
}
