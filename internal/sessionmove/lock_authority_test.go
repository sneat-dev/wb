package sessionmove

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestHeldForStoreRejectsRequestFileContentTamperedInPlace drives the
// admitted-content mismatch branch of HeldForStore: the retained request
// file descriptor still names the same inode (so the earlier same-inode
// check passes), but the bytes at that inode no longer decode to the exact
// admitted Request the lock was granted for.
func TestHeldForStoreRejectsRequestFileContentTamperedInPlace(t *testing.T) {
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	root := filepath.Join(t.TempDir(), "handoffs")
	store := NewStore(root)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()

	requestPath := filepath.Join(root, request.HandoffID, requestFileName)
	info, err := os.Stat(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	// Overwrite in place (same inode, same pathname) with bytes that no
	// longer decode to the admitted request, instead of renaming it away
	// (which the existing different-inode test already covers).
	if err := os.WriteFile(requestPath, []byte("not json"), info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if lock.HeldForStore(root, request, digest) {
		t.Fatal("tampered in-place request content was authorized")
	}
}
