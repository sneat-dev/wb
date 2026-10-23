//go:build windows

package unix

import (
	"os"
	"testing"
)

func TestWindowsSyncDirectoryValidatesHandles(t *testing.T) {
	t.Parallel()
	if err := SyncDirectory(nil); err == nil {
		t.Fatal("nil directory handle accepted")
	}
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	if err := SyncDirectory(directory); err != nil {
		t.Fatalf("live directory handle: %v", err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if err := SyncDirectory(directory); err == nil {
		t.Fatal("closed directory handle accepted")
	}
}
