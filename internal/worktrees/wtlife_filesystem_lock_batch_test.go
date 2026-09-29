package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

func TestWtLifeCovFilesystemLockBatchPreparationFailures(t *testing.T) {
	t.Parallel()

	t.Run("seed readme", func(t *testing.T) {

		t.Parallel()
		home := t.TempDir()
		if err := os.Symlink("README.md", filepath.Join(home, "README.md")); err != nil {
			t.Fatal(err)
		}
		if _, err := prepareOperationRoot(home, "task", nil); err == nil {
			t.Fatal("prepareOperationRoot accepted an unreadable README symlink loop")
		}
	})

	t.Run("invalid repository", func(t *testing.T) {

		t.Parallel()
		root := t.TempDir()
		directory := wtLifeCovOpenDirectory(t, root)
		if _, _, err := prepareWorktreeDestination(root, directory, "", "../repo"); err == nil ||
			!strings.Contains(err.Error(), "invalid worktree repository segment") {
			t.Fatalf("invalid repository error = %v", err)
		}
	})

	t.Run("closed direct parent", func(t *testing.T) {

		t.Parallel()
		root := t.TempDir()
		directory, err := os.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := directory.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := openRelativeParentDirectory(directory, root, ""); err == nil {
			t.Fatal("openRelativeParentDirectory duplicated a closed descriptor")
		}
	})

	t.Run("owned invalid parent", func(t *testing.T) {

		t.Parallel()
		root := t.TempDir()
		directory := wtLifeCovOpenDirectory(t, root)
		if _, _, err := openRelativeParentDirectory(directory, root, "owner/.."); err == nil ||
			!strings.Contains(err.Error(), "invalid secure worktree parent segment") {
			t.Fatalf("owned invalid parent error = %v", err)
		}
	})
}

func TestWtLifeCovFilesystemLockBatchStageFailures(t *testing.T) {
	t.Parallel()

	closed, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := makeSecureStageDirectory(closed); err == nil || !strings.Contains(err.Error(), "rewind secure staging parent") {
		t.Fatalf("closed shared stage parent error = %v", err)
	}
	if _, err := makeTaskBoundLocalStageDirectory(closed, "task"); err == nil || !strings.Contains(err.Error(), "rewind secure staging parent") {
		t.Fatalf("closed task stage parent error = %v", err)
	}
	if _, err := makeTaskBoundLocalStageDirectory(closed, "../task"); err == nil || !strings.Contains(err.Error(), "invalid local stage task") {
		t.Fatalf("invalid task stage error = %v", err)
	}
	if _, _, err := claimRetiredStageDirectory(closed, ".wb-stage-", ".wb-retired-stage-"); err == nil ||
		!strings.Contains(err.Error(), "rewind secure staging parent") {
		t.Fatalf("closed retired stage parent error = %v", err)
	}

	regularPath := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(regularPath, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	regular, err := os.Open(regularPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = regular.Close() })
	if _, _, err := claimRetiredStageDirectory(regular, ".wb-stage-", ".wb-retired-stage-"); err == nil ||
		!strings.Contains(err.Error(), "read secure staging parent") {
		t.Fatalf("regular retired stage parent error = %v", err)
	}
}

