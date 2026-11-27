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

	"golang.org/x/sys/unix"
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
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("entries appeared behind the symlink: %v", entries)
	}
	// A symlinked state directory is refused as well, creating nothing.
	linkedState := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(elsewhere, linkedState); err != nil {
		t.Fatal(err)
	}
	if err := WriteReceipt(linkedState, Receipt{Time: time.Now(), Trigger: "manual"}); err == nil {
		t.Fatal("a receipt was written through a symlinked state directory")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("entries appeared behind the symlinked state: %v", entries)
	}
	// A state directory that does not exist yet is created.
	fresh := filepath.Join(t.TempDir(), "new", ".wb")
	if err := WriteReceipt(fresh, Receipt{Time: time.Now(), Trigger: "manual"}); err != nil {
		t.Fatalf("a fresh state directory: %v", err)
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
		tw := newTestWorld(t)
		tw.entry("go-build1", 8)
		buildCacheFixture(t, tw, map[string]time.Duration{"0a01-a": 100 * time.Hour})
		tw.BindOfSubdirectory = bind
		tw.categories = []category{scratchCategory{}, buildCacheCategory{}, fullCleanCategory{}}
		report := tw.run(Options{Categories: SelectableCategories, AllowFullClean: true, Floor: 0.01})
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
	files, _, partial := readCacheFiles(tw.World, root)
	if partial || len(files) != 1 {
		t.Fatalf("files %v partial %t", files, partial)
	}
	stale := files[0].identity
	stale.MountID++
	if _, err := removeIn(files[0].dir, files[0].name, stale); !errors.Is(err, ErrChanged) {
		t.Fatalf("a file checked on another mount was unlinked: %v", err)
	}
	if _, _, partial := readCacheFiles(tw.World, filepath.Join(t.TempDir(), "absent")); !partial {
		t.Fatal("a missing cache must be partial")
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
	realPath, realStatx := mountinfoPath, statx
	t.Cleanup(func() { mountinfoPath, statx = realPath, realStatx })
	mountinfoPath = info
	// With statx's mount ID, the line with that ID decides.
	mountID := uint64(7)
	statx = func(_ int, _ string, _ int, _ int, stx *unix.Statx_t) error {
		stx.Mask, stx.Mnt_id = unix.STATX_MNT_ID, mountID
		return nil
	}
	if bound, err := bindOfSubdirectory("/anything"); err != nil || !bound {
		t.Fatalf("mount 7 = %t, %v", bound, err)
	}
	mountID = 1
	if bound, err := bindOfSubdirectory("/anything"); err != nil || bound {
		t.Fatalf("mount 1 = %t, %v", bound, err)
	}
	mountID = 99
	if _, err := bindOfSubdirectory("/anything"); err == nil {
		t.Fatal("a mount ID missing from mountinfo was classified")
	}
	mountID = 1
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
}

// A scan that meets a protected root's device and inode under another path
// (a bind alias) reports it.
func TestScanRecognisesProtectedIdentities(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	inner := filepath.Join(root, "alias")
	mustWrite(t, filepath.Join(inner, "f"), "x", 0o644)
	identity := identityOf(t, inner)
	result := scanTreeWithIDs(map[[2]uint64]string{{identity.Dev, identity.Ino}: "/home/u/.wb"}, nil)(root, identityOf(t, root).MountID, nil)
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

// A registered worktree is protected by its path, readable or not; git
// output that does not list the clone itself is an inventory gap.
func TestRegisteredWorktreesAreProtectedByPath(t *testing.T) {
	t.Parallel()
	deps, clone, _ := fakeWorldDeps(t)
	unreadable := filepath.Join(t.TempDir(), "file", "wt")
	mustWrite(t, filepath.Dir(unreadable), "a file where a directory belongs", 0o644)
	deps.git = func(context.Context, string, ...string) (string, error) {
		return "worktree " + clone + "\x00\x00worktree " + unreadable + "\x00\x00", nil
	}
	w, err := newWorld(context.Background(), "/r", deps)
	if err != nil || w.ProtectedGap != "" || !strings.Contains(strings.Join(w.Protected, " "), unreadable) {
		t.Fatalf("gap %q protected %v (%v)", w.ProtectedGap, w.Protected, err)
	}
	deps.git = func(context.Context, string, ...string) (string, error) { return "worktree /elsewhere\x00\x00", nil }
	if w, _ := newWorld(context.Background(), "/r", deps); !strings.Contains(w.ProtectedGap, "not listed") {
		t.Fatalf("a list without the clone = %q", w.ProtectedGap)
	}
}

// A cache root or shard whose mount cannot be read, or that is a different
// mount, stops the walk there.
//
//nolint:paralleltest // replaces the mountIDOf seam
func TestCacheWalksRefuseUnidentifiedOrForeignMounts(t *testing.T) {
	realMountIDOf := mountIDOf
	t.Cleanup(func() { mountIDOf = realMountIDOf })
	tw := newTestWorld(t)
	root := buildCacheFixture(t, tw, map[string]time.Duration{"0a01-a": 100 * time.Hour})
	mountIDOf = func(path string) (uint64, error) { return 0, errors.New("statx") }
	if files, _, partial := readCacheFiles(tw.World, root); !partial || len(files) != 0 {
		t.Fatalf("unidentified root: %v %t", files, partial)
	}
	mountIDOf = func(path string) (uint64, error) {
		if filepath.Base(path) == "0a" {
			return 99, nil
		}
		return realMountIDOf(path)
	}
	if files, _, partial := readCacheFiles(tw.World, root); !partial || len(files) != 0 {
		t.Fatalf("a mounted shard: %v %t", files, partial)
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

// A registered worktree whose path holds a newline is read whole; output
// that is not NUL-terminated is an inventory gap.
func TestWorktreeListIsNULDelimited(t *testing.T) {
	t.Parallel()
	paths, err := parseWorktreeList("worktree /a/odd\nname\x00HEAD abc\x00\x00worktree /b\x00\x00")
	if err != nil || len(paths) != 2 || paths[0] != "/a/odd\nname" {
		t.Fatalf("paths %q err %v", paths, err)
	}
	if _, err := parseWorktreeList("worktree /a\n"); err == nil {
		t.Fatal("newline-delimited output was accepted")
	}
	if paths, err := parseWorktreeList(""); err != nil || paths != nil {
		t.Fatalf("empty output = %q %v", paths, err)
	}
	deps, clone, _ := fakeWorldDeps(t)
	deps.git = func(context.Context, string, ...string) (string, error) { return "worktree " + clone + "\n", nil }
	w, err := newWorld(context.Background(), "/r", deps)
	if err != nil || !strings.Contains(w.ProtectedGap, "NUL") {
		t.Fatalf("gap = %q (%v)", w.ProtectedGap, err)
	}
}
