package sessionmove

// Coverage tests for lock.go. These are hermetic: every store root comes from
// t.TempDir(), nothing touches the network or ambient environment, and no test
// sleeps on an arbitrary wall-clock interval to synchronize.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// smCovLockFixture admits one valid request into a fresh store so tests can
// exercise the exact-admission authority paths of lock.go.
type smCovLockFixture struct {
	store   Store
	root    string
	request Request
	digest  Digest
	raw     []byte
}

func smCovNewLockFixture(t *testing.T) smCovLockFixture {
	t.Helper()
	request := validRequest()
	raw, err := EncodeRequest(request)
	if err != nil {
		t.Fatalf("encode valid request: %v", err)
	}
	digest := DigestBytes(raw)
	root := filepath.Join(t.TempDir(), "handoffs")
	store := NewStore(root)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatalf("admit valid request into %s: %v", root, err)
	}
	return smCovLockFixture{store: store, root: root, request: request, digest: digest, raw: raw}
}

func (f smCovLockFixture) smCovAcquire(t *testing.T) *ExecutionLock {
	t.Helper()
	lock, err := f.store.AcquireExecutionLock(context.Background(), f.request.HandoffID, f.digest)
	if err != nil {
		t.Fatalf("acquire execution lock for %s in %s: %v", f.request.HandoffID, f.root, err)
	}
	return lock
}

func smCovHandoffDir(f smCovLockFixture) string {
	return filepath.Join(f.root, f.request.HandoffID)
}

// smCovOpenRequest opens requestFileName inside dir for readAdmittedRequestFile.
func smCovOpenRequest(t *testing.T, dir string) *os.File {
	t.Helper()
	file, err := os.Open(filepath.Join(dir, requestFileName))
	if err != nil {
		t.Fatalf("open request file in %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

// smCovWriteRequest writes raw as requestFileName in a fresh directory and
// returns that directory.
func smCovWriteRequest(t *testing.T, raw []byte, mode os.FileMode) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, requestFileName)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write request file %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod request file %s: %v", path, err)
	}
	return dir
}

func TestSmCovLockHeldForSessionRequiresExactAggregateAndDigest(t *testing.T) {
	fixture := smCovNewLockFixture(t)
	lock := fixture.smCovAcquire(t)
	defer func() { _ = lock.Close() }()

	var nilLock *ExecutionLock
	if nilLock.HeldForSession(fixture.root, fixture.request.HandoffID, string(fixture.digest)) {
		t.Fatalf("nil execution lock claimed a held session fence")
	}
	if !lock.HeldForSession(fixture.root, fixture.request.HandoffID, string(fixture.digest)) {
		t.Fatalf("held execution lock denied its own session aggregate")
	}
	if lock.HeldForSession(fixture.root, "handoff-other", string(fixture.digest)) {
		t.Fatalf("wrong aggregate id was authorized for the held session fence")
	}
	otherDigest := DigestBytes([]byte("some other request bytes"))
	if lock.HeldForSession(fixture.root, fixture.request.HandoffID, string(otherDigest)) {
		t.Fatalf("wrong digest was authorized for the held session fence")
	}

	otherRoot := filepath.Join(t.TempDir(), "handoffs")
	otherStore := NewStore(otherRoot)
	if _, err := otherStore.Admit(fixture.raw, fixture.digest); err != nil {
		t.Fatalf("admit into second store %s: %v", otherRoot, err)
	}
	if lock.HeldForSession(otherRoot, fixture.request.HandoffID, string(fixture.digest)) {
		t.Fatalf("held fence authorized a different store root %s", otherRoot)
	}
}

func TestSmCovLockRetainSessionDirRequiresExactBinding(t *testing.T) {
	fixture := smCovNewLockFixture(t)
	lock := fixture.smCovAcquire(t)
	defer func() { _ = lock.Close() }()

	var nilLock *ExecutionLock
	if retained, err := nilLock.RetainSessionDir(fixture.root, fixture.request.HandoffID, string(fixture.digest)); err == nil {
		_ = retained.Close()
		t.Fatalf("nil execution lock returned a session directory")
	}

	otherDigest := DigestBytes([]byte("some other request bytes"))
	tests := []struct {
		name        string
		aggregateID string
		digest      Digest
	}{
		{name: "wrong aggregate", aggregateID: "handoff-other", digest: fixture.digest},
		{name: "wrong digest", aggregateID: fixture.request.HandoffID, digest: otherDigest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			retained, err := lock.RetainSessionDir(fixture.root, test.aggregateID, string(test.digest))
			if err == nil {
				_ = retained.Close()
				t.Fatalf("RetainSessionDir(%q, %s) succeeded, want binding mismatch", test.aggregateID, test.digest)
			}
			if !strings.Contains(err.Error(), "does not bind the requested session aggregate") {
				t.Fatalf("RetainSessionDir error = %v, want binding mismatch", err)
			}
		})
	}

	retained, err := lock.RetainSessionDir(fixture.root, fixture.request.HandoffID, string(fixture.digest))
	if err != nil {
		t.Fatalf("RetainSessionDir on the bound aggregate: %v", err)
	}
	defer func() { _ = retained.Close() }()
	wantInfo, err := os.Stat(smCovHandoffDir(fixture))
	if err != nil {
		t.Fatalf("stat handoff directory %s: %v", smCovHandoffDir(fixture), err)
	}
	gotInfo, err := retained.Stat()
	if err != nil {
		t.Fatalf("stat retained session directory: %v", err)
	}
	if !os.SameFile(wantInfo, gotInfo) {
		t.Fatalf("retained session directory names a different inode than %s", smCovHandoffDir(fixture))
	}
}

