//go:build linux

package diskprune

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Codex Astra code review, round 2.

// Files are removed only through a descriptor walk beneath the planned
// anchor: a moved anchor, a symlinked or missing component, ".." and an
// unopenable component are all refused before anything is unlinked.
func TestRemovalWalksBeneathThePlannedAnchor(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "shard", "entry"), "x", 0o644)
	anchor, err := AnchorOf(root)
	if err != nil {
		t.Fatal(err)
	}
	entry := identityOf(t, filepath.Join(root, "shard", "entry"))

	other := anchor
	other.ID.Ino++
	if _, err := RemoveFileBeneath(other, "shard", "entry", entry); !errors.Is(err, ErrChanged) {
		t.Fatalf("a different anchor: %v", err)
	}
	if _, err := RemoveFileBeneath(Anchor{Root: filepath.Join(root, "absent")}, "shard", "entry", entry); err == nil {
		t.Fatal("a missing anchor was opened")
	}
	if _, err := RemoveFileBeneath(anchor, "shard/../shard", "entry", entry); err == nil || errors.Is(err, ErrChanged) {
		t.Fatalf("a path leaving the anchor: %v", err)
	}
	elsewhere := t.TempDir()
	mustWrite(t, filepath.Join(elsewhere, "entry"), "x", 0o644)
	if err := os.Symlink(elsewhere, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveFileBeneath(anchor, "linked", "entry", identityOf(t, filepath.Join(elsewhere, "entry"))); !errors.Is(err, ErrChanged) || !exists(filepath.Join(elsewhere, "entry")) {
		t.Fatalf("a symlinked component was followed: %v", err)
	}
	if _, err := RemoveFileBeneath(anchor, "gone", "entry", entry); !errors.Is(err, ErrChanged) {
		t.Fatalf("a missing component: %v", err)
	}
	if os.Geteuid() != 0 {
		locked := filepath.Join(root, "locked")
		if err := os.Mkdir(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		if _, err := RemoveFileBeneath(anchor, "locked", "entry", entry); err == nil || errors.Is(err, ErrChanged) {
			t.Fatalf("an unopenable component: %v", err)
		}
	}
	if _, err := AnchorOf(filepath.Join(root, "shard", "entry")); err == nil {
		t.Fatal("a file was accepted as an anchor")
	}
	if bytes, err := RemoveFileBeneath(anchor, "shard", "entry", entry); err != nil || bytes <= 0 || exists(filepath.Join(root, "shard", "entry")) {
		t.Fatalf("the planned file: %d, %v", bytes, err)
	}
}

// A component that lies on another mount is refused.
//
//nolint:paralleltest // replaces the statx seam
func TestRemovalRefusesAComponentOnAnotherMount(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "shard", "entry"), "x", 0o644)
	anchor, err := AnchorOf(root)
	if err != nil {
		t.Fatal(err)
	}
	entry := identityOf(t, filepath.Join(root, "shard", "entry"))
	realStatx := statx
	t.Cleanup(func() { statx = realStatx })
	calls := 0
	statx = func(dirfd int, path string, flags int, mask int, stx *unix.Statx_t) error {
		err := realStatx(dirfd, path, flags, mask, stx)
		calls++
		if calls == 2 {
			stx.Mnt_id++
		}
		return err
	}
	if _, err := RemoveFileBeneath(anchor, "shard", "entry", entry); !errors.Is(err, ErrChanged) || !exists(filepath.Join(root, "shard", "entry")) {
		t.Fatalf("a component on another mount: %v", err)
	}
}

// The full clean takes candidates only from the cache the plan checked: a
// GOCACHE path now naming another directory is refused.
func TestFullCleanRefusesAReplacedCache(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	root := buildCacheFixture(t, tw, map[string]time.Duration{"0a01-a": 100 * time.Hour})
	tw.categories = []category{hookedCategory{inner: fullCleanCategory{}, after: func() {
		if err := os.Rename(root, root+".old"); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(root, "0a", cacheName("0a02-a")), "x", 0o644)
	}}}
	report := tw.run(Options{Apply: true, AllowFullClean: true, Categories: []string{CategoryBuild}, Target: Target{Kind: TargetMax}})
	it := categoryByName(t, report, categoryFullClean).Items[0]
	if !strings.HasPrefix(it.Reason, "changed: GOCACHE") || !exists(filepath.Join(root, "0a", cacheName("0a02-a"))) {
		t.Fatalf("a replaced cache = %#v", it)
	}
}

