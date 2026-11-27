//go:build linux

package diskprune

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbconfig"
)

// A /proc entry that fails to read for any reason but "the process exited"
// makes the whole table untrusted, and a container cgroup is a container.
func TestProcessTableIncompleteReadsAreUntrusted(t *testing.T) {
	t.Parallel()
	proc := newFakeProc(t)
	if err := os.MkdirAll(filepath.Join(proc.root, "77", "stat"), 0o755); err != nil {
		t.Fatal(err)
	}
	if table := readProcessTable(proc.source()); !strings.Contains(table.Evidence.Unknown, "could not be read completely") {
		t.Fatalf("an unreadable stat = %#v", table.Evidence)
	}
	docker := newFakeProc(t)
	docker.write("self/cgroup", "0::/system.slice/docker-abc.scope\n")
	if table := readProcessTable(docker.source()); !strings.Contains(table.Evidence.Unknown, "cgroup names docker") {
		t.Fatalf("container cgroup = %#v", table.Evidence)
	}
	if !mentionsPath("-I/tmp/go-build1/b001", "/tmp/go-build1") || mentionsPath("x1/tmp/go-build1", "/tmp/go-build1") {
		t.Fatal("joined flag spelling")
	}
}

// A Go build tool whose age cannot be read sets the fail-closed floor.
func TestBuildFloorUnknownAgeFailsClosed(t *testing.T) {
	t.Parallel()
	floor, pid := buildFloor(Table{Processes: []Process{{PID: 9, Comm: "compile", Age: AgeUnknown, Readable: true}}}, "/c", 0)
	if floor != 72*time.Hour || pid != 9 {
		t.Fatalf("floor %s pid %d", floor, pid)
	}
}

// The trim stops as soon as the filesystem reaches its target, a shard it
// only partly emptied says so, and evidence that turns untrusted during a
// full clean stops it.
func TestTrimStopsAtTargetAndReportsPartialShards(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	buildCacheFixture(t, tw, map[string]time.Duration{"0a01-a": 100 * time.Hour})
	probe, calls := tw.Probe, 0
	tw.Probe = func(path string) (diskFilesystem, error) {
		calls++
		fs, err := probe(path)
		// Probes 1-3 are the plan's projection and the engine's checks
		// before the item; the 4th is the trim's own first target check.
		if calls > 3 {
			fs.AvailableBytes = fs.TotalBytes
		}
		return fs, err
	}
	report := tw.run(Options{Apply: true, Categories: []string{CategoryBuild}, Target: Target{Kind: TargetRatio, Ratio: 0.5}, Floor: 0.01})
	if shard := categoryByName(t, report, CategoryBuild).Shards[0]; shard.Status != StatusKeep {
		t.Fatalf("a trim that stopped at the target removed %#v", shard)
	}

	partial := newTestWorld(t)
	buildCacheFixture(t, partial, map[string]time.Duration{"0a01-a": 100 * time.Hour, "0a02-a": 90 * time.Hour})
	first := true
	partial.RemoveFile = func(anchor Anchor, rel, name string, id Identity) (int64, error) {
		if first {
			first = false
			return 0, ErrChanged
		}
		return RemoveFileBeneath(anchor, rel, name, id)
	}
	report = partial.run(Options{Apply: true, Categories: []string{CategoryBuild}, Floor: 0.01})
	if shard := categoryByName(t, report, CategoryBuild).Shards[0]; shard.Status != StatusPartial {
		t.Fatalf("a partly emptied shard = %#v", shard)
	}

	untrusted := newTestWorld(t)
	buildCacheFixture(t, untrusted, map[string]time.Duration{"0a01-a": time.Hour})
	untrusted.categories = []category{hookedCategory{inner: fullCleanCategory{}, after: func() { untrusted.evidence = Evidence{Unknown: "fd exhaustion"} }}}
	report = untrusted.run(Options{Apply: true, Categories: []string{CategoryBuild}, AllowFullClean: true, Floor: 0.01})
	if it := categoryByName(t, report, categoryFullClean).Items[0]; !strings.HasPrefix(it.Reason, "unknown") {
		t.Fatalf("full clean under untrusted evidence = %#v", it)
	}

	empty := newTestWorld(t)
	empty.Go.GOCACHE = t.TempDir()
	if plan := (fullCleanCategory{}).plan(context.Background(), empty.World, Options{AllowFullClean: true}); len(plan.items) != 0 {
		t.Fatal("an empty cache has nothing to clean")
	}
	if plan := (fullCleanCategory{}).plan(context.Background(), empty.World, Options{Auto: true, AllowFullClean: true}); plan.Reason != "never under --auto" {
		t.Fatalf("auto full clean = %#v", plan.CategoryReport)
	}
}

