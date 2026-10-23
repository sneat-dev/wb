//go:build !windows

package lifecyclehooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

// hkCovRequirePermissionSemantics skips tests that rely on unix file
// permission checks, which do not apply when the test runs as root.
func hkCovRequirePermissionSemantics(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("file permission semantics do not apply to root")
	}
}

func TestHkCovLoadRejectsUnreadableConfig(t *testing.T) {
	t.Parallel()
	hkCovRequirePermissionSemantics(t)
	root := t.TempDir()
	executable := hkCovWriteFile(t, filepath.Join(root, "indexer"), "#!/bin/sh\n", 0o755)
	config := hkCovWriteFile(t, filepath.Join(root, "wb.yaml"), hkCovValidConfigYAML(executable), 0o600)
	if err := os.Chmod(config, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(config, 0o600) })
	if _, _, err := Load(config); err == nil || !strings.Contains(err.Error(), "read lifecycle hooks config") {
		t.Fatalf("unreadable config error=%v", err)
	}
}

func TestHkCovWriteJSONAtomicCreateTempFailure(t *testing.T) {
	t.Parallel()
	hkCovRequirePermissionSemantics(t)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
	if err := writeJSONAtomic(filepath.Join(directory, "value.json"), map[string]int{"a": 1}, 0o600); err == nil {
		t.Fatal("expected temp file creation failure in a read-only directory")
	}
}

func TestHkCovAppendReceiptRejectsReadOnlyStream(t *testing.T) {
	t.Parallel()
	hkCovRequirePermissionSemantics(t)
	dispatcher, _ := hkCovEnv(t)
	hkCovWriteFile(t, dispatcher.ReceiptPath, "", 0o400)
	if err := appendReceipt(dispatcher.ReceiptPath, hkCovReceipt("id")); err == nil {
		t.Fatal("expected open failure for a read-only receipt stream")
	}
}

func TestHkCovScanReceiptsRejectsUnreadableStream(t *testing.T) {
	t.Parallel()
	hkCovRequirePermissionSemantics(t)
	path := hkCovWriteFile(t, filepath.Join(t.TempDir(), "receipts.jsonl"), "", 0o000)
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, err := scanReceipts(path, func(Receipt) {}); err == nil {
		t.Fatal("expected open failure for an unreadable receipt stream")
	}
}