func TestSmCovLockHeldForStoreRejectsUnboundAuthority(t *testing.T) {
	t.Run("nil lock", func(t *testing.T) {
		fixture := smCovNewLockFixture(t)
		var lock *ExecutionLock
		if lock.HeldForStore(fixture.root, fixture.request, fixture.digest) {
			t.Fatalf("nil execution lock claimed authority for %s", fixture.root)
		}
	})

	t.Run("empty expected root", func(t *testing.T) {
		fixture := smCovNewLockFixture(t)
		lock := fixture.smCovAcquire(t)
		defer func() { _ = lock.Close() }()
		if lock.HeldForStore("", fixture.request, fixture.digest) {
			t.Fatalf("empty expected root was authorized")
		}
	})

	t.Run("padded expected root", func(t *testing.T) {
		fixture := smCovNewLockFixture(t)
		lock := fixture.smCovAcquire(t)
		defer func() { _ = lock.Close() }()
		if lock.HeldForStore(" "+fixture.root, fixture.request, fixture.digest) {
			t.Fatalf("whitespace-padded expected root was authorized")
		}
	})

	t.Run("different store root", func(t *testing.T) {
		fixture := smCovNewLockFixture(t)
		lock := fixture.smCovAcquire(t)
		defer func() { _ = lock.Close() }()
		otherRoot := filepath.Join(t.TempDir(), "handoffs")
		otherStore := NewStore(otherRoot)
		if _, err := otherStore.Admit(fixture.raw, fixture.digest); err != nil {
			t.Fatalf("admit into second store: %v", err)
		}
		if lock.HeldForStore(otherRoot, fixture.request, fixture.digest) {
			t.Fatalf("authority was granted for a different store root %s", otherRoot)
		}
	})

	t.Run("different request projection", func(t *testing.T) {
		fixture := smCovNewLockFixture(t)
		lock := fixture.smCovAcquire(t)
		defer func() { _ = lock.Close() }()
		changed := fixture.request
		changed.Branch = fixture.request.Branch + "-changed"
		if lock.HeldForStore(fixture.root, changed, fixture.digest) {
			t.Fatalf("authority was granted for a changed request projection")
		}
	})

	t.Run("different validating digest", func(t *testing.T) {
		fixture := smCovNewLockFixture(t)
		lock := fixture.smCovAcquire(t)
		defer func() { _ = lock.Close() }()
		otherDigest := DigestBytes([]byte("some other request bytes"))
		if err := otherDigest.validate(); err != nil {
			t.Fatalf("other digest is not well formed: %v", err)
		}
		if lock.HeldForStore(fixture.root, fixture.request, otherDigest) {
			t.Fatalf("authority was granted for a different digest %s", otherDigest)
		}
	})

	t.Run("different handoff id", func(t *testing.T) {
		fixture := smCovNewLockFixture(t)
		lock := fixture.smCovAcquire(t)
		defer func() { _ = lock.Close() }()
		changed := fixture.request
		changed.HandoffID = "handoff-other"
		if lock.HeldForStore(fixture.root, changed, fixture.digest) {
			t.Fatalf("authority was granted for handoff id %q", changed.HandoffID)
		}
	})

	t.Run("replacement request inode", func(t *testing.T) {
		fixture := smCovNewLockFixture(t)
		lock := fixture.smCovAcquire(t)
		defer func() { _ = lock.Close() }()
		requestPath := filepath.Join(smCovHandoffDir(fixture), requestFileName)
		if err := os.Rename(requestPath, requestPath+".replaced"); err != nil {
			t.Fatalf("rename admitted request: %v", err)
		}
		if err := os.WriteFile(requestPath, fixture.raw, 0o600); err != nil {
			t.Fatalf("rewrite admitted request bytes: %v", err)
		}
		if lock.HeldForStore(fixture.root, fixture.request, fixture.digest) {
			t.Fatalf("authority was granted to a replacement request inode with identical bytes")
		}
	})

	t.Run("missing request file", func(t *testing.T) {
		fixture := smCovNewLockFixture(t)
		lock := fixture.smCovAcquire(t)
		defer func() { _ = lock.Close() }()
		if err := os.Remove(filepath.Join(smCovHandoffDir(fixture), requestFileName)); err != nil {
			t.Fatalf("remove admitted request: %v", err)
		}
		if lock.HeldForStore(fixture.root, fixture.request, fixture.digest) {
			t.Fatalf("authority was granted after the admitted request disappeared")
		}
	})

	t.Run("renamed handoff directory", func(t *testing.T) {
		fixture := smCovNewLockFixture(t)
		lock := fixture.smCovAcquire(t)
		defer func() { _ = lock.Close() }()
		if err := os.Rename(smCovHandoffDir(fixture), smCovHandoffDir(fixture)+"-moved"); err != nil {
			t.Fatalf("rename handoff directory: %v", err)
		}
		if lock.HeldForStore(fixture.root, fixture.request, fixture.digest) {
			t.Fatalf("authority was granted after the handoff directory was renamed away")
		}
	})

	t.Run("renamed execution lock entry", func(t *testing.T) {
		fixture := smCovNewLockFixture(t)
		lock := fixture.smCovAcquire(t)
		defer func() { _ = lock.Close() }()
		lockPath := filepath.Join(smCovHandoffDir(fixture), executionLockFileName)
		if err := os.Rename(lockPath, lockPath+".moved"); err != nil {
			t.Fatalf("rename execution lock entry: %v", err)
		}
		if lock.HeldForStore(fixture.root, fixture.request, fixture.digest) {
			t.Fatalf("authority was granted after the stable lock entry was renamed away")
		}
	})

	t.Run("removed store root", func(t *testing.T) {
		fixture := smCovNewLockFixture(t)
		lock := fixture.smCovAcquire(t)
		defer func() { _ = lock.Close() }()
		if err := os.RemoveAll(fixture.root); err != nil {
			t.Fatalf("remove store root %s: %v", fixture.root, err)
		}
		if lock.HeldForStore(fixture.root, fixture.request, fixture.digest) {
			t.Fatalf("authority was granted after the store root was removed")
		}
	})
}