// Scratch counts a process naming the temporary directory's unresolved
// spelling, keeps a candidate that is a resolved cache, keeps a real Go
// cache that holds a fuzz corpus, and re-checks evidence at apply.
func TestScratchAliasesCachesAndApplyEvidence(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	tw.entry("go-build1", 8)
	tw.TempAliases = map[string][]string{tw.tmp: {"/tmp-first", "/tmp-alias"}}
	tw.processes = []Process{{PID: 4, Comm: "cat", Readable: true, Cwd: "/tmp-alias/go-build1"}}
	if it := itemsByName(tw.run(Options{Floor: 0.01}), CategoryScratch)["go-build1"]; it.Reason != "held-open" {
		t.Fatalf("alias reference = %#v", it)
	}
	if len(tw.aliases("/elsewhere/x")) != 0 {
		t.Fatal("an unrelated path has an alias")
	}

	cache := newTestWorld(t)
	inner := filepath.Join(cache.tmp, "wb-ci-go-cache")
	mustWrite(t, filepath.Join(inner, "README"), goCacheReadme, 0o644)
	cache.Go.GOCACHE = inner
	fuzz := filepath.Join(cache.tmp, "wb-fz-go-cache")
	mustWrite(t, filepath.Join(fuzz, "README"), goCacheReadme, 0o644)
	mustWrite(t, filepath.Join(fuzz, "fuzz", "pkg", "FuzzX", "seed"), "x", 0o644)
	items := itemsByName(cache.run(Options{Floor: 0.01}), CategoryScratch)
	if !strings.HasPrefix(items["wb-ci-go-cache"].Reason, "contains-cache-data") || items["wb-fz-go-cache"].Reason != "contains-cache-data" {
		t.Fatalf("caches in scratch = %#v", items)
	}

	late := newTestWorld(t)
	late.entry("go-build1", 8)
	late.categories = []category{hookedCategory{inner: scratchCategory{}, after: func() { late.evidence = Evidence{Unknown: "fd exhaustion"} }}}
	if it := itemsByName(late.run(Options{Apply: true, Floor: 0.01}), CategoryScratch)["go-build1"]; !strings.HasPrefix(it.Reason, "unknown") {
		t.Fatalf("untrusted evidence at apply = %#v", it)
	}
}

// A flat candidate moved away before anything is removed is left alone, and
// a hard-linked file frees nothing.
func TestRemovalChecksAFlatCandidateAndHardLinks(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	candidate := filepath.Join(parent, "c")
	mustWrite(t, filepath.Join(candidate, "f"), "x", 0o644)
	moved := newTreeRemover()
	moved.fdPath = func(int) (string, error) { return "/elsewhere", nil }
	if _, err := moved.remove(parent, "c", identityOf(t, candidate)); !errors.Is(err, ErrMoved) || !exists(filepath.Join(candidate, "f")) {
		t.Fatalf("flat move = %v", err)
	}
	if err := os.Link(filepath.Join(candidate, "f"), filepath.Join(parent, "keeper")); err != nil {
		t.Fatal(err)
	}
	if bytes, err := removeIn(candidate, "f", identityOf(t, filepath.Join(candidate, "f"))); err != nil || bytes != 0 {
		t.Fatalf("hard-linked unlink freed %d (%v)", bytes, err)
	}
	if err := os.Link(filepath.Join(parent, "keeper"), filepath.Join(parent, "second")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(parent, "keeper"))
	if err != nil {
		t.Fatal(err)
	}
	if unlinkBytes(info) != 0 {
		t.Fatal("a hard-linked file counts as freed")
	}
}

