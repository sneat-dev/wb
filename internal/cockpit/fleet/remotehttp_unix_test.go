//go:build !windows

package fleet

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestACredentialPathThatIsNotARegularFileIsRefusedWithoutBlocking proves the
// token file rule for the paths that could hang a read: a FIFO and a symbolic
// link to one are refused at once (a FIFO with no writer would block an
// ordinary open for ever), a dangling link is refused, and a link to a regular
// private file is followed.
func TestACredentialPathThatIsNotARegularFileIsRefusedWithoutBlocking(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	fifo := filepath.Join(directory, "fifo.token")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	toFIFO, dangling, toFile, shared := filepath.Join(directory, "to-fifo"), filepath.Join(directory, "dangling"), filepath.Join(directory, "to-file"), filepath.Join(directory, "to-shared")
	regular := tokenFile(t, vmBearer)
	open := filepath.Join(directory, "open.token")
	if err := os.WriteFile(open, []byte(vmBearer), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o644); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{toFIFO: fifo, dangling: filepath.Join(directory, "absent"), toFile: regular, shared: open} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for name, path := range map[string]string{"a FIFO": fifo, "a link to a FIFO": toFIFO, "a dangling link": dangling, "a link to a shared file": shared} {
			if _, err := readBearer(path); !errors.Is(err, errBearer) {
				t.Errorf("%s = %v, want it refused", name, err)
			}
		}
		if token, err := readBearer(toFile); err != nil || token != vmBearer {
			t.Errorf("a link to a private regular file = %q, %v", token, err)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("reading a credential path blocked")
	}
}