func TestSmCovLockHeldForStoreRejectsSwappedDirectoryIdentity(t *testing.T) {
	t.Run("swapped store root", func(t *testing.T) {
		fixture := smCovNewLockFixture(t)
		lock := fixture.smCovAcquire(t)
		defer func() { _ = lock.Close() }()
		if err := os.Rename(fixture.root, fixture.root+"-moved"); err != nil {
			t.Fatalf("rename store root %s: %v", fixture.root, err)
		}
		if err := os.Mkdir(fixture.root, 0o700); err != nil {
			t.Fatalf("recreate store root %s: %v", fixture.root, err)
		}
		if lock.HeldForStore(fixture.root, fixture.request, fixture.digest) {
			t.Fatalf("authority was granted to a replacement store root inode at the same path")
		}
	})

	t.Run("swapped handoff directory", func(t *testing.T) {
		fixture := smCovNewLockFixture(t)
		lock := fixture.smCovAcquire(t)
		defer func() { _ = lock.Close() }()
		handoffDir := smCovHandoffDir(fixture)
		if err := os.Rename(handoffDir, handoffDir+"-moved"); err != nil {
			t.Fatalf("rename handoff directory %s: %v", handoffDir, err)
		}
		if err := os.Mkdir(handoffDir, 0o700); err != nil {
			t.Fatalf("recreate handoff directory %s: %v", handoffDir, err)
		}
		if lock.HeldForStore(fixture.root, fixture.request, fixture.digest) {
			t.Fatalf("authority was granted to a replacement handoff directory inode at the same path")
		}
	})
}

func TestSmCovLockRetainHandoffForStoreRequiresExactBinding(t *testing.T) {
	fixture := smCovNewLockFixture(t)
	lock := fixture.smCovAcquire(t)

	var nilLock *ExecutionLock
	if retained, err := nilLock.RetainHandoffForStore(fixture.root, fixture.request, fixture.digest); err == nil {
		_ = retained.Close()
		t.Fatalf("nil execution lock returned a retained handoff directory")
	}

	otherRoot := filepath.Join(t.TempDir(), "handoffs")
	changed := fixture.request
	changed.Branch = fixture.request.Branch + "-changed"
	otherDigest := DigestBytes([]byte("some other request bytes"))
	tests := []struct {
		name    string
		root    string
		request Request
		digest  Digest
	}{
		{name: "wrong root", root: otherRoot, request: fixture.request, digest: fixture.digest},
		{name: "changed request", root: fixture.root, request: changed, digest: fixture.digest},
		{name: "wrong digest", root: fixture.root, request: fixture.request, digest: otherDigest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			retained, err := lock.RetainHandoffForStore(test.root, test.request, test.digest)
			if err == nil {
				_ = retained.Close()
				t.Fatalf("RetainHandoffForStore(%s) succeeded, want missing authority", test.root)
			}
			if !strings.Contains(err.Error(), "does not retain the exact admitted handoff directory") {
				t.Fatalf("RetainHandoffForStore error = %v, want missing authority", err)
			}
		})
	}

	retained, err := lock.RetainHandoffForStore(fixture.root, fixture.request, fixture.digest)
	if err != nil {
		t.Fatalf("RetainHandoffForStore on the held fence: %v", err)
	}
	wantInfo, err := os.Stat(smCovHandoffDir(fixture))
	if err != nil {
		t.Fatalf("stat handoff directory: %v", err)
	}
	gotInfo, err := retained.Stat()
	if err != nil {
		t.Fatalf("stat retained handoff directory: %v", err)
	}
	if !os.SameFile(wantInfo, gotInfo) {
		t.Fatalf("retained handoff descriptor names a different inode than %s", smCovHandoffDir(fixture))
	}
	if err := retained.Close(); err != nil {
		t.Fatalf("close retained handoff descriptor: %v", err)
	}

	if err := lock.Close(); err != nil {
		t.Fatalf("close execution lock: %v", err)
	}
	if retained, err := lock.RetainHandoffForStore(fixture.root, fixture.request, fixture.digest); err == nil {
		_ = retained.Close()
		t.Fatalf("closed execution lock retained a handoff directory")
	} else if !strings.Contains(err.Error(), "does not retain the exact admitted handoff directory") {
		t.Fatalf("closed execution lock error = %v, want missing authority", err)
	}
}