// A run lock that fails for any reason but another prune is fail-closed and
// says why.
func TestRunLockFailureIsReported(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	tw.LockDirs = func([]string) (*Lock, error) { return nil, errors.New("flock: operation not supported") }
	report := tw.run(Options{Apply: true, Floor: 0.01})
	if report.Outcome != OutcomeFailClosed || !strings.Contains(report.Lock.Error, "not supported") {
		t.Fatalf("report = %#v", report)
	}
}

// The world resolves a symlinked temporary directory and records the
// spelling the environment used; relative cache values are left to the
// category to refuse; checkout discovery reaches any depth and reports an
// unreadable directory.
func TestWorldResolvesRootsAndDiscoversStrictly(t *testing.T) {
	t.Parallel()
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "tmp-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(t.TempDir(), "gotmp-link")
	if err := os.Symlink(real, second); err != nil {
		t.Fatal(err)
	}
	// TMPDIR and GOTMPDIR reach one directory by two symlinks: one root,
	// both spellings kept for the reference check.
	dirs, aliases := tempDirs(worldDeps{tempDir: func() string { return link }, getenv: func(string) string { return second }})
	if len(dirs) != 1 || len(aliases[dirs[0]]) != 2 || aliases[dirs[0]][0] != link || aliases[dirs[0]][1] != second {
		t.Fatalf("dirs %v aliases %v", dirs, aliases)
	}
	if resolvedRoot("off") != "off" {
		t.Fatal("a relative root was resolved")
	}
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a", "b", "c", "d", ".git", "HEAD"), "x", 0o644)
	// A clone placed deeper than host/owner/repo (a GitLab subgroup) is
	// found, and a symlink loop is walked once.
	if err := os.Symlink(root, filepath.Join(root, "a", "loop")); err != nil {
		t.Fatal(err)
	}
	if found, err := findCheckouts(os.ReadDir, root, true); err != nil || len(found) != 1 || !strings.HasSuffix(found[0], filepath.Join("b", "c", "d")) {
		t.Fatalf("a deep checkout = %v, %v", found, err)
	}
	failure := errors.New("EACCES")
	readDir := func(dir string) ([]os.DirEntry, error) {
		if strings.HasSuffix(dir, "b") {
			return nil, failure
		}
		return os.ReadDir(dir)
	}
	if _, err := findCheckouts(readDir, root, true); !errors.Is(err, failure) {
		t.Fatalf("an unreadable nested directory = %v", err)
	}
}

// The daemon falls back to the default interval on a broken wb.yaml instead
// of a zero-interval ticker, and re-arms when the interval changes.
func TestAutoPrunerIntervalFallbackAndRearm(t *testing.T) {
	t.Parallel()
	h := newAutoHarness(t)
	var intervals []time.Duration
	var broken atomic.Bool
	broken.Store(true)
	h.pruner.Config = func() (wbconfig.DiskConfig, error) {
		if broken.Load() {
			return wbconfig.DiskConfig{}, errors.New("bad yaml")
		}
		return wbconfig.DiskConfig{CheckInterval: time.Minute}, nil
	}
	h.pruner.Ticker = func(interval time.Duration) (<-chan time.Time, func()) {
		if interval <= 0 {
			t.Errorf("ticker armed with %s", interval)
		}
		intervals = append(intervals, interval)
		return h.ticks, func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.pruner.Run(ctx); close(done) }()
	h.ticks <- h.now
	broken.Store(false)
	h.ticks <- h.now
	h.ticks <- h.now
	cancel()
	<-done
	if len(intervals) != 2 || intervals[0] != wbconfig.DefaultDiskCheckInterval || intervals[1] != time.Minute {
		t.Fatalf("intervals = %v", intervals)
	}
}

// A tree moved after its top was confirmed is caught before the next
// descent.
func TestRemovalStopsWhenMovedMidway(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	candidate := filepath.Join(parent, "c")
	mustWrite(t, filepath.Join(candidate, "sub", "f"), "x", 0o644)
	remover := newTreeRemover()
	calls := 0
	remover.fdPath = func(fd int) (string, error) {
		calls++
		if calls == 1 {
			return procFDPath(fd)
		}
		return "/home/u/projects/.wb", nil
	}
	if _, err := remover.remove(parent, "c", identityOf(t, candidate)); !errors.Is(err, ErrMoved) || !exists(filepath.Join(candidate, "sub", "f")) {
		t.Fatalf("moved midway = %v", err)
	}
}

