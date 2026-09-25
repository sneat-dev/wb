package sessionmove

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

var errPublishFault = errors.New("publish fault injected by test")

// TestPublishImmutableAtWrapsAnInjectedChmodFailure drives the "secure
// immutable temporary file" branch: real Fchmod succeeds every time on a
// descriptor this process just created, so before internal/filewrite
// carried an injectable seam this branch was unreachable from a test.
func TestPublishImmutableAtWrapsAnInjectedChmodFailure(t *testing.T) {
	t.Parallel()
	authority := smCovOpenDirectory(t, t.TempDir())
	inj := &filewrite.Injector{Step: filewrite.StepChmod, Err: errPublishFault}
	if _, err := publishImmutableAt(authority, "artifact", []byte("payload"), 0o600, inj); err == nil ||
		!strings.Contains(err.Error(), "secure immutable temporary file") {
		t.Fatalf("publishImmutableAt with injected chmod failure = %v, want a %q error", err, "secure immutable temporary file")
	}
}

// TestPublishImmutableAtWrapsAnInjectedWriteFailure drives the "write
// immutable temporary file" branch.
func TestPublishImmutableAtWrapsAnInjectedWriteFailure(t *testing.T) {
	t.Parallel()
	authority := smCovOpenDirectory(t, t.TempDir())
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errPublishFault}
	if _, err := publishImmutableAt(authority, "artifact", []byte("payload"), 0o600, inj); err == nil ||
		!strings.Contains(err.Error(), "write immutable temporary file") {
		t.Fatalf("publishImmutableAt with injected write failure = %v, want a %q error", err, "write immutable temporary file")
	}
}

// TestPublishImmutableAtWrapsAnInjectedSyncFailure drives the "sync
// immutable temporary file" branch.
func TestPublishImmutableAtWrapsAnInjectedSyncFailure(t *testing.T) {
	t.Parallel()
	authority := smCovOpenDirectory(t, t.TempDir())
	inj := &filewrite.Injector{Step: filewrite.StepSync, Err: errPublishFault}
	if _, err := publishImmutableAt(authority, "artifact", []byte("payload"), 0o600, inj); err == nil ||
		!strings.Contains(err.Error(), "sync immutable temporary file") {
		t.Fatalf("publishImmutableAt with injected sync failure = %v, want a %q error", err, "sync immutable temporary file")
	}
}

// TestPublishImmutableAtWrapsAnInjectedLinkFailureThatIsNotEEXIST drives
// the "publish immutable file" branch, distinct from the already-covered
// (name over NAME_MAX) real ENAMETOOLONG case: this uses an injected
// failure so the distinction between EEXIST (idempotent success) and
// every other Linkat error (a hard failure) is pinned directly rather
// than through one specific real errno.
func TestPublishImmutableAtWrapsAnInjectedLinkFailureThatIsNotEEXIST(t *testing.T) {
	t.Parallel()
	authority := smCovOpenDirectory(t, t.TempDir())
	inj := &filewrite.Injector{Step: filewrite.StepLink, Err: errPublishFault}
	if _, err := publishImmutableAt(authority, "artifact", []byte("payload"), 0o600, inj); err == nil ||
		!strings.Contains(err.Error(), "publish immutable file") {
		t.Fatalf("publishImmutableAt with injected link failure = %v, want a %q error", err, "publish immutable file")
	}
}

// TestPublishImmutableAtWrapsAnInjectedOpenPublishedFailure drives the
// "open published immutable file" branch: reopening the just-published
// name to verify its identity, which never fails for real once Linkat
// itself has just succeeded.
func TestPublishImmutableAtWrapsAnInjectedOpenPublishedFailure(t *testing.T) {
	t.Parallel()
	authority := smCovOpenDirectory(t, t.TempDir())
	inj := &filewrite.Injector{Step: filewrite.StepOpen, Name: "artifact", Err: errPublishFault}
	if _, err := publishImmutableAt(authority, "artifact", []byte("payload"), 0o600, inj); err == nil ||
		!strings.Contains(err.Error(), "open published immutable file") {
		t.Fatalf("publishImmutableAt with injected reopen failure = %v, want a %q error", err, "open published immutable file")
	}
}

// TestPublishImmutableAtWrapsAnInjectedDirSyncFailure drives the "sync
// immutable publication directory" branch.
func TestPublishImmutableAtWrapsAnInjectedDirSyncFailure(t *testing.T) {
	t.Parallel()
	authority := smCovOpenDirectory(t, t.TempDir())
	inj := &filewrite.Injector{Step: filewrite.StepDirSync, Err: errPublishFault}
	if _, err := publishImmutableAt(authority, "artifact", []byte("payload"), 0o600, inj); err == nil ||
		!strings.Contains(err.Error(), "sync immutable publication directory") {
		t.Fatalf("publishImmutableAt with injected directory-fsync failure = %v, want a %q error", err, "sync immutable publication directory")
	}
}

// TestPublishImmutableAtRejectsAPublishedFileThatDoesNotShareTheTemporarysInode
// drives the "does not retain the exact prepared inode" branch: the
// Injector's Hook swaps the just-linked name for a different file with
// identical stat-shape (0600 regular, one link) between the successful
// link and this call's own reopen-to-verify, so sameFile's device/inode
// comparison is what catches the swap -- exactly the defence this check
// exists for.
func TestPublishImmutableAtRejectsAPublishedFileThatDoesNotShareTheTemporarysInode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	authority := smCovOpenDirectory(t, dir)
	inj := &filewrite.Injector{
		Step: filewrite.StepOpen,
		Name: "artifact",
		Hook: func() {
			if err := unix.Unlinkat(int(authority.Fd()), "artifact", 0); err != nil {
				t.Fatalf("swap hook: unlink published name: %v", err)
			}
			fd, err := unix.Openat(int(authority.Fd()), "artifact",
				unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
			if err != nil {
				t.Fatalf("swap hook: create replacement: %v", err)
			}
			_ = unix.Close(fd)
		},
	}
	_, err := publishImmutableAt(authority, "artifact", []byte("payload"), 0o600, inj)
	if err == nil || !strings.Contains(err.Error(), "does not retain the exact prepared inode") {
		t.Fatalf("publishImmutableAt with a swapped published file = %v, want the inode-mismatch error", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "artifact")); err != nil {
		t.Fatalf("swapped replacement file missing after the check: %v", err)
	}
}