func TestSmCovLockRetainStoreRootForStoreRequiresExactBinding(t *testing.T) {
	fixture := smCovNewLockFixture(t)
	lock := fixture.smCovAcquire(t)

	var nilLock *ExecutionLock
	if retained, err := nilLock.RetainStoreRootForStore(fixture.root, fixture.request, fixture.digest); err == nil {
		_ = retained.Close()
		t.Fatalf("nil execution lock returned a retained store root")
	}

	otherRoot := filepath.Join(t.TempDir(), "handoffs")
	changed := fixture.request
	changed.Branch = fixture.request.Branch + "-changed"
	otherDigest := DigestBytes([]byte("some other request bytes"))
	tests := []struct {
		name    string
		root    string
		request Request
		digest  Digest
	}{
		{name: "wrong root", root: otherRoot, request: fixture.request, digest: fixture.digest},
		{name: "changed request", root: fixture.root, request: changed, digest: fixture.digest},
		{name: "wrong digest", root: fixture.root, request: fixture.request, digest: otherDigest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			retained, err := lock.RetainStoreRootForStore(test.root, test.request, test.digest)
			if err == nil {
				_ = retained.Close()
				t.Fatalf("RetainStoreRootForStore(%s) succeeded, want missing authority", test.root)
			}
			if !strings.Contains(err.Error(), "does not retain the exact admitted handoff store root") {
				t.Fatalf("RetainStoreRootForStore error = %v, want missing authority", err)
			}
		})
	}

	retained, err := lock.RetainStoreRootForStore(fixture.root, fixture.request, fixture.digest)
	if err != nil {
		t.Fatalf("RetainStoreRootForStore on the held fence: %v", err)
	}
	wantInfo, err := os.Stat(fixture.root)
	if err != nil {
		t.Fatalf("stat store root %s: %v", fixture.root, err)
	}
	gotInfo, err := retained.Stat()
	if err != nil {
		t.Fatalf("stat retained store root: %v", err)
	}
	if !os.SameFile(wantInfo, gotInfo) {
		t.Fatalf("retained store-root descriptor names a different inode than %s", fixture.root)
	}
	if err := retained.Close(); err != nil {
		t.Fatalf("close retained store-root descriptor: %v", err)
	}

	if err := lock.Close(); err != nil {
		t.Fatalf("close execution lock: %v", err)
	}
	if retained, err := lock.RetainStoreRootForStore(fixture.root, fixture.request, fixture.digest); err == nil {
		_ = retained.Close()
		t.Fatalf("closed execution lock retained a store root")
	} else if !strings.Contains(err.Error(), "does not retain the exact admitted handoff store root") {
		t.Fatalf("closed execution lock error = %v, want missing authority", err)
	}
}

func TestSmCovLockAcquireRejectsInvalidArguments(t *testing.T) {
	fixture := smCovNewLockFixture(t)
	regularFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(regularFile, []byte("regular file"), 0o600); err != nil {
		t.Fatalf("create regular file root %s: %v", regularFile, err)
	}

	tests := []struct {
		name      string
		store     Store
		handoffID string
		digest    Digest
		want      string
	}{
		{
			name:      "invalid handoff id",
			store:     fixture.store,
			handoffID: "not a handoff id",
			digest:    fixture.digest,
			want:      "handoff_id",
		},
		{
			name:      "non sha256 digest spelling",
			store:     fixture.store,
			handoffID: fixture.request.HandoffID,
			digest:    Digest("md5:0123456789abcdef"),
			want:      "request digest",
		},
		{
			name:      "empty root",
			store:     NewStore(""),
			handoffID: fixture.request.HandoffID,
			digest:    fixture.digest,
			want:      "handoff store root is required",
		},
		{
			name:      "padded root",
			store:     Store{Root: "\t" + fixture.root},
			handoffID: fixture.request.HandoffID,
			digest:    fixture.digest,
			want:      "handoff store root is required",
		},
		{
			name:      "missing root",
			store:     NewStore(filepath.Join(t.TempDir(), "missing")),
			handoffID: fixture.request.HandoffID,
			digest:    fixture.digest,
			want:      "open admitted handoff store root",
		},
		{
			name:      "regular file root",
			store:     NewStore(regularFile),
			handoffID: fixture.request.HandoffID,
			digest:    fixture.digest,
			want:      "open admitted handoff store root",
		},
		{
			name:      "missing handoff directory",
			store:     NewStore(t.TempDir()),
			handoffID: fixture.request.HandoffID,
			digest:    fixture.digest,
			want:      "open admitted handoff execution directory",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lock, err := test.store.AcquireExecutionLock(context.Background(), test.handoffID, test.digest)
			if err == nil {
				_ = lock.Close()
				t.Fatalf("AcquireExecutionLock(%q, %s) succeeded, want error containing %q", test.handoffID, test.digest, test.want)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("AcquireExecutionLock error = %v, want error containing %q", err, test.want)
			}
		})
	}
}

