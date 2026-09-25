package streams

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStoreArchiveLockedSurfacesANonNotExistStatFailure covers
// archiveLocked's os.Stat error branch (store.go:262-264): the archive
// destination candidate exists as a self-referential symlink, so Stat
// (which follows symlinks) fails with ELOOP rather than "not exist", and
// that must be reported instead of being treated as "the name is free".
func TestStoreArchiveLockedSurfacesANonNotExistStatFailure(t *testing.T) {
	t.Parallel()
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	fixed := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	store.Now = func() time.Time { return fixed }
	ended := fixed
	if _, err := store.Create(Stream{Name: "loopy", Phase: PhaseEnded, EndedAt: &ended}); err != nil {
		t.Fatal(err)
	}
	archivedPath := store.Dir("loopy.ended-20260506T070809Z")
	if err := os.Symlink(archivedPath, archivedPath); err != nil {
		t.Skipf("symlinks unavailable in this environment: %v", err)
	}
	_, err := store.ArchiveLocked("loopy")
	if err == nil {
		t.Fatal("ArchiveLocked with a symlink-loop destination = nil error, want one")
	}
	if !strings.Contains(err.Error(), "inspect archive destination") {
		t.Fatalf("ArchiveLocked error = %v, want it to name the inspect-destination step", err)
	}
}

// TestStoreArchiveLockedSurfacesARenameFailure covers archiveLocked's
// os.Rename error branch (store.go:267-269). A dangling symlink at the
// destination reports as "not exist" to Stat (so the suffix loop accepts
// it as free) but is a real directory entry, and POSIX rename refuses to
// replace a non-directory entry with a directory.
func TestStoreArchiveLockedSurfacesARenameFailure(t *testing.T) {
	t.Parallel()
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	fixed := time.Date(2026, 6, 7, 8, 9, 10, 0, time.UTC)
	store.Now = func() time.Time { return fixed }
	ended := fixed
	if _, err := store.Create(Stream{Name: "danglingdest", Phase: PhaseEnded, EndedAt: &ended}); err != nil {
		t.Fatal(err)
	}
	archivedPath := store.Dir("danglingdest.ended-20260607T080910Z")
	if err := os.Symlink(filepath.Join(store.Root, "nowhere"), archivedPath); err != nil {
		t.Skipf("symlinks unavailable in this environment: %v", err)
	}
	_, err := store.ArchiveLocked("danglingdest")
	if err == nil {
		t.Fatal("ArchiveLocked renaming onto a dangling-symlink destination = nil error, want one")
	}
	if !strings.Contains(err.Error(), "archive stream") {
		t.Fatalf("ArchiveLocked error = %v, want it to name the archive-rename step", err)
	}
}

// TestStoreArchiveLockedSurfacesAPostRenamePublishFailure covers
// archiveLocked's final writeAtomically error-propagation branch
// (store.go:274-276): the stream directory is read-only, which does not
// block Load, the Stat probe loop, or os.Rename itself (rename only needs
// write permission on the two *parent* directories, not on the moved
// directory's own mode, which travels with it) — but does block staging
// the updated record inside the freshly-renamed directory afterward.
func TestStoreArchiveLockedSurfacesAPostRenamePublishFailure(t *testing.T) {
	t.Parallel()
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	fixed := time.Date(2026, 8, 9, 10, 11, 12, 0, time.UTC)
	store.Now = func() time.Time { return fixed }
	ended := fixed
	if _, err := store.Create(Stream{Name: "readonlyarchive", Phase: PhaseEnded, EndedAt: &ended}); err != nil {
		t.Fatal(err)
	}
	dir := store.Dir("readonlyarchive")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(dir, 0o700)
		_ = os.Chmod(store.Dir("readonlyarchive.ended-20260809T101112Z"), 0o700)
	})
	_, err := store.ArchiveLocked("readonlyarchive")
	if err == nil {
		t.Fatal("ArchiveLocked publishing into a read-only renamed directory = nil error, want one")
	}
	if !strings.Contains(err.Error(), "stage stream state") {
		t.Fatalf("ArchiveLocked error = %v, want it to name the stage step", err)
	}
}

// TestStoreDeleteSurfacesARemoveAllFailure covers Delete's os.RemoveAll
// error branch (store.go:296-298). The stream directory itself, and
// stream.json inside it, stay fully readable so Load succeeds; a *nested*,
// non-empty subdirectory is made unreadable, so RemoveAll fails only once
// it tries to recurse into that subdirectory to empty it first.
func TestStoreDeleteSurfacesARemoveAllFailure(t *testing.T) {
	t.Parallel()
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	ended := time.Date(2026, 7, 8, 9, 10, 11, 0, time.UTC)
	if _, err := store.Create(Stream{Name: "unremovable", Phase: PhaseEnded, EndedAt: &ended}); err != nil {
		t.Fatal(err)
	}
	dir := store.Dir("unremovable")
	blockedSub := filepath.Join(dir, "blocked-sub")
	if err := os.Mkdir(blockedSub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blockedSub, "leftover"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blockedSub, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blockedSub, 0o700) })
	err := store.Delete("unremovable")
	if err == nil {
		t.Fatal("Delete with an unremovable nested directory = nil error, want one")
	}
	if !strings.Contains(err.Error(), "delete stream") {
		t.Fatalf("Delete error = %v, want it to name the delete step", err)
	}
}

// TestStoreUpdateSurfacesAWriteFailure covers Update's writeAtomically
// error-propagation branch (store.go:324-326): the stream directory is
// read-only, so staging the new state file fails after the mutation
// function itself has already run successfully.
func TestStoreUpdateSurfacesAWriteFailure(t *testing.T) {
	t.Parallel()
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if _, err := store.Create(Stream{Name: "readonly", Phase: PhaseOpen}); err != nil {
		t.Fatal(err)
	}
	// A first, successful Update materializes the per-stream lock file: the
	// per-stream lock is acquired lazily (Create only takes the store-wide
	// lock), and opening an already-existing lock file needs no write
	// permission on the directory, unlike creating it for the first time.
	// That isolates the read-only directory's effect to writeAtomically's
	// own CreateTemp call below, rather than the lock's os.OpenFile.
	if _, err := store.Update("readonly", func(*Stream) error { return nil }); err != nil {
		t.Fatal(err)
	}
	dir := store.Dir("readonly")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	_, err := store.Update("readonly", func(stream *Stream) error {
		stream.Phase = PhaseEnded
		return nil
	})
	if err == nil {
		t.Fatal("Update with a read-only stream directory = nil error, want one")
	}
	if !strings.Contains(err.Error(), "stage stream state") {
		t.Fatalf("Update error = %v, want it to name the stage step", err)
	}
}

// TestStoreWriteAtomicallyInjectedSurfacesAnEncodeFailure covers
// writeAtomicallyInjected's json.MarshalIndent error branch
// (store.go:340-343). A CreatedAt year outside [0,9999] makes time.Time's
// own MarshalJSON fail — a real encoding failure reachable through a
// public Stream field, no seam needed.
func TestStoreWriteAtomicallyInjectedSurfacesAnEncodeFailure(t *testing.T) {
	t.Parallel()
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	unencodable := Stream{Name: "bad-time", CreatedAt: time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)}
	err := store.writeAtomicallyInjected("bad-time", unencodable, nil)
	if err == nil {
		t.Fatal("writeAtomicallyInjected with an unencodable CreatedAt = nil error, want one")
	}
	if _, statErr := os.Stat(store.statePath("bad-time")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a failed encode published a visible state file: %v", statErr)
	}
}