func TestWtLifeCovFilesystemLockBatchRecoverEmptyStageRoot(t *testing.T) {
	t.Parallel()

	if recovered, err := recoverTaskBoundLocalStage(context.Background(), nil, filepath.Join(t.TempDir(), "missing"), "task", "branch"); err == nil || recovered {
		t.Fatalf("missing recovery root = %t, %v", recovered, err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "unrelated"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if recovered, err := recoverTaskBoundLocalStage(context.Background(), nil, root, "task", "branch"); err != nil || recovered {
		t.Fatalf("empty recovery result = %t, %v", recovered, err)
	}
	linked := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(root, linked); err != nil {
		t.Fatal(err)
	}
	if recovered, err := recoverTaskBoundLocalStage(context.Background(), nil, linked, "task", "branch"); err == nil || recovered {
		t.Fatalf("symlinked recovery root = %t, %v", recovered, err)
	}
}

func TestWtLifeCovFilesystemLockBatchDirectoryIdentityAndMove(t *testing.T) {
	t.Parallel()

	t.Run("identity", func(t *testing.T) {

		t.Parallel()
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "source"), 0o700); err != nil {
			t.Fatal(err)
		}
		parent := wtLifeCovOpenDirectory(t, root)
		source := wtLifeCovOpenDirectory(t, filepath.Join(root, "source"))
		other := wtLifeCovOpenDirectory(t, t.TempDir())
		if directoryEntryStillMatches(nil, "source", source) || directoryEntryStillMatches(parent, "source", nil) {
			t.Fatal("nil directory identity unexpectedly matched")
		}
		if !directoryEntryStillMatches(parent, "source", source) {
			t.Fatal("held source did not match its directory entry")
		}
		if directoryEntryStillMatches(parent, "source", other) {
			t.Fatal("unrelated directory matched the source entry")
		}
		if directoryEntryStillMatches(parent, "missing", source) {
			t.Fatal("missing directory entry matched")
		}
	})

	t.Run("authorized move outcomes", func(t *testing.T) {

		t.Parallel()
		newFixture := func(t *testing.T) (string, *os.File, *os.File) {
			t.Helper()
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "source"), 0o700); err != nil {
				t.Fatal(err)
			}
			return root, wtLifeCovOpenDirectory(t, root), wtLifeCovOpenDirectory(t, filepath.Join(root, "source"))
		}

		t.Run("mismatch", func(t *testing.T) {

			t.Parallel()
			_, parent, _ := newFixture(t)
			other := wtLifeCovOpenDirectory(t, t.TempDir())
			if _, err := moveExpectedDirectoryNoReplaceAuthorized(parent, "source", parent, "target", other, nil); err == nil {
				t.Fatal("mismatched source identity moved")
			}
		})

		t.Run("authorization", func(t *testing.T) {

			t.Parallel()
			_, parent, source := newFixture(t)
			want := errors.New("authorization refused")
			if _, err := moveExpectedDirectoryNoReplaceAuthorized(parent, "source", parent, "target", source, func() error { return want }); !errors.Is(err, want) {
				t.Fatalf("authorization error = %v", err)
			}
		})

		t.Run("collision", func(t *testing.T) {

			t.Parallel()
			root, parent, source := newFixture(t)
			if err := os.Mkdir(filepath.Join(root, "target"), 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := moveExpectedDirectoryNoReplaceAuthorized(parent, "source", parent, "target", source, nil); !errors.Is(err, unix.EEXIST) {
				t.Fatalf("collision error = %v", err)
			}
		})

		t.Run("success", func(t *testing.T) {

			t.Parallel()
			_, parent, source := newFixture(t)
			moved, err := moveExpectedDirectoryNoReplaceAuthorized(parent, "source", parent, "target", source, nil)
			if err != nil {
				t.Fatal(err)
			}
			_ = moved.Close()
		})

		t.Run("source recreation", func(t *testing.T) {

			t.Parallel()
			root, parent, source := newFixture(t)
			moved, err := moveExpectedDirectoryNoReplaceAuthorized(parent, "source", parent, "target", source, nil, func() {
				if mkdirErr := os.Mkdir(filepath.Join(root, "source"), 0o700); mkdirErr != nil {
					t.Fatal(mkdirErr)
				}
			})
			if moved == nil || !errors.Is(err, errDirectoryMoveIdentityChanged) {
				t.Fatalf("source recreation = %v, %v", moved, err)
			}
			_ = moved.Close()
		})
	})
}

func TestWtLifeCovFilesystemLockBatchQuarantineMatchingStage(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".wb-stage-test"), 0o700); err != nil {
		t.Fatal(err)
	}
	parent := wtLifeCovOpenDirectory(t, root)
	stage := wtLifeCovOpenDirectory(t, filepath.Join(root, ".wb-stage-test"))
	closed, err := os.Open(filepath.Join(root, ".wb-stage-test"))
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	if err := quarantineMatchingStageDirectoryAt(parent, closed); err == nil || !strings.Contains(err.Error(), "inspect held staging directory") {
		t.Fatalf("closed stage error = %v", err)
	}

	unrelated := wtLifeCovOpenDirectory(t, t.TempDir())
	if err := quarantineMatchingStageDirectoryAt(parent, unrelated); err != nil {
		t.Fatalf("unrelated stage error = %v", err)
	}
	if err := quarantineMatchingStageDirectoryAt(parent, stage); err != nil {
		t.Fatalf("matching stage quarantine: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".wb-stage-test")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active stage remains after quarantine: %v", err)
	}
}