func TestSmCovLockAcquireRejectsUnstableExecutionLockEntry(t *testing.T) {
	t.Run("lock entry is a directory", func(t *testing.T) {
		fixture := smCovNewLockFixture(t)
		lockPath := filepath.Join(smCovHandoffDir(fixture), executionLockFileName)
		if err := os.Mkdir(lockPath, 0o700); err != nil {
			t.Fatalf("create directory lock entry %s: %v", lockPath, err)
		}
		lock, err := fixture.store.AcquireExecutionLock(context.Background(), fixture.request.HandoffID, fixture.digest)
		if err == nil {
			_ = lock.Close()
			t.Fatalf("acquire succeeded with a directory in place of the execution lock")
		}
		if !strings.Contains(err.Error(), "open handoff execution lock") {
			t.Fatalf("AcquireExecutionLock error = %v, want open handoff execution lock", err)
		}
	})

	t.Run("lock entry has multiple links", func(t *testing.T) {
		fixture := smCovNewLockFixture(t)
		dir := smCovHandoffDir(fixture)
		source := filepath.Join(dir, "linked-source")
		if err := os.WriteFile(source, []byte("linked"), 0o600); err != nil {
			t.Fatalf("create link source %s: %v", source, err)
		}
		if err := os.Link(source, filepath.Join(dir, executionLockFileName)); err != nil {
			t.Fatalf("hard link execution lock entry: %v", err)
		}
		lock, err := fixture.store.AcquireExecutionLock(context.Background(), fixture.request.HandoffID, fixture.digest)
		if err == nil {
			_ = lock.Close()
			t.Fatalf("acquire succeeded with a multi-link execution lock")
		}
		if !strings.Contains(err.Error(), "handoff execution lock is not one regular file") {
			t.Fatalf("AcquireExecutionLock error = %v, want not one regular file", err)
		}
	})
}

func TestSmCovLockAcquireRejectsCorruptedAdmittedRequest(t *testing.T) {
	fixture := smCovNewLockFixture(t)
	requestPath := filepath.Join(smCovHandoffDir(fixture), requestFileName)
	if err := os.Remove(requestPath); err != nil {
		t.Fatalf("remove admitted request %s: %v", requestPath, err)
	}
	if err := os.WriteFile(requestPath, []byte("{corrupted"), 0o600); err != nil {
		t.Fatalf("corrupt admitted request %s: %v", requestPath, err)
	}
	lock, err := fixture.store.AcquireExecutionLock(context.Background(), fixture.request.HandoffID, fixture.digest)
	if err == nil {
		_ = lock.Close()
		t.Fatalf("acquire succeeded with a corrupted admitted request")
	}
	if !strings.Contains(err.Error(), "decode admitted handoff request") {
		t.Fatalf("AcquireExecutionLock error = %v, want decode admitted handoff request", err)
	}
}

func TestSmCovLockAcquireRejectsUnresolvableRoot(t *testing.T) {
	fixture := smCovNewLockFixture(t)
	if runtime.GOOS == "windows" {
		// A process cannot remove its own working directory on Windows, so the
		// unresolvable-root branch is not observable there.
		t.Log("windows: removing the process working directory is not possible")
		return
	}
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove working directory %s: %v", dir, err)
	}
	lock, err := NewStore(".").AcquireExecutionLock(context.Background(), fixture.request.HandoffID, fixture.digest)
	if err == nil {
		_ = lock.Close()
		t.Fatalf("acquire succeeded with an unresolvable relative store root")
	}
	// Linux getcwd(2) fails for an unlinked working directory, so filepath.Abs
	// returns an error; Darwin resolves the retained vnode path and the call
	// instead fails when the removed store root is opened.
	if _, getwdErr := os.Getwd(); getwdErr != nil {
		if !strings.Contains(err.Error(), "resolve handoff store root") {
			t.Fatalf("AcquireExecutionLock error = %v, want resolve handoff store root", err)
		}
		return
	}
	if !strings.Contains(err.Error(), "open admitted handoff store root") {
		t.Fatalf("AcquireExecutionLock error = %v, want open admitted handoff store root", err)
	}
}