func TestHkCovDrainWarnsWhenUnseenFailureCannotBeRecorded(t *testing.T) {
	t.Parallel()
	hkCovRequirePermissionSemantics(t)
	dispatcher, checkout := hkCovEnv(t)
	dispatcher.Run = func(context.Context, Invocation) error {
		if err := os.Chmod(dispatcher.unseenDir(), 0o500); err != nil {
			return err
		}
		return errors.New("hook failed")
	}
	t.Cleanup(func() { _ = os.Chmod(dispatcher.unseenDir(), 0o700) })
	hkCovSeedJob(t, dispatcher, hkCovJob("index", checkout, "b"))
	report, err := dispatcher.Drain(context.Background(), 1)
	if err != nil || !containsText(report.Warnings, "record unseen lifecycle hook failure") {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestHkCovDrainWarnsWhenQueueItemCannotBeCompleted(t *testing.T) {
	t.Parallel()
	hkCovRequirePermissionSemantics(t)
	dispatcher, checkout := hkCovEnv(t)
	dispatcher.Run = func(context.Context, Invocation) error {
		if err := os.Chmod(dispatcher.runningDir(), 0o500); err != nil {
			return err
		}
		return nil
	}
	t.Cleanup(func() { _ = os.Chmod(dispatcher.runningDir(), 0o700) })
	hkCovSeedJob(t, dispatcher, hkCovJob("index", checkout, "b"))
	report, err := dispatcher.Drain(context.Background(), 1)
	if err != nil || report.Executed != 1 || !containsText(report.Warnings, "complete lifecycle hook queue item") {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestHkCovGCReportsUnreadableReceiptStream(t *testing.T) {
	t.Parallel()
	hkCovRequirePermissionSemantics(t)
	dispatcher, _ := hkCovEnv(t)
	hkCovWriteFile(t, dispatcher.ReceiptPath, "", 0o000)
	t.Cleanup(func() { _ = os.Chmod(dispatcher.ReceiptPath, 0o600) })
	if _, err := dispatcher.GC(GCOptions{}); err == nil {
		t.Fatal("expected open failure for an unreadable receipt stream")
	}
}

func TestHkCovRewriteReceiptRecordsCreateTempFailure(t *testing.T) {
	t.Parallel()
	hkCovRequirePermissionSemantics(t)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
	if err := rewriteReceiptRecords(filepath.Join(directory, "receipts.jsonl"), nil); err == nil {
		t.Fatal("expected temp file creation failure in a read-only directory")
	}
}

func TestHkCovClaimUnseenWarningsReportsUnreadableEntry(t *testing.T) {
	t.Parallel()
	hkCovRequirePermissionSemantics(t)
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dispatcher.unseenDir(), "a.json")
	hkCovWriteFile(t, path, hkCovMustJSON(t, hkCovReceipt("a")), 0o000)
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, err := dispatcher.claimUnseenWarnings(10); err == nil {
		t.Fatal("expected unreadable unseen failure to be reported")
	}
}

func TestHkCovSyncDirectoryRejectsMissingPath(t *testing.T) {
	t.Parallel()
	if err := syncDirectory(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("expected missing directory failure")
	}
}

type hkCovFakeFileInfo struct {
	sys any
}

func (info hkCovFakeFileInfo) Name() string       { return "fake" }
func (info hkCovFakeFileInfo) Size() int64        { return 0 }
func (info hkCovFakeFileInfo) Mode() os.FileMode  { return 0o755 }
func (info hkCovFakeFileInfo) ModTime() time.Time { return time.Time{} }
func (info hkCovFakeFileInfo) IsDir() bool        { return false }
func (info hkCovFakeFileInfo) Sys() any           { return info.sys }

func TestHkCovTrustedExecutableAndOwnerChecks(t *testing.T) {
	t.Parallel()
	notExecutable := hkCovWriteFile(t, filepath.Join(t.TempDir(), "indexer"), "#!/bin/sh\n", 0o644)
	info, err := os.Stat(notExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTrustedExecutable(notExecutable, info); err == nil || !strings.Contains(err.Error(), "executable file") {
		t.Fatalf("non-executable error=%v", err)
	}

	if err := validateTrustedOwnerAndMode(hkCovFakeFileInfo{sys: "not stat"}, "run"); err == nil || !strings.Contains(err.Error(), "unsupported file metadata") {
		t.Fatalf("unsupported metadata error=%v", err)
	}

	foreign := &syscall.Stat_t{Uid: uint32(os.Geteuid()) + 4242}
	if err := validateTrustedOwnerAndMode(hkCovFakeFileInfo{sys: foreign}, "run"); err == nil || !strings.Contains(err.Error(), "owned by the current user or root") {
		t.Fatalf("foreign owner error=%v", err)
	}

	executable := hkCovWriteFile(t, filepath.Join(t.TempDir(), "indexer-ok"), "#!/bin/sh\n", 0o755)
	info, err = os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTrustedExecutable(executable, info); err != nil {
		t.Fatalf("private executable rejected: %v", err)
	}
}

func TestHkCovRecoverRunningReportsPendingWriteFailure(t *testing.T) {
	t.Parallel()
	hkCovRequirePermissionSemantics(t)
	dispatcher, checkout := hkCovEnv(t)
	hkCovWriteJob(t, dispatcher.runningDir(), hkCovJob("index", checkout, "new"))
	hkCovWriteJob(t, dispatcher.pendingDir(), hkCovJob("index", checkout, "old"))
	if err := os.Chmod(dispatcher.pendingDir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dispatcher.pendingDir(), 0o700) })
	if _, err := dispatcher.recoverRunning(); err == nil {
		t.Fatal("expected pending write failure while merging a recovered item")
	}
}

func TestHkCovRecoverRunningReportsRestoreRenameFailure(t *testing.T) {
	t.Parallel()
	hkCovRequirePermissionSemantics(t)
	dispatcher, checkout := hkCovEnv(t)
	hkCovWriteJob(t, dispatcher.runningDir(), hkCovJob("index", checkout, "new"))
	if err := os.MkdirAll(dispatcher.pendingDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dispatcher.pendingDir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dispatcher.pendingDir(), 0o700) })
	if _, err := dispatcher.recoverRunning(); err == nil {
		t.Fatal("expected rename failure while restoring a recovered item")
	}
}