// A cache root that cannot be listed is a partial enumeration.
func TestUnlistableCacheIsPartial(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root lists every directory")
	}
	tw := newTestWorld(t)
	root := buildCacheFixture(t, tw, map[string]time.Duration{"0a01-a": 100 * time.Hour})
	if err := os.Chmod(root, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })
	if files, _, partial := readCacheFiles(tw.World, root); !partial || len(files) != 0 {
		t.Fatalf("an unlistable cache: %v %t", files, partial)
	}
}

// A category root inside a directory that a protected checkout shows
// through a bind mount (same device and inode) is protected.
func TestRootUnderAProtectedAliasIsRefused(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	source := t.TempDir()
	identity := identityOf(t, source)
	tw.ProtectedIDs = map[[2]uint64]string{{identity.Dev, identity.Ino}: "/checkouts/app"}
	if reason := tw.bindProblem(filepath.Join(source, "tmp")); !strings.Contains(reason, "/checkouts/app") {
		t.Fatalf("reason = %q", reason)
	}
}

// The extraction is revalidated under Go's lock: one that appeared since the
// plan, one that went away, and one that cannot be inspected each refuse the
// version; at plan, an uninspectable or non-directory extraction stops the
// walk.
func TestModuleExtractionPresenceIsRevalidated(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		extracted bool
		mutate    func(f *moduleFixture, tree string)
		want      string
	}{
		"appeared": {false, func(f *moduleFixture, tree string) { mustWrite(f.t, filepath.Join(tree, "go.mod"), "module x", 0o644) }, "changed: the version was extracted"},
		"gone": {true, func(f *moduleFixture, tree string) {
			_ = filepath.WalkDir(tree, func(path string, _ os.DirEntry, _ error) error { return os.Chmod(path, 0o755) })
			if err := os.RemoveAll(tree); err != nil {
				f.t.Fatal(err)
			}
		}, "changed: the extracted tree is gone"},
		"uninspectable": {false, func(f *moduleFixture, tree string) {
			if err := os.MkdirAll(filepath.Dir(tree), 0o755); err != nil {
				f.t.Fatal(err)
			}
			_ = os.Chmod(filepath.Dir(tree), 0o600)
			f.t.Cleanup(func() { _ = os.Chmod(filepath.Dir(tree), 0o755) })
		}, "changed: the extraction cannot be inspected"},
	} {
		if name == "uninspectable" && os.Geteuid() == 0 {
			continue
		}
		f := newModuleFixture(t)
		tree := f.add("example.com/a", "v1.0.0", true, c.extracted)
		f.tw.categories = []category{hookedCategory{inner: moduleCategory{}, after: func() { c.mutate(f, tree) }}}
		report := f.tw.run(Options{Apply: true, Categories: []string{CategoryModules}, Floor: 0.01})
		if it := categoryByName(t, report, CategoryModules).Items[0]; !strings.HasPrefix(it.Reason, c.want) {
			t.Errorf("%s: %#v", name, it)
		}
	}
	file := newModuleFixture(t)
	tree := file.add("example.com/a", "v1.0.0", true, false)
	mustWrite(t, tree, "not a directory", 0o644)
	if _, err := readModuleCache(file.tw.World, file.cache); err == nil {
		t.Fatal("a file in place of the extraction was walked past")
	}
	notdir := newModuleFixture(t)
	notdir.add("example.com/a", "v1.0.0", true, false)
	mustWrite(t, filepath.Join(notdir.cache, "example.com"), "a file where a directory belongs", 0o644)
	if _, err := readModuleCache(notdir.tw.World, notdir.cache); err == nil {
		t.Fatal("an uninspectable extraction was treated as absent")
	}
}

