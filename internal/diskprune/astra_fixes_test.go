//go:build linux

package diskprune

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Codex Astra code review behaviours.

// Receipts are never written through a symlinked directory and never
// overwrite an existing file.
func TestReceiptsRefuseSymlinksAndCollisions(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(state, "disk")); err != nil {
		t.Fatal(err)
	}
	if err := WriteReceipt(state, Receipt{Time: time.Now(), Trigger: "manual"}); err == nil {
		t.Fatal("a receipt was written through a symlinked directory")
	}
	entries, _ := os.ReadDir(elsewhere)
	if len(entries) > 1 {
		t.Fatalf("files appeared behind the symlink: %v", entries)
	}
	clean := t.TempDir()
	receipt := Receipt{Time: time.Unix(1700000000, 0), Trigger: "manual"}
	if err := WriteReceipt(clean, receipt); err != nil {
		t.Fatal(err)
	}
	if err := WriteReceipt(clean, receipt); err == nil {
		t.Fatal("an existing receipt was overwritten")
	}
}

// A root on a bind mount of a subdirectory, or one whose mount cannot be
// identified, skips every category that would delete under it.
func TestBindMountedRootsAreRefused(t *testing.T) {
	t.Parallel()
	for name, bind := range map[string]func(string) (bool, error){
		"bound":   func(string) (bool, error) { return true, nil },
		"unknown": func(string) (bool, error) { return false, errors.New("no mountinfo") },
	} {
		f := newModuleFixture(t)
		f.add("example.com/a", "v1.0.0", true, true)
		f.tw.entry("go-build1", 8)
		buildCacheFixture(t, f.tw, map[string]time.Duration{"0a01-a": 100 * time.Hour})
		store := filepath.Join(t.TempDir(), "store")
		mustWrite(t, filepath.Join(store, "v10", "x"), "x", 0o644)
		f.tw.PnpmStores = []string{store}
		f.tw.BindOfSubdirectory = bind
		f.tw.categories = []category{scratchCategory{}, buildCacheCategory{}, pnpmCategory{}, moduleCategory{}, fullCleanCategory{}}
		report := f.tw.run(Options{Categories: SelectableCategories, AllowFullClean: true, Floor: 0.01})
		for _, cat := range report.Categories {
			if !strings.HasPrefix(cat.Reason, "unknown") {
				t.Errorf("%s: %s = %q", name, cat.Name, cat.Reason)
			}
		}
	}
}

// A shard that is its own mount is not the cache; a file whose directory
// is no longer on the checked mount is not unlinked.
func TestBuildCacheRefusesMountedShards(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	root := buildCacheFixture(t, tw, map[string]time.Duration{"0a01-a": 100 * time.Hour})
	files, partial := readCacheFiles(tw.World, root)
	if partial || len(files) != 1 {
		t.Fatalf("files %v partial %t", files, partial)
	}
	stale := files[0].identity
	stale.MountID++
	if _, err := RemoveFileAt(files[0].dir, files[0].name, stale); !errors.Is(err, ErrChanged) {
		t.Fatalf("a file checked on another mount was unlinked: %v", err)
	}
	if _, partial := readCacheFiles(tw.World, filepath.Join(t.TempDir(), "absent")); !partial {
		t.Fatal("a missing cache must be partial")
	}
}