func TestSmCovLockAcquireWaitsInterruptiblyForContendedFence(t *testing.T) {
	fixture := smCovNewLockFixture(t)
	held := fixture.smCovAcquire(t)
	defer func() { _ = held.Close() }()

	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		lock, err := fixture.store.AcquireExecutionLock(ctx, fixture.request.HandoffID, fixture.digest)
		if err == nil {
			_ = lock.Close()
			t.Fatalf("contended acquire succeeded with a cancelled context")
		}
		if !strings.Contains(err.Error(), "wait for handoff execution lock") {
			t.Fatalf("contended acquire error = %v, want wait for handoff execution lock", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("contended acquire error = %v, want wrapped context.Canceled", err)
		}
		if lock != nil {
			t.Fatalf("contended acquire returned a non-nil lock alongside an error")
		}
	})

	t.Run("expired timeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		lock, err := fixture.store.AcquireExecutionLock(ctx, fixture.request.HandoffID, fixture.digest)
		if err == nil {
			_ = lock.Close()
			t.Fatalf("contended acquire succeeded with an expired timeout")
		}
		if !strings.Contains(err.Error(), "wait for handoff execution lock") {
			t.Fatalf("contended acquire error = %v, want wait for handoff execution lock", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("contended acquire error = %v, want wrapped context.DeadlineExceeded", err)
		}
	})

	t.Run("fence is reusable after release", func(t *testing.T) {
		if err := held.Close(); err != nil {
			t.Fatalf("release held fence: %v", err)
		}
		reacquired, err := fixture.store.AcquireExecutionLock(context.Background(), fixture.request.HandoffID, fixture.digest)
		if err != nil {
			t.Fatalf("acquire released fence: %v", err)
		}
		defer func() { _ = reacquired.Close() }()
		if !reacquired.HeldForStore(fixture.root, fixture.request, fixture.digest) {
			t.Fatalf("reacquired fence does not hold the exact admitted authority")
		}
	})
}

func TestSmCovLockOpenExecutionLockAtReusesStableInode(t *testing.T) {
	dir := t.TempDir()
	handoff, err := os.Open(dir)
	if err != nil {
		t.Fatalf("open handoff directory %s: %v", dir, err)
	}
	defer func() { _ = handoff.Close() }()

	firstFD, err := openExecutionLockAt(int(handoff.Fd()))
	if err != nil {
		t.Fatalf("first openExecutionLockAt: %v", err)
	}
	first := os.NewFile(uintptr(firstFD), "smcov-execution-lock-first")
	if first == nil {
		t.Fatalf("wrap first execution lock fd %d", firstFD)
	}
	defer func() { _ = first.Close() }()

	secondFD, err := openExecutionLockAt(int(handoff.Fd()))
	if err != nil {
		t.Fatalf("second openExecutionLockAt: %v", err)
	}
	second := os.NewFile(uintptr(secondFD), "smcov-execution-lock-second")
	if second == nil {
		t.Fatalf("wrap second execution lock fd %d", secondFD)
	}
	defer func() { _ = second.Close() }()

	firstInfo, err := first.Stat()
	if err != nil {
		t.Fatalf("stat first execution lock: %v", err)
	}
	secondInfo, err := second.Stat()
	if err != nil {
		t.Fatalf("stat second execution lock: %v", err)
	}
	if !os.SameFile(firstInfo, secondInfo) {
		t.Fatalf("reopened execution lock names a different inode than the first open")
	}
	onDisk, err := os.Stat(filepath.Join(dir, executionLockFileName))
	if err != nil {
		t.Fatalf("stat stable execution lock path: %v", err)
	}
	if !os.SameFile(firstInfo, onDisk) {
		t.Fatalf("open execution lock does not name the stable %s inode", executionLockFileName)
	}

	closedDir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open directory for closed-fd case: %v", err)
	}
	closedFD := int(closedDir.Fd())
	if err := closedDir.Close(); err != nil {
		t.Fatalf("close directory before reuse: %v", err)
	}
	if fd, err := openExecutionLockAt(closedFD); err == nil {
		_ = os.NewFile(uintptr(fd), "smcov-unexpected-lock").Close()
		t.Fatalf("openExecutionLockAt succeeded with a closed handoff directory descriptor")
	}
}

func TestSmCovLockOpenExecutionLockAtReportsCreationFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod unwritable handoff directory %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	handoff, err := os.Open(dir)
	if err != nil {
		t.Fatalf("open unwritable handoff directory %s: %v", dir, err)
	}
	defer func() { _ = handoff.Close() }()

	fd, err := openExecutionLockAt(int(handoff.Fd()))
	if err == nil {
		_ = os.NewFile(uintptr(fd), "smcov-unexpected-lock").Close()
		if os.Geteuid() == 0 {
			t.Log("running as root: directory write permission is not enforced, create failure not observable")
			return
		}
		t.Fatalf("openExecutionLockAt created a lock in an unwritable directory")
	}
}