// Membership is refreshed when module deletion starts: an incomplete fresh
// inventory refuses, and a checkout registered since the plan protects its
// versions.
func TestModuleMembershipIsRefreshed(t *testing.T) {
	t.Parallel()
	gap := newModuleFixture(t)
	gap.add("example.com/a", "v1.0.0", true, true)
	gap.tw.RefreshCheckouts = func() ([]string, string) { return nil, "git worktree list failed" }
	report := gap.tw.run(Options{Apply: true, Categories: []string{CategoryModules}, Floor: 0.01})
	if it := categoryByName(t, report, CategoryModules).Items[0]; !strings.Contains(it.Reason, "git worktree list failed") {
		t.Fatalf("gap = %#v", it)
	}
	late := newModuleFixture(t)
	tree := late.add("example.com/a", "v1.0.0", true, true)
	newcomer := t.TempDir()
	mustWrite(t, filepath.Join(newcomer, "go.sum"), "example.com/a v1.0.0"+h+"\n", 0o644)
	mustWrite(t, filepath.Join(newcomer, "README.md"), "not a sum file", 0o644)
	late.tw.RefreshCheckouts = func() ([]string, string) { return []string{newcomer}, "" }
	report = late.tw.run(Options{Apply: true, Categories: []string{CategoryModules}, Floor: 0.01})
	if it := categoryByName(t, report, CategoryModules).Items[0]; it.Status == StatusDeleted || !exists(tree) {
		t.Fatalf("a version a new checkout names = %#v", it)
	}
}

// The .partial marker: a failure to create it is reported on the deleted
// version, and a marker that cannot be removed afterwards is a failure.
func TestModuleMarkerOutcomesAreReported(t *testing.T) {
	t.Parallel()
	f := newModuleFixture(t)
	f.add("example.com/a", "v1.0.0", true, true)
	dl := filepath.Join(f.cache, "cache", "download", "example.com", "a", "@v")
	f.tw.categories = []category{hookedCategory{inner: moduleCategory{}, after: func() {
		// A non-empty directory under the marker's name: it cannot be
		// opened as the marker, nor removed by name.
		mustWrite(t, filepath.Join(dl, "v1.0.0.partial", "x"), "x", 0o644)
	}}}
	report := f.tw.run(Options{Apply: true, Categories: []string{CategoryModules}, Floor: 0.01})
	it := categoryByName(t, report, CategoryModules).Items[0]
	if it.Status != StatusFailed || !strings.Contains(it.Error, ".partial marker remains") {
		t.Fatalf("marker = %#v", it)
	}
	if err := writeMarker("/dev/null"); err == nil {
		t.Fatal("a device was accepted as the marker")
	}
	if err := writeMarker(filepath.Join(t.TempDir(), "absent", "m")); err == nil {
		t.Fatal("a marker in a missing directory was reported written")
	}
	var applied ItemReport
	applyResult(&item{ItemReport: ItemReport{Reason: "unused"}}, itemResult{note: "marker not written"})
	applied.Reason = "unused"
	target := &item{ItemReport: applied}
	applyResult(target, itemResult{note: ".partial marker not written: ENOSPC"})
	if target.Status != StatusDeleted || target.Reason != "unused; .partial marker not written: ENOSPC" {
		t.Fatalf("note = %#v", target.ItemReport)
	}
}

// A module lock under the .lock name must be a regular file.
func TestModuleLockMustBeARegularFile(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "v1.0.0.lock")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LockModuleVersion(dir); err == nil {
		t.Fatal("a directory was taken as a lock")
	}
	fifo := filepath.Join(t.TempDir(), "v1.0.0.lock")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := LockModuleVersion(fifo); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO was taken as a lock")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening a FIFO lock blocked")
	}
}

// Bounded reads refuse an oversized file, and a proxy body over the limit
// is an error, never a prefix compared as the whole.
func TestBoundedReads(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "big")
	mustWrite(t, path, strings.Repeat("x", 11), 0o644)
	if _, err := readRegularFile(path, 10, false); err == nil {
		t.Fatal("an oversized file was read")
	}
	if raw, err := readRegularFile(path, 11, false); err != nil || len(raw) != 11 {
		t.Fatalf("a file at the limit: %d %v", len(raw), err)
	}
	if resolved("") != "" {
		t.Fatal("an empty path resolved")
	}
}

//nolint:paralleltest // replaces probeClient
func TestProbeBodyOverTheLimitIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("m", maxProbeBody+1)))
	}))
	defer server.Close()
	realClient := probeClient
	t.Cleanup(func() { probeClient = realClient })
	probeClient = server.Client()
	if _, body, err := fetchURL(context.Background(), "GET", server.URL); err == nil || body != nil {
		t.Fatalf("an oversized body was accepted: %d bytes, %v", len(body), err)
	}
}