// The module cache walk refuses a symlinked or non-directory cache/, a
// download directory on another mount, and a file Go does not write.
func TestModuleCacheWalkStaysInside(t *testing.T) {
	t.Parallel()
	f := newModuleFixture(t)
	f.add("example.com/a", "v1.0.0", true, true)
	mustWrite(t, filepath.Join(f.cache, "cache", "download", "example.com", "a", "@v", "v1.0.0.zip.bak"), "x", 0o644)
	mustWrite(t, filepath.Join(f.cache, "cache", "download", "example.com", "b", "@v", "v1.0.0.mod"), "module b\n", 0o444)
	if err := os.Symlink(t.TempDir(), filepath.Join(f.cache, "cache", "download", "example.com", "b", "@v", "v1.0.0.info")); err != nil {
		t.Fatal(err)
	}
	report := f.tw.run(Options{Categories: []string{CategoryModules}, Floor: 0.01})
	items := map[string]string{}
	for _, it := range categoryByName(t, report, CategoryModules).Items {
		items[filepath.Base(it.Path)] = it.Reason
	}
	if !strings.HasPrefix(items["a@v1.0.0"], "unknown") || !strings.HasPrefix(items["b@v1.0.0"], "unknown") || len(items) != 2 {
		t.Fatalf("foreign files = %v", items)
	}

	linked := newModuleFixture(t)
	linked.add("example.com/a", "v1.0.0", true, false)
	target := t.TempDir()
	if err := os.Rename(filepath.Join(linked.cache, "cache"), filepath.Join(target, "cache")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(target, "cache"), filepath.Join(linked.cache, "cache")); err != nil {
		t.Fatal(err)
	}
	if _, err := readModuleCache(linked.tw.World, linked.cache); err == nil {
		t.Fatal("a symlinked cache/ was walked")
	}
	if _, err := readModuleCache(linked.tw.World, filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing module cache was walked")
	}
	nocache := t.TempDir()
	if _, err := readModuleCache(linked.tw.World, nocache); err == nil {
		t.Fatal("a module cache without cache/ was walked")
	}
}

// An extracted tree holding a checkout, a mount, a socket, protected data or
// an unreadable entry is kept, at plan and again before any mutation.
func TestExtractedTreeVerdicts(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		scan scanResult
		want string
	}{
		{scanResult{Protected: "/p"}, "protected: /p"},
		{scanResult{Partial: true}, "partial-scan"},
		{scanResult{ContainsMount: true}, "contains-mount"},
		{scanResult{HasGit: true}, "contains-checkout"},
		{scanResult{HasSpecial: true}, "runtime"},
		{scanResult{}, ""},
	} {
		if got := extractedVerdict(c.scan); got != c.want {
			t.Errorf("%#v = %q, want %q", c.scan, got, c.want)
		}
	}
	f := newModuleFixture(t)
	tree := f.add("example.com/a", "v1.0.0", true, true)
	if err := os.Chmod(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(tree, "vendor", "repo", ".git"), "gitdir: x", 0o644)
	if it := categoryByName(t, f.tw.run(Options{Categories: []string{CategoryModules}}), CategoryModules).Items[0]; it.Reason != "contains-checkout" {
		t.Fatalf("a checkout in a module tree = %#v", it)
	}
	late := newModuleFixture(t)
	lateTree := late.add("example.com/a", "v1.0.0", true, true)
	late.tw.categories = []category{hookedCategory{inner: moduleCategory{}, after: func() {
		late.tw.Scan = func(string, uint64, []string) scanResult { return scanResult{HasGit: true} }
	}}}
	report := late.tw.run(Options{Apply: true, Categories: []string{CategoryModules}, Floor: 0.01})
	if it := categoryByName(t, report, CategoryModules).Items[0]; it.Reason != "contains-checkout" || !exists(lateTree) {
		t.Fatalf("a checkout found at apply = %#v", it)
	}
}