func TestWtLifeCovFilesystemLockBatchMetadataAndIdentity(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })

	for _, operation := range []string{"", "bad\noperation", "bad\roperation", "bad\x00operation"} {
		if err := writeOperationLockMetadata(file, operation); err == nil {
			t.Fatalf("invalid operation %q accepted", operation)
		}
	}
	if err := writeOperationLockMetadata(file, " coverage-batch "); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(content), "operation=coverage-batch\n") {
		t.Fatalf("lock metadata = %q, %v", content, err)
	}

	closed, err := os.OpenFile(filepath.Join(root, "closed"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	if err := writeOperationLockMetadata(closed, "closed"); err == nil || !strings.Contains(err.Error(), "initialize worktree operation lock") {
		t.Fatalf("closed metadata error = %v", err)
	}
	if err := holdOperationLock(closed); err == nil || !strings.Contains(err.Error(), "hold secure worktree operation lock") {
		t.Fatalf("closed flock error = %v", err)
	}

	if _, err := lockIdentity(nil); err == nil {
		t.Fatal("nil lock identity accepted")
	}
	if _, err := exclusivelyOwnedLockIdentity(nil); err == nil {
		t.Fatal("nil exclusive lock identity accepted")
	}
	if _, err := lockIdentity(closed); err == nil {
		t.Fatal("closed lock identity accepted")
	}
	if _, err := exclusivelyOwnedLockIdentity(closed); err == nil {
		t.Fatal("closed exclusive lock identity accepted")
	}
	directory := wtLifeCovOpenDirectory(t, root)
	if _, err := lockIdentity(directory); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("directory lock identity error = %v", err)
	}
	if _, err := exclusivelyOwnedLockIdentity(directory); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("directory exclusive lock identity error = %v", err)
	}
	identity, err := lockIdentity(file)
	if err != nil {
		t.Fatal(err)
	}
	if !lockEntryStillMatches(directory, "lock", identity) || lockEntryStillMatches(directory, "missing", identity) {
		t.Fatal("lock entry identity classification failed")
	}
	if err := os.Link(path, filepath.Join(root, "lock-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := exclusivelyOwnedLockIdentity(file); err == nil || !strings.Contains(err.Error(), "2 links") {
		t.Fatalf("hard-linked lock identity error = %v", err)
	}
}

func TestWtLifeCovFilesystemLockBatchHoldContention(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "lock")
	first, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := holdOperationLock(first); err != nil {
		t.Fatal(err)
	}
	if err := holdOperationLock(second); !errors.Is(err, errOperationLockHeld) {
		t.Fatalf("contended lock error = %v", err)
	}
}

