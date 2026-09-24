package sessionmove

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// TestAcquireExecutionLockWaitsThenReportsContextCancellation drives the
// ctx.Done() branch of AcquireExecutionLock's retry loop: a held lock keeps
// every later acquisition attempt blocked on EWOULDBLOCK until the caller's
// context is cancelled.
func TestAcquireExecutionLockWaitsThenReportsContextCancellation(t *testing.T) {
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := DigestBytes(raw)
	store := NewStore(filepath.Join(t.TempDir(), "handoffs"))
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	held, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = store.AcquireExecutionLock(ctx, request.HandoffID, digest)
	if err == nil {
		t.Fatal("acquired a lock that another holder still retains")
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("returned after %s, want at least one retry wait before giving up", elapsed)
	}
}