// Under Go's lock the version's complete file set must equal the plan's:
// a file appeared, one went away, one was rewritten in place, or the ziphash
// changed, and nothing is touched.
func TestModuleRevalidationIsComplete(t *testing.T) {
	t.Parallel()
	dl := func(f *moduleFixture, name string) string {
		return filepath.Join(f.cache, "cache", "download", "example.com", "a", "@v", name)
	}
	for name, mutate := range map[string]func(*moduleFixture){
		"appeared": func(f *moduleFixture) { mustWrite(f.t, dl(f, "v1.0.0.zip.part"), "x", 0o644) },
		"gone":     func(f *moduleFixture) { _ = os.Remove(dl(f, "v1.0.0.info")) },
		"rewritten": func(f *moduleFixture) {
			_ = os.Chmod(dl(f, "v1.0.0.info"), 0o644)
			mustWrite(f.t, dl(f, "v1.0.0.info"), `{"Version":"v1.0.0","Time":"later"}`, 0o644)
		},
		"ziphash": func(f *moduleFixture) {
			_ = os.Chmod(dl(f, "v1.0.0.ziphash"), 0o644)
			mustWrite(f.t, dl(f, "v1.0.0.ziphash"), "h1:different", 0o644)
		},
	} {
		f := newModuleFixture(t)
		tree := f.add("example.com/a", "v1.0.0", true, true)
		f.tw.categories = []category{hookedCategory{inner: moduleCategory{}, after: func() { mutate(f) }}}
		report := f.tw.run(Options{Apply: true, Categories: []string{CategoryModules}, Floor: 0.01})
		it := categoryByName(t, report, CategoryModules).Items[0]
		if !strings.HasPrefix(it.Reason, "changed") || !exists(tree) || exists(dl(f, "v1.0.0.partial")) {
			t.Errorf("%s: %#v (tree kept %t)", name, it, exists(tree))
		}
	}
	gone := newModuleFixture(t)
	gone.add("example.com/a", "v1.0.0", true, false)
	if reason := unchangedSincePlan(gone.tw.World, moduleVersion{downloadDir: filepath.Join(t.TempDir(), "absent")}, 0); !strings.HasPrefix(reason, "changed") {
		t.Fatalf("an unreadable download directory = %q", reason)
	}
}

// The final process check runs immediately before the first mutation, after
// the slow proofs.
func TestModuleFinalProcessCheck(t *testing.T) {
	t.Parallel()
	for name, late := range map[string]func(*moduleFixture){
		"busy":      func(f *moduleFixture) { f.tw.processes = []Process{{PID: 6, Comm: "go", Readable: true}} },
		"untrusted": func(f *moduleFixture) { f.tw.evidence = Evidence{Unknown: "fd exhaustion"} },
	} {
		f := newModuleFixture(t)
		tree := f.add("example.com/a", "v1.0.0", true, true)
		http := f.tw.HTTP
		f.tw.HTTP = func(ctx context.Context, method, url string) (int, []byte, error) {
			late(f)
			return http(ctx, method, url)
		}
		report := f.tw.run(Options{Apply: true, Categories: []string{CategoryModules}, Floor: 0.01})
		if it := categoryByName(t, report, CategoryModules).Items[0]; it.Status != StatusSkipped || !exists(tree) {
			t.Errorf("%s: %#v", name, it)
		}
	}
}

// Sum-file fingerprints fail closed on a checkout that cannot be resolved or
// walked.
func TestSumStampFailures(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "absent"), filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	if _, err := sumStamp([]string{filepath.Join(dir, "dangling")}, nil); err == nil {
		t.Fatal("a dangling checkout was fingerprinted")
	}
	locked := filepath.Join(dir, "locked")
	mustWrite(t, filepath.Join(locked, "sub", "go.sum"), "", 0o644)
	if err := os.Chmod(filepath.Join(locked, "sub"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(locked, "sub"), 0o755) })
	if _, err := sumStamp([]string{locked}, nil); err == nil && os.Geteuid() != 0 {
		t.Fatal("an unreadable checkout was fingerprinted")
	}
	f := newModuleFixture(t)
	f.add("example.com/a", "v1.0.0", true, true)
	root := f.checkout(map[string]string{"go.sum": ""})
	f.tw.categories = []category{hookedCategory{inner: moduleCategory{}, after: func() { _ = os.RemoveAll(root) }}}
	report := f.tw.run(Options{Apply: true, Categories: []string{CategoryModules}, Floor: 0.01})
	if it := categoryByName(t, report, CategoryModules).Items[0]; !strings.HasPrefix(it.Reason, "incomplete-reference-set") {
		t.Fatalf("a checkout gone at apply = %#v", it)
	}
}

