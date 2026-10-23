package sessionpark

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestResumeLocksPreserveNativeLockRefusals(t *testing.T) {
	t.Parallel()
	t.Run("source", func(t *testing.T) {
		t.Parallel()
		store, bundle := spCovCreatedStore(t)
		lock, err := store.acquireWithFlock(context.Background(), bundle.ParkedSessionID, func(fd, flags int) error { return syscall.EBADF })
		if lock != nil || !errors.Is(err, syscall.EBADF) {
			t.Fatalf("acquire = %v, %v", lock, err)
		}
	})
	t.Run("target", func(t *testing.T) {
		t.Parallel()
		store, admission := spCovTargetFixture(t)
		lock, err := store.acquireWithFlock(context.Background(), admission.Envelope.Request.ResumeID, admission.Digest, func(fd, flags int) error { return syscall.EBADF })
		if lock != nil || !errors.Is(err, syscall.EBADF) {
			t.Fatalf("acquire = %v, %v", lock, err)
		}
	})
}

func TestRetainedAuthorityPreservesDuplicateRefusal(t *testing.T) {
	t.Parallel()
	t.Run("source", func(t *testing.T) {
		t.Parallel()
		store, bundle := spCovCreatedStore(t)
		lock := spCovAcquire(t, store, bundle.ParkedSessionID)
		raw, err := EncodeBundle(bundle)
		if err != nil {
			t.Fatal(err)
		}
		digest := string(sessionmove.DigestBytes(raw))
		f, err := lock.retainSessionDir(store.Root, bundle.ParkedSessionID, digest, func(fd int) (int, error) { return -1, syscall.EMFILE })
		if f != nil || !errors.Is(err, syscall.EMFILE) || !lock.HeldForSession(store.Root, bundle.ParkedSessionID, digest) {
			t.Fatalf("retain = %v, %v", f, err)
		}
	})
	t.Run("target", func(t *testing.T) {
		t.Parallel()
		store, admission := spCovTargetFixture(t)
		lock := spCovTargetLock(t, store, admission)
		f, err := lock.retainSessionDir(store.Root, admission.Envelope.Request.ResumeID, string(admission.Digest), func(fd int) (int, error) { return -1, syscall.EMFILE })
		if f != nil || !errors.Is(err, syscall.EMFILE) || !lock.HeldForSession(store.Root, admission.Envelope.Request.ResumeID, string(admission.Digest)) {
			t.Fatalf("retain = %v, %v", f, err)
		}
	})
}

func TestEventReadersRejectClosedDescriptors(t *testing.T) {
	t.Parallel()
	f, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := listSourceEventsAt(f, "park-test"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("source events = %v", err)
	}
	if _, err := listTargetEventsAt(f, "resume-test"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("target events = %v", err)
	}
}

func TestEventReadersRejectRegularFiles(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if _, err := listSourceEventsAt(f, "park-test"); err == nil {
		t.Fatal("source accepted regular file as events")
	}
	if _, err := listTargetEventsAt(f, "resume-test"); err == nil {
		t.Fatal("target accepted regular file as events")
	}
}
