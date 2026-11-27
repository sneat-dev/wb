//go:build linux

package diskprune

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func scanOf(t *testing.T, root string, protected ...string) scanResult {
	t.Helper()
	return scanTree(root, identityOf(t, root).MountID, protected)
}

// Age comes from files' modification and status-change times, so a tree
// copied with old modification times is still young.
func TestScanAgeUsesFileStatusChangeTimes(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "go-build1")
	mustWrite(t, filepath.Join(root, "b001", "x.a"), "x", 0o644)
	old := time.Now().Add(-96 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "b001", "x.a"), old, old); err != nil {
		t.Fatal(err)
	}
	result := scanOf(t, root)
	if time.Since(result.Newest) > time.Minute {
		t.Fatalf("newest = %v; the status-change time of a just-copied file must count", result.Newest)
	}
	if result.Files != 1 || len(result.Inodes) != 1 || result.Partial || result.HasGit || result.HasSpecial || result.CacheData {
		t.Fatalf("result = %#v", result)
	}
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(filepath.Join(empty, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if result := scanOf(t, empty); result.Newest.IsZero() || result.Files != 0 {
		t.Fatalf("an empty tree's age comes from its directories: %#v", result)
	}
}

// Checkouts, live runtime directories and cache data are found at any depth.
func TestScanFindsWhatMustBeKept(t *testing.T) {
	t.Parallel()
	gitFile := filepath.Join(t.TempDir(), "wb-wt-1")
	mustWrite(t, filepath.Join(gitFile, "repo", ".git"), "gitdir: /x", 0o644)
	if !scanOf(t, gitFile).HasGit {
		t.Fatal(".git file missed")
	}
	gitDir := filepath.Join(t.TempDir(), "wb-index-1")
	mustWrite(t, filepath.Join(gitDir, "repo", ".git", "HEAD"), "ref", 0o644)
	if !scanOf(t, gitDir).HasGit {
		t.Fatal(".git directory missed")
	}
	sockDir := filepath.Join(os.TempDir(), "wbsock"+time.Now().Format("150405.000000000"))
	if err := os.MkdirAll(sockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	listener, err := net.Listen("unix", filepath.Join(sockDir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if !scanOf(t, sockDir).HasSpecial {
		t.Fatal("socket missed")
	}
	modCache := filepath.Join(t.TempDir(), "wb-ci-cache")
	mustWrite(t, filepath.Join(modCache, "mod", "cache", "download", "x", "@v", "list"), "", 0o644)
	if !scanOf(t, modCache).CacheData {
		t.Fatal("module cache missed")
	}
	fuzz := filepath.Join(t.TempDir(), "wb-fz-go-cache")
	mustWrite(t, filepath.Join(fuzz, "fuzz", "pkg", "FuzzX", "corpus"), "x", 0o644)
	if err := os.MkdirAll(filepath.Join(fuzz, "ab"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !scanOf(t, fuzz).CacheData {
		t.Fatal("fuzz corpus missed")
	}
	emptyFuzz := filepath.Join(t.TempDir(), "c")
	if err := os.MkdirAll(filepath.Join(emptyFuzz, "fuzz"), 0o755); err != nil {
		t.Fatal(err)
	}
	if scanOf(t, emptyFuzz).CacheData {
		t.Fatal("an empty fuzz directory is not cache data")
	}
}

// A protected root inside the tree is reported and not descended; an
// unreadable entry or a nested mount makes the scan partial or mounted.
func TestScanReportsProtectedMountsAndGaps(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "go-build1")
	clone := filepath.Join(root, "acme", "app")
	mustWrite(t, filepath.Join(clone, "main.go"), "package main", 0o644)
	if hit := scanOf(t, root, clone).Protected; hit != clone {
		t.Fatalf("protected = %q", hit)
	}
	if hit := scanOf(t, root, filepath.Dir(root)).Protected; hit != "" {
		t.Fatalf("a protected parent is the caller's check, got %q", hit)
	}

	locked := filepath.Join(t.TempDir(), "go-build2")
	mustWrite(t, filepath.Join(locked, "sub", "f"), "x", 0o644)
	if err := os.Chmod(filepath.Join(locked, "sub"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(locked, "sub"), 0o755) })
	if os.Geteuid() != 0 && !scanOf(t, locked).Partial {
		t.Fatal("unreadable subtree not reported")
	}

	mounted := filepath.Join(t.TempDir(), "go-build3")
	mustWrite(t, filepath.Join(mounted, "m", "f"), "x", 0o644)
	seams := realScanSeams()
	base := identityOf(t, mounted).MountID
	seams.mountID = func(path string) (uint64, error) {
		if filepath.Base(path) == "m" {
			return base + 1, nil
		}
		return mountIDOf(path)
	}
	if !seams.scan(mounted, base, nil).ContainsMount {
		t.Fatal("nested mount missed")
	}
	seams.mountID = func(string) (uint64, error) { return 0, errors.New("injected") }
	if !seams.scan(mounted, base, nil).Partial {
		t.Fatal("a mount lookup failure must make the scan partial")
	}
	seams = realScanSeams()
	seams.lstat = func(string, *unix.Stat_t) error { return errors.New("injected") }
	if !seams.scan(mounted, base, nil).Partial {
		t.Fatal("a stat failure must make the scan partial")
	}
	missing := realScanSeams().scan(filepath.Join(t.TempDir(), "absent"), 0, nil)
	if !missing.Partial {
		t.Fatal("a missing root must be partial")
	}
}

// Selected-prefix accounting: a file whose two links are split across two
// candidates comes back with the second, and a file linked from outside never
// comes back.
func TestReclaimAccumulatorCountsLastLinks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, b, outside := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "outside")
	mustWrite(t, filepath.Join(a, "shared"), "0123456789", 0o644)
	mustWrite(t, filepath.Join(a, "external"), "0123456789", 0o644)
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(a, "shared"), filepath.Join(b, "shared")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(a, "external"), filepath.Join(outside, "external")); err != nil {
		t.Fatal(err)
	}
	accumulator := newReclaimAccumulator()
	if got := accumulator.add(scanOf(t, a).Inodes); got != 0 {
		t.Fatalf("a alone releases %d, want 0", got)
	}
	if got := accumulator.add(scanOf(t, b).Inodes); got <= 0 {
		t.Fatalf("a then b releases %d, want the shared file's blocks", got)
	}
	if got := accumulator.add(scanOf(t, b).Inodes); got != 0 {
		t.Fatalf("a file is released once, got %d", got)
	}
	if insideAny("/tmp/x", []string{"", "/home"}) != "" || insideAny("/home/u/.wb/x", []string{"/home/u/.wb"}) != "/home/u/.wb" || insideAny("/tmp", []string{"/tmp/c/.git"}) != "/tmp/c/.git" {
		t.Fatal("insideAny")
	}
}
