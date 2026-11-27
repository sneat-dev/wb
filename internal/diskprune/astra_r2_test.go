//go:build linux

package diskprune

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// Bounded reads refuse an oversized file, and a proxy body over the limit
// is an error, never a prefix compared as the whole.
func TestBoundedReads(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "big")
	mustWrite(t, path, strings.Repeat("x", 11), 0o644)
	if _, err := readRegularFile(path, 10); err == nil {
		t.Fatal("an oversized file was read")
	}
	if raw, err := readRegularFile(path, 11); err != nil || len(raw) != 11 {
		t.Fatalf("a file at the limit: %d %v", len(raw), err)
	}
	if _, err := readRegularFile(filepath.Join(t.TempDir(), "absent"), 10); err == nil {
		t.Fatal("a missing file was read")
	}
	if resolved("") != "" {
		t.Fatal("an empty path resolved")
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

// A checkout a worktrees root holds is protected even when no clone
// registers it. The world's scans stop with the run's context.
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
	w, err := newWorld(context.Background(), "/r", deps)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(w.Protected, " "), held) {
		t.Fatalf("protected %v", w.Protected)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.runCtx = ctx
	if scan := w.Scan(held, identityOf(t, held).MountID, nil); !scan.Partial {
		t.Fatalf("a scan in a cancelled run = %#v", scan)
	}
}

// A mapped file's pathname is kept byte for byte: consecutive spaces
// survive, a newline arrives as \012, and anonymous or pseudo mappings are
// not files.
func TestMappedFilesKeepThePathnameExactly(t *testing.T) {
	t.Parallel()
	maps := "7f00-7f01 r--p 00000000 08:01 42                         /tmp/a  b/lib.so\n" +
		"7f01-7f02 r--p 00000000 08:01 43   /tmp/odd\\012name (deleted)\n" +
		"7f02-7f03 rw-p 00000000 00:00 0                          [heap]\n" +
		"7f03-7f04 rw-p 00000000 00:00 0\n" +
		"short line\n"
	files := mappedFiles(maps)
	if len(files) != 2 || files[0] != "/tmp/a  b/lib.so" || files[1] != "/tmp/odd\nname" {
		t.Fatalf("files = %q", files)
	}
}

// The cache walk: a shard that is a protected root under another path, an
// entry that cannot be inspected, or one whose identity cannot be read all
// make the enumeration partial and contribute no candidate.
//
//nolint:paralleltest // replaces the lstatEntry seam
func TestCacheWalkIncompleteEvidenceIsPartial(t *testing.T) {
	tw := newTestWorld(t)
	root := buildCacheFixture(t, tw, map[string]time.Duration{"0a01-a": 100 * time.Hour})
	shard := identityOf(t, filepath.Join(root, "0a"))
	tw.ProtectedIDs = map[[2]uint64]string{{shard.Dev, shard.Ino}: "/home/u/projects/.wb"}
	if files, _, partial := readCacheFiles(tw.World, root); !partial || len(files) != 0 {
		t.Fatalf("a protected shard: %v %t", files, partial)
	}
	tw.ProtectedIDs = nil
	identity := tw.Identity
	tw.Identity = func(path string) (Identity, error) {
		if strings.HasSuffix(path, "-a") {
			return Identity{}, errors.New("statx")
		}
		return identity(path)
	}
	if files, _, partial := readCacheFiles(tw.World, root); !partial || len(files) != 0 {
		t.Fatalf("an entry without identity: %v %t", files, partial)
	}
	tw.Identity = identity
	real := lstatEntry
	t.Cleanup(func() { lstatEntry = real })
	lstatEntry = func(string) (os.FileInfo, error) { return nil, os.ErrPermission }
	if files, _, partial := readCacheFiles(tw.World, root); !partial || len(files) != 0 {
		t.Fatalf("an uninspectable entry: %v %t", files, partial)
	}
	lstatEntry = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	if files, _, partial := readCacheFiles(tw.World, root); partial || len(files) != 0 {
		t.Fatalf("an entry Go removed meanwhile: %v %t", files, partial)
	}
}

// An incomplete protected inventory stops both build-cache steps: GOCACHE
// could lie inside a checkout the inventory missed.
func TestBuildCacheStopsOnAnInventoryGap(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	buildCacheFixture(t, tw, map[string]time.Duration{"0a01-a": 100 * time.Hour})
	tw.ProtectedGap = "canonical clones could not be listed"
	tw.categories = []category{buildCacheCategory{}, fullCleanCategory{}}
	report := tw.run(Options{Apply: true, AllowFullClean: true, Categories: []string{CategoryBuild}, Floor: 0.01})
	for _, name := range []string{CategoryBuild, categoryFullClean} {
		if cat := categoryByName(t, report, name); !strings.HasPrefix(cat.Reason, "unknown: canonical clones") {
			t.Errorf("%s = %#v", name, cat)
		}
	}
	if report.Outcome != OutcomeFailClosed {
		t.Fatalf("outcome = %q", report.Outcome)
	}
}

// A scratch entry that is itself a mount is kept before any projection, and
// a file is never opened as an anchor.
func TestMountedScratchEntryAndFileAnchor(t *testing.T) {
	t.Parallel()
	tw := newTestWorld(t)
	tw.entry("go-build1", 8)
	identity := tw.Identity
	tw.Identity = func(path string) (Identity, error) {
		id, err := identity(path)
		if strings.HasSuffix(path, "go-build1") {
			id.MountID++
		}
		return id, err
	}
	if it := itemsByName(tw.run(Options{Floor: 0.01}), CategoryScratch)["go-build1"]; it.Reason != "contains-mount" || it.ProjectedAvailableBytes != nil {
		t.Fatalf("a mounted entry = %#v", it)
	}
	file := filepath.Join(t.TempDir(), "file")
	mustWrite(t, file, "x", 0o644)
	if _, err := RemoveFileBeneath(Anchor{Root: file}, "", "x", Identity{}); err == nil || errors.Is(err, ErrChanged) {
		t.Fatalf("a file as the anchor: %v", err)
	}
	if lines := parseMountinfo("x 1 0:1 / /x rw - y y rw\n"); len(lines) != 0 {
		t.Fatalf("a malformed mount ID was parsed: %v", lines)
	}
}
