//go:build !windows

package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

func TestResidueDirectoryListingKeepsCallerOffsetAndClosesTemporaryHandle(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	parent, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	first, err := parent.Readdirnames(1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first entry: %v %v", first, err)
	}
	var held *os.File
	names, err := directoryEntryNamesWithOpener(parent, "diagnostic path", func(directory *os.File, name, path string) (*os.File, error) {
		if directory != parent || name != "." || path != "diagnostic path" {
			t.Fatalf("listing boundary: %v %q %q", directory, name, path)
		}
		var openErr error
		held, openErr = openResidueDirectoryAt(directory, name, path)
		if openErr != nil {
			return nil, openErr
		}
		flags, flagErr := unix.FcntlInt(held.Fd(), unix.F_GETFD, 0)
		if flagErr != nil || flags&unix.FD_CLOEXEC == 0 {
			t.Fatalf("temporary handle lacks close-on-exec: %d %v", flags, flagErr)
		}
		return held, nil
	})
	sort.Strings(names)
	if err != nil || strings.Join(names, ",") != "a,b,c" {
		t.Fatalf("fresh listing: %v %v", names, err)
	}
	if _, err := held.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("listing handle remains open: %v", err)
	}
	rest, err := parent.Readdirnames(-1)
	if err != nil || len(rest) != 2 {
		t.Fatalf("caller offset changed: %v %v", rest, err)
	}
	for _, name := range rest {
		if name == first[0] {
			t.Fatalf("caller rewound: %v + %v", first, rest)
		}
	}
}

func TestResidueDirectoryListingPreservesNativeOpenAndReadErrors(t *testing.T) {
	t.Parallel()
	parent, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	var listing *os.File
	names, err := directoryEntryNamesWithOpener(parent, "read-failure", func(directory *os.File, name, path string) (*os.File, error) {
		var err error
		listing, err = openResidueDirectoryAt(directory, name, path)
		if err == nil {
			err = listing.Close()
		}
		return listing, err
	})
	if names != nil || err == nil || !strings.Contains(err.Error(), "list residue directory read-failure") {
		t.Fatalf("read error lost: %v %v", names, err)
	}
	if _, err := listing.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("failed listing left handle open: %v", err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	if names, err := directoryEntryNames(parent, "closed-parent"); names != nil || err == nil || !strings.Contains(err.Error(), "list residue directory closed-parent") {
		t.Fatalf("open error lost: %v %v", names, err)
	}
}

func TestResidueDirectoryOpenRefusesReplacementAndClosesFailedInspection(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "child")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	var expected unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), "child", &expected, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}
	var held *os.File
	directory, err := openResidueDirectoryWithOpener(parent, path, "child", expected, func(parent *os.File, name, path string) (*os.File, error) {
		var err error
		held, err = openResidueDirectoryAt(parent, name, path)
		if err == nil {
			err = held.Close()
		}
		return held, err
	})
	if directory != nil || err == nil || !strings.Contains(err.Error(), "inspect residue directory") {
		t.Fatalf("closed inspection accepted: %v %v", directory, err)
	}
	if _, err := held.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("failed inspection handle remains open: %v", err)
	}
	if err := os.Rename(path, path+"-retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "marker"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	directory, err = openResidueDirectoryWithOpener(parent, path, "child", expected, func(parent *os.File, name, path string) (*os.File, error) {
		var err error
		held, err = openResidueDirectoryAt(parent, name, path)
		return held, err
	})
	if directory != nil || err == nil || !strings.Contains(err.Error(), "was replaced") {
		t.Fatalf("replacement accepted: %v %v", directory, err)
	}
	if _, err := held.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("replacement handle remains open: %v", err)
	}
	if marker, err := os.ReadFile(filepath.Join(path, "marker")); err != nil || string(marker) != "preserve" {
		t.Fatalf("replacement changed: %q %v", marker, err)
	}
	if directory, err := openResidueDirectory(parent, "absent", "absent", expected); directory != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing child error lost: %v %v", directory, err)
	}
	if err := unix.Fstatat(int(parent.Fd()), "child", &expected, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}
	directory, err = openResidueDirectory(parent, path, "child", expected)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	flags, err := unix.FcntlInt(directory.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("residue child lacks close-on-exec: %d %v", flags, err)
	}
}

func TestResidueDirectoryOpenPreservesPermissionAdviceAndSymlinkRefusal(t *testing.T) {
	t.Parallel()
	requireUnprivilegedResidueTest(t)
	root := t.TempDir()
	path := filepath.Join(root, "child")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	var expected unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), "child", &expected, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0700) })
	if directory, err := openResidueDirectory(parent, path, "child", expected); directory != nil || !errors.Is(err, unix.EACCES) || !strings.Contains(err.Error(), "chmod u+rx "+path) {
		t.Fatalf("permission advice lost: %v %v", directory, err)
	}
	if err := os.Symlink(path, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if directory, err := openResidueDirectory(parent, "link", "link", expected); directory != nil || err == nil {
		t.Fatalf("symlink followed: %v %v", directory, err)
	}
}