// A cancelled run ends a scan early, as partial.
func TestScanStopsWhenTheRunIsCancelled(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a", "f"), "x", 0o644)
	result := scanTreeWithIDs(nil, func() bool { return true })(root, identityOf(t, root).MountID, nil)
	if !result.Partial || result.Files != 0 {
		t.Fatalf("a cancelled scan = %#v", result)
	}
	w := &World{}
	if w.runCancelled() {
		t.Fatal("a world with no run is cancelled")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.runCtx = ctx
	if !w.runCancelled() {
		t.Fatal("a cancelled run was not noticed")
	}
}

// Scratch's checks must all be of the planned directory: one swapped in
// while the tree is scanned is not removed.
func TestScratchRefusesADirectorySwappedDuringItsChecks(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	path := filepath.Join(tw.tmp, "go-build1")
	tw.entry("go-build1", 8)
	scan := tw.Scan
	applying := false
	tw.categories = []category{hookedCategory{inner: scratchCategory{}, after: func() { applying = true }}}
	tw.Scan = func(root string, mount uint64, protected []string) scanResult {
		result := scan(root, mount, protected)
		if applying && root == path {
			_ = os.Rename(path, path+".moved")
			mustWrite(t, filepath.Join(path, "f"), "x", 0o644)
		}
		return result
	}
	report := tw.run(Options{Apply: true, Floor: 0.01})
	if it := itemsByName(report, CategoryScratch)["go-build1"]; !strings.HasPrefix(it.Reason, "changed: not the directory") || !exists(filepath.Join(path, "f")) {
		t.Fatalf("a swapped directory = %#v", it)
	}
}

// A receipt's state directory that cannot be created is an error.
func TestReceiptStateDirectoryThatCannotBeCreated(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root creates anywhere")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	if err := WriteReceipt(filepath.Join(parent, ".wb"), Receipt{Time: time.Now(), Trigger: "manual"}); err == nil {
		t.Fatal("a receipt was reported written under an unwritable parent")
	}
}

// The world refreshes its checkouts with the same inventory it built.
func TestWorldRefreshesItsCheckouts(t *testing.T) {
	t.Parallel()
	deps, clone, _ := fakeWorldDeps(t)
	w, err := newWorld(context.Background(), "/r", nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	checkouts, gap := w.RefreshCheckouts()
	if gap != "" || len(checkouts) == 0 || checkouts[0] != clone {
		t.Fatalf("refreshed %v gap %q", checkouts, gap)
	}
}

// A child directory renamed away while it is emptied, with an empty
// directory put in its place, is replaced: the replacement is not removed in
// the original's stead.
func TestRemoveTreeLeavesAReplacedChildAlone(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	candidate := filepath.Join(parent, "c")
	sub := filepath.Join(candidate, "sub")
	mustWrite(t, filepath.Join(sub, "f"), "x", 0o644)
	remover := newTreeRemover()
	unlink := remover.sys.unlinkat
	swapped := false
	remover.sys.unlinkat = func(dirfd int, name string, flags int) error {
		if name == "f" && !swapped {
			swapped = true
			if err := os.Rename(sub, filepath.Join(candidate, "moved")); err != nil {
				t.Error(err)
			}
			if err := os.Mkdir(sub, 0o755); err != nil {
				t.Error(err)
			}
		}
		return unlink(dirfd, name, flags)
	}
	if _, err := remover.remove(parent, "c", identityOf(t, candidate)); !errors.Is(err, ErrReplaced) {
		t.Fatalf("a replaced child: %v", err)
	}
	if info, err := os.Stat(sub); err != nil || !info.IsDir() {
		t.Fatalf("the replacement was removed: %v", err)
	}
}

// A checkout a worktrees root holds counts even when no clone registers it:
// it is protected and read for references. The world's scans stop with the
// run's context.
func TestWorktreesRootCheckoutsAreInventoried(t *testing.T) {
	t.Parallel()
	deps, _, _ := fakeWorldDeps(t)
	resolution, err := deps.resolve("")
	if err != nil {
		t.Fatal(err)
	}
	held := filepath.Join(resolution.Write.WorktreesRoot, "task", "github.com", "acme", "app")
	if err := os.MkdirAll(filepath.Join(held, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := newWorld(context.Background(), "/r", nil, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(w.Checkouts, " "), held) || !strings.Contains(strings.Join(w.Protected, " "), held) {
		t.Fatalf("checkouts %v protected %v", w.Checkouts, w.Protected)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.runCtx = ctx
	if scan := w.Scan(held, identityOf(t, held).MountID, nil); !scan.Partial {
		t.Fatalf("a scan in a cancelled run = %#v", scan)
	}
}
