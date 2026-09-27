package sessionmove

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

func TestSmCovReadAdmittedRequestFileRejectsClosedAndNonRegularDescriptors(t *testing.T) {
	t.Parallel()
	fixture := smCovNewLockFixture(t)

	closed, err := os.Open(filepath.Join(smCovHandoffDir(fixture), requestFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readAdmittedRequestFile(closed, fixture.request.HandoffID, fixture.digest); err == nil || !strings.Contains(err.Error(), "inspect admitted handoff request") {
		t.Fatalf("closed request descriptor error = %v", err)
	}

	directory, err := os.Open(smCovHandoffDir(fixture))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	if _, err := readAdmittedRequestFile(directory, fixture.request.HandoffID, fixture.digest); err == nil || !strings.Contains(err.Error(), "bounded immutable file") {
		t.Fatalf("directory request descriptor error = %v", err)
	}
}

func TestSmCovSecureDirectoriesRejectUnsafeExistingModes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	parent, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	for _, name := range []string{"unsafe-message", successorAddressesDirName} {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if directory, err := openSecureDirectoryAt(parent, "unsafe-message", false, "message test"); err == nil {
		_ = directory.Close()
		t.Fatal("openSecureDirectoryAt accepted mode 0755")
	}
	if directory, err := openSuccessorAddressesAt(parent, false); err == nil {
		_ = directory.Close()
		t.Fatal("openSuccessorAddressesAt accepted mode 0755")
	}
}

func TestSmCovReadImmutableRejectsMutableAndOversizedArtifacts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	authority, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authority.Close() })

	mutable := filepath.Join(dir, "mutable")
	if err := os.WriteFile(mutable, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readImmutableAt(authority, "mutable", 32, "test artifact"); err == nil || !strings.Contains(err.Error(), "single-link bounded") {
		t.Fatalf("mutable artifact error = %v", err)
	}

	oversized := filepath.Join(dir, "oversized")
	if err := os.WriteFile(oversized, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(oversized, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readImmutableAt(authority, "oversized", 4, "test artifact"); err == nil || !strings.Contains(err.Error(), "single-link bounded") {
		t.Fatalf("oversized artifact error = %v", err)
	}

	linked := filepath.Join(dir, "linked")
	if err := os.WriteFile(linked, []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(linked, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(linked, filepath.Join(dir, "linked-copy")); err != nil {
		t.Fatal(err)
	}
	if _, err := readImmutableAt(authority, "linked", 32, "test artifact"); err == nil || !strings.Contains(err.Error(), "single-link") {
		t.Fatalf("hard-linked artifact error = %v", err)
	}
}

func TestSmCovOpenEventsAtRejectsUnsafeExistingDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, eventsDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, eventsDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	handoff, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handoff.Close() })
	if events, err := openEventsAt(handoff, false); err == nil {
		_ = events.Close()
		t.Fatal("openEventsAt accepted mode 0755")
	}
}

func TestSmCovOpenRootReportsCreateFailureBelowARegularFile(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(blocker, "handoffs"))
	if root, err := store.openRoot(true); err == nil {
		_ = root.Close()
		t.Fatal("openRoot created a directory below a regular file")
	} else if !strings.Contains(err.Error(), "create handoff store root") {
		t.Fatalf("openRoot create failure = %v", err)
	}
}

func TestSmCovReadImmutableFileReportsDeterministicIOFailures(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(path, []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		t.Fatal(err)
	}
	base := func() immutableReadOps {
		return immutableReadOps{
			fstat:   func(_ int, got *unix.Stat_t) error { *got = stat; return nil },
			readAll: func(reader io.Reader) ([]byte, error) { return io.ReadAll(reader) },
			seek:    file.Seek,
		}
	}
	tests := []struct {
		name string
		ops  func() immutableReadOps
		want string
	}{
		{"initial stat", func() immutableReadOps {
			ops := base()
			ops.fstat = func(int, *unix.Stat_t) error { return errors.New("fstat failed") }
			return ops
		}, "inspect artifact"},
		{"initial read", func() immutableReadOps {
			ops := base()
			ops.readAll = func(io.Reader) ([]byte, error) { return nil, errors.New("read failed") }
			return ops
		}, "read artifact"},
		{"rewind", func() immutableReadOps {
			ops := base()
			ops.seek = func(int64, int) (int64, error) { return 0, errors.New("seek failed") }
			return ops
		}, "rewind artifact"},
		{"verification read", func() immutableReadOps {
			ops := base()
			calls := 0
			ops.readAll = func(reader io.Reader) ([]byte, error) {
				calls++
				if calls == 2 {
					return nil, errors.New("verify failed")
				}
				return io.ReadAll(reader)
			}
			return ops
		}, "verify artifact"},
		{"reinspect", func() immutableReadOps {
			ops := base()
			calls := 0
			ops.fstat = func(_ int, got *unix.Stat_t) error {
				calls++
				if calls == 2 {
					return errors.New("reinspect failed")
				}
				*got = stat
				return nil
			}
			return ops
		}, "reinspect artifact"},
		{"changed", func() immutableReadOps {
			ops := base()
			calls := 0
			ops.fstat = func(_ int, got *unix.Stat_t) error {
				calls++
				*got = stat
				if calls == 2 {
					got.Size++
				}
				return nil
			}
			return ops
		}, "changed while it was verified"},
		{"oversized read", func() immutableReadOps {
			ops := base()
			ops.readAll = func(io.Reader) ([]byte, error) { return []byte(strings.Repeat("x", 33)), nil }
			return ops
		}, "exceeds 32 bytes"},
		{"size changed during first read", func() immutableReadOps {
			ops := base()
			ops.readAll = func(io.Reader) ([]byte, error) { return []byte("short"), nil }
			return ops
		}, "size changed while it was read"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			if _, err := readImmutableFile(file, 32, "artifact", test.ops()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readImmutableFile error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSmCovPublishImmutableReportsEntropyFailure(t *testing.T) {
	t.Parallel()
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	if _, err := publishImmutableAtWithRandom(directory, "artifact", []byte("body"), 0o600, nil, func([]byte) (int, error) {
		return 0, errors.New("entropy unavailable")
	}); err == nil || !strings.Contains(err.Error(), "entropy unavailable") {
		t.Fatalf("publishImmutableAtWithRandom error = %v", err)
	}
}

func TestSmCovRetainedAuthorityReportsDescriptorDupFailure(t *testing.T) {
	t.Parallel()
	fixture := smCovNewLockFixture(t)
	lock := fixture.smCovAcquire(t)
	t.Cleanup(func() { _ = lock.Close() })
	fail := func(int) (int, error) { return -1, errors.New("descriptor exhausted") }
	if retained, err := lock.retainHandoffForStore(fixture.root, fixture.request, fixture.digest, fail); err == nil {
		_ = retained.Close()
		t.Fatal("retainHandoffForStore accepted a failed descriptor duplicate")
	} else if !strings.Contains(err.Error(), "descriptor exhausted") {
		t.Fatalf("retainHandoffForStore error = %v", err)
	}
	if retained, err := lock.retainStoreRootForStore(fixture.root, fixture.request, fixture.digest, fail); err == nil {
		_ = retained.Close()
		t.Fatal("retainStoreRootForStore accepted a failed descriptor duplicate")
	} else if !strings.Contains(err.Error(), "descriptor exhausted") {
		t.Fatalf("retainStoreRootForStore error = %v", err)
	}
}
