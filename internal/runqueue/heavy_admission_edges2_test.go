package runqueue

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAtomicWriteFileReportsRenameFailure drives atomicWriteFile's own
// os.Rename error branch (visibility.go): renaming the temp file onto an
// existing, non-empty directory at the destination path fails with EISDIR/
// ENOTEMPTY, no seam needed.
func TestAtomicWriteFileReportsRenameFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	destination := filepath.Join(dir, "target")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "occupant"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(destination, []byte("payload"), 0o600); err == nil {
		t.Fatal("atomicWriteFile onto a non-empty directory succeeded, want a rename error")
	}
}

// TestReadHolderRecordsSkipsASubdirectory drives readHolderRecords' own
// IsDir-continue branch (visibility.go): a subdirectory sitting inside the
// holders directory (never written by this package, but tolerated) is
// skipped rather than misread as a holder record.
func TestReadHolderRecordsSkipsASubdirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "stray"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := readHolderRecords(dir); len(got) != 0 {
		t.Fatalf("readHolderRecords with only a stray subdirectory = %#v, want none", got)
	}
}

// TestAnnounceHeavyHolderReportsWriteFailureWhenTheRunningDirIsReadOnly
// drives announceHeavyHolder's atomicWriteFile-error branch (heavy.go): the
// running dir already exists (so MkdirAll is a a no-op), but is not
// writable, so CreateTemp inside atomicWriteFile fails.
func TestAnnounceHeavyHolderReportsWriteFailureWhenTheRunningDirIsReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks")
	}
	t.Parallel()
	root := t.TempDir()
	dir := heavyRunningDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	path, ok := announceHeavyHolder(root, Participant{PID: 1}, 1, time.Now())
	if ok || path != "" {
		t.Fatalf("announceHeavyHolder over a read-only running dir = (%q, %v), want (\"\", false)", path, ok)
	}
}

// TestAdmitHeavyReportsAdmissionLockDirectoryCreationFailure drives
// admitHeavy's own tryLockHeavyAdmission-error branch (heavy.go), distinct
// from the standalone tryLockHeavyAdmission unit test: this drives it
// through the full admitHeavy call path, with the ticket already at the
// head of the FIFO queue, so the error return happens on admitHeavy's own
// first attempt rather than via its retry/backoff loop.
func TestAdmitHeavyReportsAdmissionLockDirectoryCreationFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks")
	}
	t.Parallel()
	root := t.TempDir()
	ticket := RegisterHeavy(root, Participant{PID: 1})
	defer ticket.Forget()

	heavyDir := heavyRoot(root)
	if err := os.MkdirAll(heavyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(heavyDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(heavyDir, 0o700) })

	if _, _, _, err := admitHeavy(context.Background(), root, Participant{PID: 1}, ticket); err == nil {
		t.Fatal("admitHeavy with an admission-lock directory it cannot create succeeded, want an error")
	}
}

// TestAdmitHeavyRetriesWhenAnnounceFailsUnderARaceMinusReadOnlyRunningDir
// drives admitHeavy's own announceHeavyHolder-failure branch (heavy.go,
// "review finding PR #628, M2"): a read-only running dir lets
// readHeavyHolders (which only needs read+execute) succeed with zero live
// holders, so the admission decision proceeds all the way to
// announceHeavyHolder, which then fails to write its own holder record —
// exactly the scenario that finding describes, reproduced without any new
// production seam.
func TestAdmitHeavyRetriesWhenAnnounceFailsThenReportsCancellation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks")
	}
	t.Parallel()
	root := t.TempDir()
	ticket := RegisterHeavy(root, Participant{PID: 1})
	defer ticket.Forget()

	dir := heavyRunningDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	ctx, cancel := context.WithTimeout(context.Background(), retryInterval*3+retryInterval/4)
	defer cancel()
	if _, _, _, err := admitHeavy(ctx, root, Participant{PID: 1}, ticket); err == nil {
		t.Fatal("admitHeavy that can never announce its holder record succeeded, want its Err() once the context expires")
	}
}