func TestSmCovLockAdmittedRequestAtValidatesDurableRequest(t *testing.T) {
	fixture := smCovNewLockFixture(t)
	otherDigest := DigestBytes([]byte("some other request bytes"))

	t.Run("missing request file", func(t *testing.T) {
		handoff, err := os.Open(t.TempDir())
		if err != nil {
			t.Fatalf("open empty handoff directory: %v", err)
		}
		defer func() { _ = handoff.Close() }()
		request, file, err := admittedRequestAt(handoff, fixture.request.HandoffID, fixture.digest)
		if err == nil {
			_ = file.Close()
			t.Fatalf("admittedRequestAt returned request %#v from an empty handoff", request)
		}
		if !strings.Contains(err.Error(), "open admitted handoff request") {
			t.Fatalf("admittedRequestAt error = %v, want open admitted handoff request", err)
		}
		if file != nil {
			t.Fatalf("admittedRequestAt returned a non-nil request file alongside an error")
		}
	})

	t.Run("mismatched digest", func(t *testing.T) {
		dir := smCovWriteRequest(t, fixture.raw, 0o600)
		handoff, err := os.Open(dir)
		if err != nil {
			t.Fatalf("open handoff directory %s: %v", dir, err)
		}
		defer func() { _ = handoff.Close() }()
		request, file, err := admittedRequestAt(handoff, fixture.request.HandoffID, otherDigest)
		if err == nil {
			_ = file.Close()
			t.Fatalf("admittedRequestAt returned request %#v for a mismatched digest", request)
		}
		if !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("admittedRequestAt error = %v, want ErrHandoffConflict", err)
		}
		if file != nil {
			t.Fatalf("admittedRequestAt returned a non-nil request file alongside an error")
		}
	})

	t.Run("mismatched handoff id", func(t *testing.T) {
		dir := smCovWriteRequest(t, fixture.raw, 0o600)
		handoff, err := os.Open(dir)
		if err != nil {
			t.Fatalf("open handoff directory %s: %v", dir, err)
		}
		defer func() { _ = handoff.Close() }()
		request, file, err := admittedRequestAt(handoff, "handoff-other", fixture.digest)
		if err == nil {
			_ = file.Close()
			t.Fatalf("admittedRequestAt returned request %#v for a mismatched handoff id", request)
		}
		if !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("admittedRequestAt error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("success returns admitted request and open file", func(t *testing.T) {
		dir := smCovWriteRequest(t, fixture.raw, 0o600)
		handoff, err := os.Open(dir)
		if err != nil {
			t.Fatalf("open handoff directory %s: %v", dir, err)
		}
		defer func() { _ = handoff.Close() }()
		request, file, err := admittedRequestAt(handoff, fixture.request.HandoffID, fixture.digest)
		if err != nil {
			t.Fatalf("admittedRequestAt on the exact admitted request: %v", err)
		}
		if file == nil {
			t.Fatalf("admittedRequestAt returned a nil request file on success")
		}
		defer func() { _ = file.Close() }()
		if request != fixture.request {
			t.Fatalf("admittedRequestAt request = %#v, want %#v", request, fixture.request)
		}
		fileInfo, err := file.Stat()
		if err != nil {
			t.Fatalf("stat returned request file: %v", err)
		}
		wantInfo, err := os.Stat(filepath.Join(dir, requestFileName))
		if err != nil {
			t.Fatalf("stat request file path: %v", err)
		}
		if !os.SameFile(fileInfo, wantInfo) {
			t.Fatalf("returned request file names a different inode than %s", filepath.Join(dir, requestFileName))
		}
	})
}

