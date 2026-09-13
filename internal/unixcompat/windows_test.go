//go:build windows

package unix

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOpenNoFollowRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("creating symlinks requires Windows developer mode or elevated privileges: %v", err)
	}
	fd, err := Open(link, O_RDONLY|O_DIRECTORY|O_NOFOLLOW, 0)
	if err == nil {
		_ = Close(fd)
		t.Fatal("Open followed a symlink despite O_NOFOLLOW")
	}
}

func TestOpenNoFollowCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "created.lock")
	fd, err := Open(path, O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW, 0o600)
	if err != nil {
		t.Fatalf("create missing file with O_NOFOLLOW: %v", err)
	}
	if err := Close(fd); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("created mode = %v, want regular file", info.Mode())
	}
}

func TestOpenNoFollowTransfersSingleHandleOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned.lock")
	fd, err := Open(path, O_RDWR|O_CREAT|O_EXCL|O_NOFOLLOW, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = Close(fd)
		t.Fatal("wrap transferred Windows handle")
	}
	var stat Stat_t
	if err := Fstat(fd, &stat); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	runtime.GC()
	runtime.Gosched()
	if _, err := file.Stat(); err != nil {
		_ = file.Close()
		t.Fatalf("transferred handle was closed by another os.File owner: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFstatIdentityMatchesFstatat(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "identity.lock")
	if err := os.WriteFile(path, []byte("lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	directoryFD, err := Open(root, O_RDONLY|O_DIRECTORY|O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = Close(directoryFD) }()
	fileFD, err := Openat(directoryFD, filepath.Base(path), O_RDONLY|O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = Close(fileFD) }()

	var opened, named Stat_t
	if err := Fstat(fileFD, &opened); err != nil {
		t.Fatal(err)
	}
	if err := Fstatat(directoryFD, filepath.Base(path), &named, AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}
	if opened.Dev != named.Dev || opened.Ino != named.Ino || opened.Nlink != named.Nlink {
		t.Fatalf("opened identity (%d,%d,%d) != named identity (%d,%d,%d)",
			opened.Dev, opened.Ino, opened.Nlink, named.Dev, named.Ino, named.Nlink)
	}
}
