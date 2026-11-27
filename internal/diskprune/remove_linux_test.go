//go:build linux

package diskprune

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func identityOf(t *testing.T, path string) Identity {
	t.Helper()
	identity, err := StatIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

// chmodTreeWritable undoes read-only fixtures so t.TempDir can clean up.
func chmodTreeWritable(t *testing.T, root string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				_ = os.Chmod(path, 0o755)
			}
			return nil
		})
	})
}

// A checked tree is removed completely, read-only directories included, and
// a symlink inside it is removed as a link while its target survives.
func TestRemoveTreeRemovesTheCheckedTree(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "precious"), "keep", 0o644)
	candidate := filepath.Join(parent, "go-build1")
	mustWrite(t, filepath.Join(candidate, "b001", "_pkg_.a"), "archive", 0o444)
	mustWrite(t, filepath.Join(candidate, "mod", "x@v1", "x.go"), "package x", 0o444)
	if err := os.Symlink(outside, filepath.Join(candidate, "link")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(candidate, "mod", "x@v1"), filepath.Join(candidate, "mod"), candidate} {
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	chmodTreeWritable(t, parent)
	outcome, err := RemoveTree(parent, "go-build1", identityOf(t, candidate))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(candidate); !os.IsNotExist(err) {
		t.Fatalf("candidate survived: %v", err)
	}
	if outcome.Files != 3 {
		t.Fatalf("outcome = %#v, want 3 files", outcome)
	}
	if _, err := os.Stat(filepath.Join(outside, "precious")); err != nil {
		t.Fatalf("symlink target lost: %v", err)
	}
}

// A name that now names a symlink, a file, or a different directory is not
// the checked object, and nothing behind it is touched.
func TestRemoveTreeRefusesAChangedName(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	candidate := filepath.Join(parent, "c")
	mustWrite(t, filepath.Join(candidate, "f"), "x", 0o644)
	checked := identityOf(t, candidate)

	target := t.TempDir()
	mustWrite(t, filepath.Join(target, "precious"), "keep", 0o644)
	if err := os.RemoveAll(candidate); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, candidate); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveTree(parent, "c", checked); !errors.Is(err, ErrChanged) {
		t.Fatalf("symlink swap: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "precious")); err != nil {
		t.Fatal("symlink target touched")
	}

	if err := os.Remove(candidate); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, candidate, "now a file", 0o644)
	if _, err := RemoveTree(parent, "c", checked); !errors.Is(err, ErrChanged) {
		t.Fatalf("file swap: %v", err)
	}

	if err := os.Remove(candidate); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(candidate, "new"), "keep", 0o644)
	if _, err := RemoveTree(parent, "c", checked); !errors.Is(err, ErrChanged) {
		t.Fatalf("directory swap: %v", err)
	}
	if _, err := os.Stat(filepath.Join(candidate, "new")); err != nil {
		t.Fatal("replacement directory touched")
	}
}

// A candidate that is itself a mount (a bind alias of protected data has its
// own mount ID), or that holds one, is refused.
func TestRemoveTreeNeverCrossesAMount(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	candidate := filepath.Join(parent, "c")
	mustWrite(t, filepath.Join(candidate, "inner", "f"), "x", 0o644)
	checked := identityOf(t, candidate)

	alias := newTreeRemover()
	calls := 0
	alias.mountIDAt = func(dirfd int, name string) (uint64, error) {
		calls++
		if calls == 1 { // the parent: a candidate on another mount than it is a bind alias
			return checked.MountID + 1, nil
		}
		return statxMountID(dirfd, name)
	}
	if _, err := alias.remove(parent, "c", checked); !errors.Is(err, ErrContainsMount) {
		t.Fatalf("bind alias: %v", err)
	}
	nested := newTreeRemover()
	nestedCalls := 0
	nested.mountIDAt = func(dirfd int, name string) (uint64, error) {
		nestedCalls++
		if nestedCalls == 3 { // the opened "inner" directory
			return checked.MountID + 7, nil
		}
		return statxMountID(dirfd, name)
	}
	if _, err := nested.remove(parent, "c", checked); !errors.Is(err, ErrContainsMount) {
		t.Fatalf("nested mount: %v", err)
	}
	if _, err := os.Stat(filepath.Join(candidate, "inner", "f")); err != nil {
		t.Fatal("mounted subtree touched")
	}
	stale := checked
	stale.MountID++
	if _, err := RemoveTree(parent, "c", stale); !errors.Is(err, ErrContainsMount) {
		t.Fatalf("mount changed since check: %v", err)
	}
}