// AC per-filesystem-target: a temporary directory on a filesystem already at
// its target keeps its entries, while one on a full filesystem is pruned;
// each item is judged against its own filesystem, not the category's first.
func TestEachItemIsJudgedOnItsOwnFilesystem(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	full := tw.tmp
	ample := t.TempDir()
	tw.TempDirs = []string{full, ample}
	tw.entry("go-build1", 8)
	mustWrite(t, filepath.Join(ample, "go-build2", "001", "data"), "x", 0o644)
	identity := tw.Identity
	tw.Identity = func(path string) (Identity, error) {
		id, err := identity(path)
		if strings.HasPrefix(path, ample) {
			id.Dev += 1000
		}
		return id, err
	}
	probe := tw.Probe
	tw.Probe = func(path string) (diskFilesystem, error) {
		fs, err := probe(path)
		if strings.HasPrefix(path, ample) {
			fs.AvailableBytes = fs.TotalBytes / 2
		}
		return fs, err
	}
	report := tw.run(Options{Apply: true, Target: Target{Kind: TargetRatio, Ratio: 0.2}, Floor: 0.01})
	items := itemsByName(report, CategoryScratch)
	if items["go-build1"].Status != StatusDeleted || items["go-build2"].Status != StatusBeyondTarget || !exists(filepath.Join(ample, "go-build2")) {
		t.Fatalf("items = %#v", items)
	}
	if len(report.Filesystems) != 2 {
		t.Fatalf("filesystems = %#v", report.Filesystems)
	}
}

// Round-2 review (Fable 5.2) behaviours.

// N1: a symlinked clone directory is found; one that cannot be followed is
// an inventory gap; another user's unreadable directory is not.
func TestFindCheckoutsFollowsSymlinksAndSkipsForeignDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := t.TempDir()
	mustWrite(t, filepath.Join(target, ".git", "HEAD"), "ref", 0o644)
	if err := os.MkdirAll(filepath.Join(root, "github.com", "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "github.com", "acme", "app")); err != nil {
		t.Fatal(err)
	}
	found, err := findCheckouts(os.ReadDir, root, true)
	if err != nil || len(found) != 1 || !strings.HasSuffix(found[0], "app") {
		t.Fatalf("symlinked clone: %v, %v", found, err)
	}
	if err := os.Symlink(filepath.Join(root, "absent"), filepath.Join(root, "github.com", "acme", "dangling")); err != nil {
		t.Fatal(err)
	}
	if _, err := findCheckouts(os.ReadDir, root, true); err == nil {
		t.Fatal("a dangling symlinked placement was skipped silently")
	}
	ghost := t.TempDir()
	if err := os.Mkdir(filepath.Join(ghost, "mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	readDir := func(dir string) ([]os.DirEntry, error) {
		switch filepath.Base(dir) {
		case "lost+found", "mine":
			return nil, os.ErrPermission
		}
		entries, err := os.ReadDir(dir)
		if dir == ghost {
			entries = append(entries, ghostDir{name: "lost+found"})
		}
		return entries, err
	}
	if _, err := findCheckouts(readDir, ghost, true); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("an unreadable directory of this user's = %v", err)
	}
	if err := os.Remove(filepath.Join(ghost, "mine")); err != nil {
		t.Fatal(err)
	}
	if found, err := findCheckouts(readDir, ghost, true); err != nil || len(found) != 0 {
		t.Fatalf("another user's unreadable directory = %v, %v", found, err)
	}
}

// ghostDir is a directory entry with nothing on disk behind it, standing in
// for a directory this user does not own.
type ghostDir struct{ name string }

func (g ghostDir) Name() string             { return g.name }
func (ghostDir) IsDir() bool                { return true }
func (ghostDir) Type() os.FileMode          { return os.ModeDir }
func (ghostDir) Info() (os.FileInfo, error) { return nil, os.ErrNotExist }