func TestWtLifeCovFilesystemLockBatchReclaimAndClaim(t *testing.T) {
	t.Parallel()

	t.Run("reclaim states", func(t *testing.T) {

		t.Parallel()
		root := t.TempDir()
		directory := wtLifeCovOpenDirectory(t, root)
		if _, err := reclaimInterruptedLock(directory, false); err == nil {
			t.Fatal("missing lock was reclaimed")
		}
		if err := os.Mkdir(filepath.Join(root, ".lock"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := reclaimInterruptedLock(directory, true); err == nil {
			t.Fatal("directory lock was reclaimed")
		}
		if err := os.Remove(filepath.Join(root, ".lock")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".lock"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := reclaimInterruptedLock(directory, true); !errors.Is(err, errOperationLockHeld) {
			t.Fatalf("empty lock error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, ".lock"), []byte("operation=old\npid=1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := reclaimInterruptedLock(directory, false); err == nil || !strings.Contains(err.Error(), "was interrupted") {
			t.Fatalf("unapproved reclaim error = %v", err)
		}
		lock, err := reclaimInterruptedLock(directory, true)
		if err != nil || !lock.interrupted {
			t.Fatalf("approved reclaim = %#v, %v", lock, err)
		}
		_ = lock.file.Close()
	})

	t.Run("retired claims", func(t *testing.T) {

		t.Parallel()
		root := t.TempDir()
		directory := wtLifeCovOpenDirectory(t, root)
		closed, err := os.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		_ = closed.Close()
		if _, _, err := claimRetiredLock(closed); err == nil || !strings.Contains(err.Error(), "rewind secure operation directory") {
			t.Fatalf("closed retired-lock directory error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, ".wb-retired-lock-hard"), []byte("retired"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(filepath.Join(root, ".wb-retired-lock-hard"), filepath.Join(root, "foreign-link")); err != nil {
			t.Fatal(err)
		}
		if claimed, reused, err := claimRetiredLock(directory); err != nil || reused || claimed != nil {
			t.Fatalf("hard-linked retirement claim = %v, %t, %v", claimed, reused, err)
		}
		if err := os.WriteFile(filepath.Join(root, ".wb-retired-lock-good"), []byte("retired"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".lock"), []byte("active"), 0o600); err != nil {
			t.Fatal(err)
		}
		if claimed, reused, err := claimRetiredLock(directory); err != nil || reused || claimed != nil {
			t.Fatalf("active-lock collision claim = %v, %t, %v", claimed, reused, err)
		}
		if err := os.Remove(filepath.Join(root, ".lock")); err != nil {
			t.Fatal(err)
		}
		claimed, reused, err := claimRetiredLock(directory)
		if err != nil || !reused || claimed == nil {
			t.Fatalf("retired lock claim = %v, %t, %v", claimed, reused, err)
		}
		_ = claimed.Close()
	})
}

func TestWtLifeCovFilesystemLockBatchMoveLockRaces(t *testing.T) {
	t.Parallel()

	newFixture := func(t *testing.T) (string, *os.File, managedLockIdentity) {
		t.Helper()
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "source"), []byte("source"), 0o600); err != nil {
			t.Fatal(err)
		}
		directory := wtLifeCovOpenDirectory(t, root)
		file, err := os.OpenFile(filepath.Join(root, "source"), os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		identity, err := lockIdentity(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		return root, directory, identity
	}

	t.Run("mismatch", func(t *testing.T) {

		t.Parallel()
		_, directory, identity := newFixture(t)
		identity.inode++
		if _, err := moveExpectedLockNoReplace(directory, "source", "target", identity); !errors.Is(err, errDirectoryMoveIdentityChanged) {
			t.Fatalf("mismatched lock move error = %v", err)
		}
	})

	t.Run("collision", func(t *testing.T) {

		t.Parallel()
		root, directory, identity := newFixture(t)
		if err := os.WriteFile(filepath.Join(root, "target"), []byte("target"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := moveExpectedLockNoReplace(directory, "source", "target", identity); !errors.Is(err, unix.EEXIST) {
			t.Fatalf("lock move collision error = %v", err)
		}
	})

	t.Run("open failure", func(t *testing.T) {

		t.Parallel()
		root, directory, identity := newFixture(t)
		_, err := moveExpectedLockNoReplace(directory, "source", "target", identity, moveExpectedLockHooks{afterMove: func() {
			if renameErr := os.Rename(filepath.Join(root, "target"), filepath.Join(root, "moved-away")); renameErr != nil {
				t.Fatal(renameErr)
			}
		}})
		if err == nil || !strings.Contains(err.Error(), "open moved operation lock") {
			t.Fatalf("moved lock open error = %v", err)
		}
	})

	t.Run("source recreation", func(t *testing.T) {

		t.Parallel()
		root, directory, identity := newFixture(t)
		moved, err := moveExpectedLockNoReplace(directory, "source", "target", identity, moveExpectedLockHooks{afterMove: func() {
			if writeErr := os.WriteFile(filepath.Join(root, "source"), []byte("successor"), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
		}})
		if moved == nil || !errors.Is(err, errDirectoryMoveIdentityChanged) {
			t.Fatalf("recreated source result = %v, %v", moved, err)
		}
		_ = moved.Close()
	})

	t.Run("destination replacement", func(t *testing.T) {

		t.Parallel()
		root, directory, identity := newFixture(t)
		moved, err := moveExpectedLockNoReplace(directory, "source", "target", identity, moveExpectedLockHooks{afterMove: func() {
			if renameErr := os.Rename(filepath.Join(root, "target"), filepath.Join(root, "expected-away")); renameErr != nil {
				t.Fatal(renameErr)
			}
			if writeErr := os.WriteFile(filepath.Join(root, "target"), []byte("replacement"), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
		}})
		if moved != nil || !errors.Is(err, errDirectoryMoveIdentityChanged) {
			t.Fatalf("replaced destination result = %v, %v", moved, err)
		}
		if content, readErr := os.ReadFile(filepath.Join(root, "source")); readErr != nil || string(content) != "replacement" {
			t.Fatalf("replacement restoration = %q, %v", content, readErr)
		}
	})

	t.Run("replacement preservation", func(t *testing.T) {

		t.Parallel()
		root, directory, identity := newFixture(t)
		moved, err := moveExpectedLockNoReplace(directory, "source", "target", identity, moveExpectedLockHooks{
			afterMove: func() {
				if renameErr := os.Rename(filepath.Join(root, "target"), filepath.Join(root, "expected-away")); renameErr != nil {
					t.Fatal(renameErr)
				}
				if writeErr := os.WriteFile(filepath.Join(root, "target"), []byte("replacement"), 0o600); writeErr != nil {
					t.Fatal(writeErr)
				}
			},
			beforeRestore: func() {
				if writeErr := os.WriteFile(filepath.Join(root, "source"), []byte("successor"), 0o600); writeErr != nil {
					t.Fatal(writeErr)
				}
			},
		})
		if moved != nil || !errors.Is(err, errDirectoryMoveIdentityChanged) || !strings.Contains(err.Error(), "changed before restoration") {
			t.Fatalf("replacement preservation result = %v, %v", moved, err)
		}
		if content, readErr := os.ReadFile(filepath.Join(root, "target")); readErr != nil || string(content) != "replacement" {
			t.Fatalf("replacement changed = %q, %v", content, readErr)
		}
	})

	t.Run("source inspection failure", func(t *testing.T) {

		t.Parallel()
		_, directory, identity := newFixture(t)
		moved, err := moveExpectedLockNoReplace(directory, "source", "target", identity, moveExpectedLockHooks{afterOpen: func() {
			_ = directory.Close()
		}})
		if moved != nil || err == nil || !strings.Contains(err.Error(), "inspect operation lock source after move") {
			t.Fatalf("source inspection result = %v, %v", moved, err)
		}
	})

	t.Run("success", func(t *testing.T) {

		t.Parallel()
		_, directory, identity := newFixture(t)
		moved, err := moveExpectedLockNoReplace(directory, "source", "target", identity)
		if err != nil || moved == nil {
			t.Fatalf("successful lock move = %v, %v", moved, err)
		}
		_ = moved.Close()
	})
}

func TestWtLifeCovFilesystemLockBatchAcquireAndQuarantine(t *testing.T) {
	t.Parallel()

	closed, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	if _, err := acquireLockAtReclaimingInterrupted(closed, false, "operation"); err == nil {
		t.Fatal("lock acquired in a closed operation directory")
	}

	root := t.TempDir()
	directory := wtLifeCovOpenDirectory(t, root)
	lock, err := acquireLockAtReclaimingInterrupted(directory, false, "coverage-batch")
	if err != nil {
		t.Fatal(err)
	}
	if err := quarantineLockEntry(directory, managedLockIdentity{}); !errors.Is(err, errDirectoryMoveIdentityChanged) {
		t.Fatalf("mismatched quarantine error = %v", err)
	}
	if err := lock.release(); err != nil {
		t.Fatalf("release acquired lock: %v", err)
	}
	if err := (operationLock{}).release(); err != nil {
		t.Fatalf("empty lock release: %v", err)
	}
}
