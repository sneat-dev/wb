//go:build windows

package unix

import (
	"os"
	"path/filepath"
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