// A tree moved out of its parent mid-removal is left alone from then on.
func TestRemoveTreeStopsWhenTheTreeMoves(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	candidate := filepath.Join(parent, "c")
	mustWrite(t, filepath.Join(candidate, "inner", "f"), "x", 0o644)
	moved := newTreeRemover()
	moved.fdPath = func(int) (string, error) { return "/home/u/projects/.wb/worklogs", nil }
	if _, err := moved.remove(parent, "c", identityOf(t, candidate)); !errors.Is(err, ErrMoved) {
		t.Fatalf("moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(candidate, "inner", "f")); err != nil {
		t.Fatal("moved subtree touched")
	}
}

// When the checked directory is renamed away and a new one takes its name
// while its contents are removed, the contents go through the descriptor and
// the replacement survives.
func TestRemoveTreeLeavesAReplacementAlone(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	candidate := filepath.Join(parent, "c")
	mustWrite(t, filepath.Join(candidate, "f"), "x", 0o644)
	replaced := newTreeRemover()
	replaced.beforeRemoveTop = func() {
		if err := os.Rename(candidate, filepath.Join(parent, "renamed")); err != nil {
			t.Error(err)
		}
		mustWrite(t, filepath.Join(candidate, "new"), "keep", 0o644)
	}
	if _, err := replaced.remove(parent, "c", identityOf(t, candidate)); !errors.Is(err, ErrReplaced) {
		t.Fatalf("replaced: %v", err)
	}
	if _, err := os.Stat(filepath.Join(candidate, "new")); err != nil {
		t.Fatal("replacement lost")
	}
	if entries, _ := os.ReadDir(filepath.Join(parent, "renamed")); len(entries) != 0 {
		t.Fatalf("original contents survived: %v", entries)
	}
}

// failingSyscalls fails the nth call of one syscall and passes the rest
// through, so every error path is reachable without root.
func failingSyscalls(op string, n int) treeSyscalls {
	sys := realTreeSyscalls()
	failure := errors.New("injected " + op)
	calls := 0
	hit := func() bool { calls++; return calls == n }
	switch op {
	case "openDir":
		sys.openDir = func(string) (int, error) { return -1, failure }
	case "openat":
		real := sys.openat
		sys.openat = func(dirfd int, name string) (int, error) {
			if hit() {
				return -1, failure
			}
			return real(dirfd, name)
		}
	case "fstat":
		real := sys.fstat
		sys.fstat = func(fd int, stat *unix.Stat_t) error {
			if hit() {
				return failure
			}
			return real(fd, stat)
		}
	case "fstatat":
		real := sys.fstatat
		sys.fstatat = func(dirfd int, name string, stat *unix.Stat_t) error {
			if hit() {
				return failure
			}
			return real(dirfd, name, stat)
		}
	case "unlinkat":
		real := sys.unlinkat
		sys.unlinkat = func(dirfd int, name string, flags int) error {
			if hit() {
				return failure
			}
			return real(dirfd, name, flags)
		}
	case "fchmod":
		sys.fchmod = func(int, uint32) error { return failure }
	case "readDir":
		sys.readDir = func(int) ([]string, error) { return nil, failure }
	}
	return sys
}

// Every syscall failure stops the removal with an error and never reports
// success.
func TestRemoveTreeReportsEverySyscallFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		op string
		n  int
	}{
		{"openDir", 1}, {"openat", 1}, {"openat", 2}, {"fstat", 1}, {"fstat", 2},
		{"fstatat", 1}, {"unlinkat", 1}, {"unlinkat", 2}, {"unlinkat", 3},
		{"fchmod", 1}, {"readDir", 1},
	}
	for _, c := range cases {
		parent := t.TempDir()
		candidate := filepath.Join(parent, "c")
		mustWrite(t, filepath.Join(candidate, "sub", "f"), "x", 0o644)
		if c.op == "fchmod" {
			if err := os.Chmod(candidate, 0o555); err != nil {
				t.Fatal(err)
			}
			chmodTreeWritable(t, parent)
		}
		remover := newTreeRemover()
		remover.sys = failingSyscalls(c.op, c.n)
		if _, err := remover.remove(parent, "c", identityOf(t, candidate)); err == nil || errors.Is(err, ErrChanged) && c.op != "fstatat" {
			t.Errorf("%s #%d: err = %v", c.op, c.n, err)
		}
	}
}