func TestSmCovLockReadAdmittedRequestFileEnforcesBounds(t *testing.T) {
	fixture := smCovNewLockFixture(t)
	otherDigest := DigestBytes([]byte("some other request bytes"))

	t.Run("mode 0644 is accepted because permissions are not part of the bound check", func(t *testing.T) {
		dir := smCovWriteRequest(t, fixture.raw, 0o644)
		request, err := readAdmittedRequestFile(smCovOpenRequest(t, dir), fixture.request.HandoffID, fixture.digest)
		if err != nil {
			t.Fatalf("readAdmittedRequestFile(mode 0644) error = %v, want admitted request", err)
		}
		if request != fixture.request {
			t.Fatalf("readAdmittedRequestFile(mode 0644) request = %#v, want %#v", request, fixture.request)
		}
	})

	t.Run("request file is a directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, requestFileName), 0o700); err != nil {
			t.Fatalf("create directory request file: %v", err)
		}
		request, err := readAdmittedRequestFile(smCovOpenRequest(t, dir), fixture.request.HandoffID, fixture.digest)
		if err == nil {
			t.Fatalf("readAdmittedRequestFile accepted a directory as request %#v", request)
		}
		if !strings.Contains(err.Error(), "is not one bounded immutable file") {
			t.Fatalf("readAdmittedRequestFile error = %v, want not one bounded immutable file", err)
		}
	})

	t.Run("request file has a second hard link", func(t *testing.T) {
		dir := smCovWriteRequest(t, fixture.raw, 0o600)
		path := filepath.Join(dir, requestFileName)
		if err := os.Link(path, filepath.Join(dir, "request.json.link")); err != nil {
			t.Fatalf("hard link request file: %v", err)
		}
		request, err := readAdmittedRequestFile(smCovOpenRequest(t, dir), fixture.request.HandoffID, fixture.digest)
		if err == nil {
			t.Fatalf("readAdmittedRequestFile accepted a multi-link request %#v", request)
		}
		if !strings.Contains(err.Error(), "is not one bounded immutable file") {
			t.Fatalf("readAdmittedRequestFile error = %v, want not one bounded immutable file", err)
		}
	})

	t.Run("request file exceeds the byte limit", func(t *testing.T) {
		oversized := make([]byte, maxExecutionLockRequestBytes+1)
		dir := smCovWriteRequest(t, oversized, 0o600)
		request, err := readAdmittedRequestFile(smCovOpenRequest(t, dir), fixture.request.HandoffID, fixture.digest)
		if err == nil {
			t.Fatalf("readAdmittedRequestFile accepted an oversized request %#v", request)
		}
		if !strings.Contains(err.Error(), "is not one bounded immutable file") {
			t.Fatalf("readAdmittedRequestFile error = %v, want not one bounded immutable file", err)
		}
	})

	t.Run("request file is corrupted json", func(t *testing.T) {
		dir := smCovWriteRequest(t, []byte("{not-json"), 0o600)
		request, err := readAdmittedRequestFile(smCovOpenRequest(t, dir), fixture.request.HandoffID, fixture.digest)
		if err == nil {
			t.Fatalf("readAdmittedRequestFile accepted corrupted json as request %#v", request)
		}
		if !strings.Contains(err.Error(), "decode admitted handoff request") {
			t.Fatalf("readAdmittedRequestFile error = %v, want decode admitted handoff request", err)
		}
	})

	t.Run("handoff id mismatch conflicts", func(t *testing.T) {
		dir := smCovWriteRequest(t, fixture.raw, 0o600)
		request, err := readAdmittedRequestFile(smCovOpenRequest(t, dir), "handoff-other", fixture.digest)
		if err == nil {
			t.Fatalf("readAdmittedRequestFile accepted a mismatched handoff id as request %#v", request)
		}
		if !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("readAdmittedRequestFile error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("digest mismatch conflicts", func(t *testing.T) {
		dir := smCovWriteRequest(t, fixture.raw, 0o600)
		request, err := readAdmittedRequestFile(smCovOpenRequest(t, dir), fixture.request.HandoffID, otherDigest)
		if err == nil {
			t.Fatalf("readAdmittedRequestFile accepted a mismatched digest as request %#v", request)
		}
		if !errors.Is(err, ErrHandoffConflict) {
			t.Fatalf("readAdmittedRequestFile error = %v, want ErrHandoffConflict", err)
		}
	})

	t.Run("exact admitted request decodes", func(t *testing.T) {
		dir := smCovWriteRequest(t, fixture.raw, 0o600)
		request, err := readAdmittedRequestFile(smCovOpenRequest(t, dir), fixture.request.HandoffID, fixture.digest)
		if err != nil {
			t.Fatalf("readAdmittedRequestFile on the exact admitted request: %v", err)
		}
		if request != fixture.request {
			t.Fatalf("readAdmittedRequestFile request = %#v, want %#v", request, fixture.request)
		}
	})
}

func TestSmCovLockSameFileRejectsNilAndDistinctDescriptors(t *testing.T) {
	first, err := os.CreateTemp(t.TempDir(), "smcov-same-file-*")
	if err != nil {
		t.Fatalf("create first temp file: %v", err)
	}
	defer func() { _ = first.Close() }()
	second, err := os.CreateTemp(t.TempDir(), "smcov-same-file-*")
	if err != nil {
		t.Fatalf("create second temp file: %v", err)
	}
	defer func() { _ = second.Close() }()

	if sameFile(nil, first) {
		t.Fatalf("sameFile(nil, file) = true, want false")
	}
	if sameFile(first, nil) {
		t.Fatalf("sameFile(file, nil) = true, want false")
	}
	if !sameFile(first, first) {
		t.Fatalf("sameFile(file, file) = false, want true")
	}
	if sameFile(first, second) {
		t.Fatalf("sameFile(first, second) = true for distinct files, want false")
	}
}

func TestSmCovLockCloseIsIdempotentAndReleasesFence(t *testing.T) {
	fixture := smCovNewLockFixture(t)

	var nilLock *ExecutionLock
	if err := nilLock.Close(); err != nil {
		t.Fatalf("nil execution lock Close() = %v, want nil", err)
	}

	lock := fixture.smCovAcquire(t)
	if !lock.HeldForStore(fixture.root, fixture.request, fixture.digest) {
		t.Fatalf("acquired lock does not hold exact admitted authority")
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("first Close() = %v, want nil", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("second Close() = %v, want nil for an idempotent release", err)
	}
	if lock.HeldForStore(fixture.root, fixture.request, fixture.digest) {
		t.Fatalf("closed lock still claims exact admitted authority")
	}
	if retained, err := lock.RetainHandoffForStore(fixture.root, fixture.request, fixture.digest); err == nil {
		_ = retained.Close()
		t.Fatalf("closed lock retained the admitted handoff directory")
	}

	reacquired, err := fixture.store.AcquireExecutionLock(context.Background(), fixture.request.HandoffID, fixture.digest)
	if err != nil {
		t.Fatalf("reacquire after Close released the flock: %v", err)
	}
	if err := reacquired.Close(); err != nil {
		t.Fatalf("close reacquired lock: %v", err)
	}
}