// pnpm: an incomplete protected inventory, a store holding a mount, an
// unresolvable cache directory, and the default cache locations.
func TestPnpmInventoryMountsAndDefaultCache(t *testing.T) {
	t.Parallel()
	gap, _ := pnpmWorld(t)
	gap.ProtectedGap = "git worktree list failed"
	if cat := categoryByName(t, gap.run(Options{}), CategoryPnpm); !strings.HasPrefix(cat.Reason, "unknown") {
		t.Fatalf("gap = %#v", cat)
	}
	mounted, _ := pnpmWorld(t)
	mounted.Scan = func(string, uint64, []string) scanResult { return scanResult{ContainsMount: true} }
	if cat := categoryByName(t, mounted.run(Options{}), CategoryPnpm); !strings.HasPrefix(cat.Reason, "contains-mount") {
		t.Fatalf("mounted store = %#v", cat)
	}
	none, fake := pnpmWorld(t)
	fake.cacheDir = "undefined"
	none.Home = ""
	if it := categoryByName(t, none.run(Options{Apply: true, Floor: 0.01}), CategoryPnpm).Items[0]; it.Status != StatusDeleted && !strings.HasPrefix(it.Reason, "unknown") {
		t.Fatalf("undefined cache dir = %#v", it)
	}
	w := &World{Home: "/home/u"}
	if got := w.pnpmDefaultCache(); got != "/home/u/.cache/pnpm" && os.Getenv("XDG_CACHE_HOME") == "" {
		t.Fatalf("default cache = %q", got)
	}
	if resolved("") != "" {
		t.Fatal("an empty path resolved")
	}
	measured, _ := pnpmWorld(t)
	measured.Identity = func(string) (Identity, error) { return Identity{}, errors.New("statx") }
	if singleLinkBytes(measured.World, measured.PnpmStores) != 0 {
		t.Fatal("an unmeasurable store has bytes")
	}
}

// XDG_CACHE_HOME decides pnpm's default cache when set.
//
//nolint:paralleltest // sets XDG_CACHE_HOME
func TestPnpmDefaultCacheFollowsXDG(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/xdg")
	if got := (&World{Home: "/home/u"}).pnpmDefaultCache(); got != "/xdg/pnpm" {
		t.Fatalf("xdg cache = %q", got)
	}
	t.Setenv("XDG_CACHE_HOME", "")
	if got := (&World{Home: "/home/u"}).pnpmDefaultCache(); got != "/home/u/.cache/pnpm" {
		t.Fatalf("linux cache = %q", got)
	}
}

// pnpm with no way to name its cache directory is refused.
func TestPnpmUnknownCacheDirectory(t *testing.T) {
	t.Parallel()
	tw, fake := pnpmWorld(t)
	fake.cacheDir = ""
	tw.Home = ""
	w := tw.World
	if got := w.pnpmDefaultCache(); got != filepath.Join(".cache", "pnpm") && os.Getenv("XDG_CACHE_HOME") == "" {
		t.Fatalf("default without a home = %q", got)
	}
}

// mountinfo escapes are decoded, a missing or matchless mountinfo is an
// error, and a bind of a subdirectory is recognised.
//
//nolint:paralleltest // replaces the mountinfo seam
func TestMountinfoDecodingAndBinds(t *testing.T) {
	if got := unescapeMountinfo(`/mnt/with\040space\134x\0`); got != `/mnt/with space\x\0` {
		t.Fatalf("decoded %q", got)
	}
	info := filepath.Join(t.TempDir(), "mountinfo")
	mustWrite(t, info, "1 0 8:1 / / rw - ext4 /dev/sda1 rw\n7 1 8:1 /home/u/.wb/reports /mnt/with\\040space rw - ext4 /dev/sda1 rw\nshort\n", 0o644)
	realPath := mountinfoPath
	t.Cleanup(func() { mountinfoPath = realPath })
	mountinfoPath = info
	if bound, err := bindOfSubdirectory("/mnt/with space/tmp"); err != nil || !bound {
		t.Fatalf("bind of a subdirectory = %t, %v", bound, err)
	}
	if bound, err := bindOfSubdirectory("/usr"); err != nil || bound {
		t.Fatalf("the root mount = %t, %v", bound, err)
	}
	mustWrite(t, info, "short\n", 0o644)
	if _, err := bindOfSubdirectory("/usr"); err == nil {
		t.Fatal("a matchless mountinfo identified a mount")
	}
	mountinfoPath = filepath.Join(t.TempDir(), "absent")
	if _, err := bindOfSubdirectory("/usr"); err == nil {
		t.Fatal("a missing mountinfo identified a mount")
	}
}