// Mount-ID and descriptor-path failures are errors too.
func TestRemoveTreeReportsLookupFailures(t *testing.T) {
	t.Parallel()
	failure := errors.New("injected")
	for name, mutate := range map[string]func(*treeRemover){
		"parent mount": func(r *treeRemover) {
			r.mountIDAt = func(int, string) (uint64, error) { return 0, failure }
		},
		"candidate mount": func(r *treeRemover) { r.mountIDAt = failOnCall(2, failure) },
		"child mount":     func(r *treeRemover) { r.mountIDAt = failOnCall(3, failure) },
		"fd path":         func(r *treeRemover) { r.fdPath = func(int) (string, error) { return "", failure } },
	} {
		parent := t.TempDir()
		mustWrite(t, filepath.Join(parent, "c", "sub", "f"), "x", 0o644)
		remover := newTreeRemover()
		mutate(&remover)
		if _, err := remover.remove(parent, "c", identityOf(t, filepath.Join(parent, "c"))); !errors.Is(err, failure) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := statxMountID(-1, "/nonexistent-dir-for-statx"); err == nil {
		t.Fatal("statx of a missing path succeeded")
	}
	if _, err := StatIdentity(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("identity of a missing path succeeded")
	}
	if _, err := readDirNames(-1); err == nil {
		t.Fatal("reading a bad descriptor succeeded")
	}
}

// A single file is unlinked only while it is still the checked file.
func TestRemoveFileAtChecksIdentity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "0a1b-a")
	mustWrite(t, path, strings.Repeat("x", 8192), 0o644)
	checked := identityOf(t, path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, "replacement", 0o644)
	if _, err := removeIn(dir, "0a1b-a", checked); !errors.Is(err, ErrChanged) {
		t.Fatalf("replaced file: %v", err)
	}
	fresh := identityOf(t, path)
	if bytes, err := removeIn(dir, "0a1b-a", fresh); err != nil || bytes <= 0 {
		t.Fatalf("remove = %d, %v", bytes, err)
	}
	if _, err := removeIn(dir, "0a1b-a", fresh); err == nil {
		t.Fatal("removing a missing file succeeded")
	}
	if _, err := removeIn(filepath.Join(dir, "missing"), "x", fresh); err == nil {
		t.Fatal("missing directory accepted")
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(sub, "f"), "x", 0o644)
	stuck := identityOf(t, filepath.Join(sub, "f"))
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Fatal(err)
	}
	chmodTreeWritable(t, dir)
	if _, err := removeIn(sub, "f", stuck); err == nil || errors.Is(err, ErrChanged) {
		t.Fatalf("unlink in a read-only directory: %v", err)
	}
}

// Before Linux 5.8 statx carries no mount ID: the mount is unknown, never
// guessed from mountinfo, and identity without a mount ID fails.
//
//nolint:paralleltest // replaces the package's statx and mountIDOf seams
func TestMountIDWithoutStatxIsUnknown(t *testing.T) {
	realStatx, realMountIDOf := statx, mountIDOf
	t.Cleanup(func() { statx, mountIDOf = realStatx, realMountIDOf })
	statx = func(dirfd int, path string, flags int, mask int, stx *unix.Statx_t) error {
		err := realStatx(dirfd, path, flags, mask, stx)
		stx.Mask &^= unix.STATX_MNT_ID
		return err
	}
	dir := t.TempDir()
	if _, err := statxMountID(unix.AT_FDCWD, dir); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("no statx mount ID: %v", err)
	}
	if _, err := bindOfSubdirectory(dir); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("bind check without statx mount ID: %v", err)
	}
	statx = func(int, string, int, int, *unix.Statx_t) error { return errors.New("EBADF") }
	if _, err := statxMountID(9999, "rel"); err == nil {
		t.Fatal("a failed statx gave a mount ID")
	}
	mountIDOf = func(string) (uint64, error) { return 0, errors.New("injected") }
	if _, err := StatIdentity(dir); err == nil {
		t.Fatal("identity without a mount id succeeded")
	}
}

// failOnCall fails the nth mount-ID lookup: 1 is the parent, 2 the opened
// candidate, 3 its first opened subdirectory.
func failOnCall(n int, failure error) func(int, string) (uint64, error) {
	calls := 0
	return func(dirfd int, name string) (uint64, error) {
		calls++
		if calls == n {
			return 0, failure
		}
		return statxMountID(dirfd, name)
	}
}
