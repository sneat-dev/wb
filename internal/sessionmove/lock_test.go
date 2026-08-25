package sessionmove

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireExecutionLockBindsExactAdmissionAndStoreIdentity(t *testing.T) {
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
	wrongDigest := DigestBytes(append(append([]byte(nil), raw...), '\n'))
	if _, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, wrongDigest); !errors.Is(err, ErrHandoffConflict) {
		t.Fatalf("wrong digest acquisition error = %v, want handoff conflict", err)
	}

	lock, err := store.AcquireExecutionLock(context.Background(), request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if !lock.HeldForStore(root, request, digest) {
		t.Fatal("exact admitted store authority was not recognized")
	}
	otherRoot := filepath.Join(t.TempDir(), "handoffs")
	otherStore := NewStore(otherRoot)
	if _, err := otherStore.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	if lock.HeldForStore(otherRoot, request, digest) {
		t.Fatal("same handoff and digest in a different store was authorized")
	}
	if lock.HeldForStore(root, request, wrongDigest) {
		t.Fatal("wrong request digest was authorized")
	}
	requestPath := filepath.Join(root, request.HandoffID, requestFileName)
	if err := os.Rename(requestPath, requestPath+".replaced"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if lock.HeldForStore(root, request, digest) {
		t.Fatal("replacement request inode was authorized despite identical bytes")
	}
}