// Reading a README never follows a symlink or blocks on a FIFO.
func TestReadSmallRegularRefusesSpecialFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "README")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSmallRegular(fifo, 10); !errors.Is(err, ErrChanged) {
		t.Fatalf("a FIFO was read: %v", err)
	}
	if err := os.Symlink(fifo, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := readSmallRegular(filepath.Join(dir, "link"), 10); err == nil {
		t.Fatal("a symlink was followed")
	}
	cache := filepath.Join(dir, "wb-x-cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(cache, "README"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isGoBuildCacheShape(cache) {
		t.Fatal("a FIFO README made a Go cache")
	}
	if linkCount(fakeInfo{}) != 1 {
		t.Fatal("link count without a stat")
	}
}

// A scan that meets a protected root's device and inode under another path
// (a bind alias) reports it.
func TestScanRecognisesProtectedIdentities(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	inner := filepath.Join(root, "alias")
	mustWrite(t, filepath.Join(inner, "f"), "x", 0o644)
	identity := identityOf(t, inner)
	result := scanTreeWithIDs(map[[2]uint64]string{{identity.Dev, identity.Ino}: "/home/u/.wb"})(root, identityOf(t, root).MountID, nil)
	if result.Protected != "/home/u/.wb" {
		t.Fatalf("protected identity = %#v", result)
	}
	if ids := protectedIDs([]string{root, filepath.Join(root, "absent")}); len(ids) != 1 {
		t.Fatalf("ids = %v", ids)
	}
}

// Scratch at apply re-checks ownership and producer shape, and honours
// cancellation before removing.
func TestScratchApplyRevalidates(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*testWorld, string){
		"owner":    func(tw *testWorld, path string) { tw.UID++ },
		"shape":    func(tw *testWorld, path string) { mustWrite(tw.t, filepath.Join(path, "notnumbered", "f"), "x", 0o644) },
		"vanished": func(tw *testWorld, path string) { _ = os.RemoveAll(path) },
	} {
		tw := newTestWorld(t)
		path := filepath.Join(tw.tmp, "TestFoo1")
		mustWrite(t, filepath.Join(path, "001", "f"), "x", 0o644)
		tw.categories = []category{hookedCategory{inner: scratchCategory{}, after: func() { mutate(tw, path) }}}
		report := tw.run(Options{Apply: true, Floor: 0.01})
		if it := itemsByName(report, CategoryScratch)["TestFoo1"]; it.Status != StatusSkipped || !strings.HasPrefix(it.Reason, "changed") {
			t.Errorf("%s: %#v", name, it)
		}
	}
	tw := newTestWorld(t)
	tw.entry("go-build1", 8)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	identity := identityOf(t, filepath.Join(tw.tmp, "go-build1"))
	if result := applyScratch(ctx, tw.World, 0, tw.tmp, "go-build1", identity); result.reason != "cancelled" {
		t.Fatalf("cancelled apply = %#v", result)
	}
	if it := planScratchEntry(tw.World, Options{}, Table{}, tw.tmp, "TestFoo", newReclaimAccumulator()); it != nil {
		t.Fatalf("a non-numbered Test name = %#v", it)
	}
	notNumbered := filepath.Join(tw.tmp, "TestBar9")
	mustWrite(t, filepath.Join(notNumbered, "x", "f"), "x", 0o644)
	if it := planScratchEntry(tw.World, Options{}, Table{}, tw.tmp, "TestBar9", newReclaimAccumulator()); it == nil || it.Reason != "not-a-known-producer" {
		t.Fatalf("a Test directory without numbered children = %#v", it)
	}
}

// A per-item probe that fails after the category boundary measured fine
// stops that item, fail-closed.
func TestItemProbeFailureIsFailClosed(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	tw.entry("go-build1", 8)
	probe, calls := tw.Probe, 0
	tw.Probe = func(path string) (diskFilesystem, error) {
		calls++
		// 1: projection; 2: the category boundary; 3: the item itself.
		if calls == 3 {
			return diskFilesystem{}, errors.New("statfs")
		}
		return probe(path)
	}
	report := tw.run(Options{Apply: true, Floor: 0.01})
	if it := itemsByName(report, CategoryScratch)["go-build1"]; it.Reason != "probe-failed" || report.Outcome != OutcomeFailClosed {
		t.Fatalf("item = %#v outcome %q", it, report.Outcome)
	}
}