// N6: a probe that fails inside the trim stops it as probe-failed, and a
// cancelled context stops it before anything is removed.
func TestTrimProbeFailureAndCancellation(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	root := buildCacheFixture(t, tw, map[string]time.Duration{"0a01-a": 100 * time.Hour})
	probe, calls := tw.Probe, 0
	tw.Probe = func(path string) (diskFilesystem, error) {
		calls++
		if calls > 3 {
			return diskFilesystem{}, errors.New("statfs")
		}
		return probe(path)
	}
	report := tw.run(Options{Apply: true, Categories: []string{CategoryBuild}, Floor: 0.01})
	if it := categoryByName(t, report, CategoryBuild).Items[0]; it.Reason != "probe-failed" || !exists(filepath.Join(root, "0a", cacheName("0a01-a"))) {
		t.Fatalf("probe failure in the trim = %#v", it)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	files, anchor, _ := readCacheFiles(tw.World, root)
	if result := trimEntries(ctx, tw.World, root, anchor, files, 0, newShardTracker(files), false); result.unlinked != 0 || !exists(filepath.Join(root, "0a", cacheName("0a01-a"))) {
		t.Fatalf("a cancelled trim removed %d bytes", result.unlinked)
	}
}

// N5: the receipt finding follows unattended runs only, and the newest of
// the daemon's and a manual --auto run's.
func TestReceiptFindingFollowsUnattendedRuns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Now()
	for _, receipt := range []Receipt{
		{Time: now.Add(-time.Hour), Trigger: "daemon", Outcome: OutcomeDone},
		{Time: now.Add(-time.Minute), Trigger: "manual", Outcome: OutcomeTargetUnmet},
	} {
		if err := WriteReceipt(dir, receipt); err != nil {
			t.Fatal(err)
		}
	}
	if finding := ReceiptFinding(dir); finding != "" {
		t.Fatalf("a manual run's own target produced %q", finding)
	}
	if err := WriteReceipt(dir, Receipt{Time: now, Trigger: "manual-auto", Outcome: OutcomeFailed}); err != nil {
		t.Fatal(err)
	}
	if finding := ReceiptFinding(dir); !strings.Contains(finding, "manual-auto") {
		t.Fatalf("a manual --auto failure = %q", finding)
	}
}

// N7: a cmdline, environ or descriptor that fails to read for any reason
// but permission or exit makes the table untrusted.
func TestProcessReadFailuresBeyondPermissionAreUntrusted(t *testing.T) {
	t.Parallel()
	for _, broken := range []string{"cmdline", "environ"} {
		proc := newFakeProc(t)
		proc.add(fakeProcess{pid: 50, comm: "bash", cwd: "/", uid: 1})
		path := filepath.Join(proc.root, "50", broken)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if table := readProcessTable(proc.source()); table.Evidence.Unknown == "" {
			t.Errorf("an unreadable %s was trusted", broken)
		}
	}
	proc := newFakeProc(t)
	proc.add(fakeProcess{pid: 51, comm: "bash", cwd: "/", uid: 1})
	proc.write("51/fd/3", "not a link")
	if table := readProcessTable(proc.source()); table.Evidence.Unknown == "" {
		t.Error("an unreadable descriptor was trusted")
	}
}

// N6 (scratch): an entry that disappears while it is examined is not a
// finding.
func TestScratchEntryVanishingDuringExaminationIsIgnored(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	path := tw.entry("go-build1", 8)
	tw.Identity = func(string) (Identity, error) { return Identity{}, os.ErrNotExist }
	if it := planScratchEntry(tw.World, Options{}, Table{}, tw.tmp, "go-build1", newReclaimAccumulator()); it != nil {
		t.Fatalf("vanished at identity = %#v", it)
	}
	tw.Identity = StatIdentity
	tw.Scan = func(root string, mount uint64, protected []string) scanResult {
		_ = os.RemoveAll(path)
		return scanResult{Partial: true}
	}
	if it := planScratchEntry(tw.World, Options{}, Table{}, tw.tmp, "go-build1", newReclaimAccumulator()); it != nil {
		t.Fatalf("vanished during the scan = %#v", it)
	}
}
