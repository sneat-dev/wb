//go:build !windows

package unix

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestTailCovSyncDirectoryFlushesAnOpenFile covers the one piece of real
// behavior this package adds on top of the golang.org/x/sys/unix aliases: a
// directory handle opened for the durability step of an atomic rename can be
// synced, and a handle that is no longer valid reports the failure instead of
// pretending the flush happened.
func TestTailCovSyncDirectoryFlushesAnOpenFile(t *testing.T) {
	directory := t.TempDir()

	handle, err := os.Open(directory)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	if err := SyncDirectory(handle); err != nil {
		_ = handle.Close()
		t.Fatalf("SyncDirectory(open directory) = %v, want nil", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close directory: %v", err)
	}

	if err := SyncDirectory(handle); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("SyncDirectory(closed directory) = %v, want os.ErrClosed", err)
	}
}

// TestTailCovSyncDirectoryFlushesFileContent proves the helper is not a no-op
// on the file case an atomic writer actually uses before renaming it into
// place: the bytes are visible to an independent reader after the sync.
func TestTailCovSyncDirectoryFlushesFileContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "staged.tmp")
	handle, err := os.OpenFile(path, O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW, 0o600)
	if err != nil {
		t.Fatalf("create staged file: %v", err)
	}
	if _, err := handle.WriteString("staged payload"); err != nil {
		_ = handle.Close()
		t.Fatalf("write staged file: %v", err)
	}
	if err := SyncDirectory(handle); err != nil {
		_ = handle.Close()
		t.Fatalf("SyncDirectory(staged file) = %v, want nil", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close staged file: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged file: %v", err)
	}
	if string(raw) != "staged payload" {
		t.Fatalf("staged file = %q, want the written payload", raw)
	}
}