// A registered worktree that exists but cannot be read is an inventory gap.
func TestUnreadableRegisteredWorktreeIsAGap(t *testing.T) {
	t.Parallel()
	deps, clone, _ := fakeWorldDeps(t)
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.MkdirAll(filepath.Join(locked, "wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	deps.git = func(context.Context, string, ...string) (string, error) {
		return "worktree " + clone + "\n\nworktree " + filepath.Join(locked, "wt") + "\n", nil
	}
	w, err := newWorld(context.Background(), "/r", nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 && !strings.Contains(w.ProtectedGap, "cannot be read") {
		t.Fatalf("gap = %q", w.ProtectedGap)
	}
}

// pnpm's default cache on macOS.
func TestPnpmDefaultCacheOnMacOS(t *testing.T) {
	t.Parallel()
	if os.Getenv("XDG_CACHE_HOME") == "" {
		if got := pnpmDefaultCacheFor("darwin", "/Users/u"); got != "/Users/u/Library/Caches/pnpm" {
			t.Fatalf("macOS cache = %q", got)
		}
	}
}

// A cache root or shard, or a module download directory, whose mount cannot
// be read, or that is a different mount, stops the walk there.
//
//nolint:paralleltest // replaces the mountIDOf seam
func TestCacheWalksRefuseUnidentifiedOrForeignMounts(t *testing.T) {
	realMountIDOf := mountIDOf
	t.Cleanup(func() { mountIDOf = realMountIDOf })
	tw := newTestWorld(t)
	root := buildCacheFixture(t, tw, map[string]time.Duration{"0a01-a": 100 * time.Hour})
	mountIDOf = func(path string) (uint64, error) { return 0, errors.New("statx") }
	if files, partial := readCacheFiles(tw.World, root); !partial || len(files) != 0 {
		t.Fatalf("unidentified root: %v %t", files, partial)
	}
	mountIDOf = func(path string) (uint64, error) {
		if filepath.Base(path) == "0a" {
			return 99, nil
		}
		return realMountIDOf(path)
	}
	if files, partial := readCacheFiles(tw.World, root); !partial || len(files) != 0 {
		t.Fatalf("a mounted shard: %v %t", files, partial)
	}
	f := newModuleFixture(t)
	f.add("example.com/a", "v1.0.0", true, false)
	mountIDOf = func(path string) (uint64, error) {
		if filepath.Base(path) == "@v" {
			return 99, nil
		}
		return realMountIDOf(path)
	}
	if _, err := readModuleCache(f.tw.World, f.cache); !errors.Is(err, ErrContainsMount) {
		t.Fatalf("a mounted @v directory: %v", err)
	}
	mountIDOf = func(path string) (uint64, error) {
		if filepath.Base(path) == "example.com" {
			return 0, errors.New("statx")
		}
		return realMountIDOf(path)
	}
	if _, err := readModuleCache(f.tw.World, f.cache); err == nil {
		t.Fatal("an unidentified download directory was walked")
	}
}

// An inode whose creation time cannot be read has none to compare: 0.
func TestBirthTimeOfAMissingEntryIsUnknown(t *testing.T) {
	t.Parallel()
	if birth := birthAt(-1, filepath.Join(t.TempDir(), "absent")); birth != 0 {
		t.Fatalf("birth = %d", birth)
	}
}

// A file recreated under a reused inode number is not the checked file.
func TestReusedInodeNumberIsNotTheCheckedFile(t *testing.T) {
	t.Parallel()
	checked := Identity{Dev: 1, Ino: 2, Birth: 100}
	if checked.sameInode(1, 2, 200) || !checked.sameInode(1, 2, 100) || !(Identity{Dev: 1, Ino: 2}).sameInode(1, 2, 300) {
		t.Fatal("birth time comparison")
	}
}