func TestHkCovRecoverRunningReportsRenameAfterQuarantineFailure(t *testing.T) {
	t.Parallel()
	hkCovRequirePermissionSemantics(t)
	dispatcher, checkout := hkCovEnv(t)
	job := hkCovJob("index", checkout, "new")
	hkCovWriteJob(t, dispatcher.runningDir(), job)
	hkCovWriteFile(t, filepath.Join(dispatcher.pendingDir(), queueFileName(job.Key)), "{broken", 0o600)
	if err := os.MkdirAll(dispatcher.quarantineDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dispatcher.runningDir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dispatcher.runningDir(), 0o700) })
	if _, err := dispatcher.recoverRunning(); err == nil {
		t.Fatal("expected rename failure after quarantining an invalid pending item")
	}
}

func TestHkCovRecoverRunningReportsRunningRemovalFailure(t *testing.T) {
	t.Parallel()
	hkCovRequirePermissionSemantics(t)
	dispatcher, checkout := hkCovEnv(t)
	hkCovWriteJob(t, dispatcher.runningDir(), hkCovJob("index", checkout, "new"))
	hkCovWriteJob(t, dispatcher.pendingDir(), hkCovJob("index", checkout, "old"))
	if err := os.Chmod(dispatcher.runningDir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dispatcher.runningDir(), 0o700) })
	if _, err := dispatcher.recoverRunning(); err == nil {
		t.Fatal("expected removal failure after merging a recovered item")
	}
}

func TestHkCovClaimBatchReportsSyncFailure(t *testing.T) {
	t.Parallel()
	hkCovRequirePermissionSemantics(t)
	dispatcher, checkout := hkCovEnv(t)
	hkCovWriteJob(t, dispatcher.pendingDir(), hkCovJob("index", checkout, "b"))
	if err := os.MkdirAll(dispatcher.runningDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dispatcher.runningDir(), 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dispatcher.runningDir(), 0o700) })
	worker := flock.New(filepath.Join(dispatcher.StateDir, "worker.lock"))
	if _, _, _, err := dispatcher.claimBatch(1, worker); err == nil {
		t.Fatal("expected sync failure after claiming a job into an unreadable directory")
	}
}

func TestHkCovGCApplyReportsDiagnosticRemovalFailure(t *testing.T) {
	t.Parallel()
	hkCovRequirePermissionSemantics(t)
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	receipt := hkCovReceipt("stubborn")
	receipt.FinishedAt = now.Add(-72 * time.Hour)
	locked := filepath.Join(dispatcher.diagnosticsDir(), receipt.ID, "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	hkCovWriteFile(t, filepath.Join(locked, "child"), "x", 0o600)
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	hkCovWriteReceiptStream(t, dispatcher.ReceiptPath, receipt)
	if _, err := dispatcher.GC(GCOptions{Apply: true, Keep: 0, OlderThan: time.Hour, Now: now}); err == nil {
		t.Fatal("expected diagnostic removal failure")
	}
}

func TestHkCovQuarantineFileReportsReasonWriteFailure(t *testing.T) {
	t.Parallel()
	hkCovRequirePermissionSemantics(t)
	root := t.TempDir()
	dispatcher := hkCovDispatcherFor(t, root, filepath.Join(root, "wb.yaml"))
	dispatcher.StateDir = filepath.Join(root, "state")
	now := time.Unix(1700000000, 0).UTC()
	dispatcher.Now = func() time.Time { return now }
	// The quarantined name fits in NAME_MAX, but the ".reason.txt" sibling does not.
	base := strings.Repeat("a", 250-1-len(receiptID(now))-4)
	source := hkCovWriteFile(t, filepath.Join(root, base+".json"), "{}", 0o600)
	if _, err := dispatcher.quarantineFile(source, "because"); err == nil {
		t.Fatal("expected reason file write failure for an over-long quarantine name")
	}
}
