package lifecyclehooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestHkCovStatusDefaultsLimitAndFailsOnState(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	status, err := dispatcher.Status(0)
	if err != nil || status.Worker != "idle" || status.StateDir == "" || status.ReceiptPath == "" {
		t.Fatalf("status=%+v err=%v", status, err)
	}

	blocker := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)
	broken := dispatcher
	broken.StateDir = filepath.Join(blocker, "state")
	if _, err := broken.Status(1); err == nil || !strings.Contains(err.Error(), "create lifecycle hook state") {
		t.Fatalf("state error=%v", err)
	}
}

func TestHkCovStatusReportsRunningWorkerAndFindings(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	worker := flock.New(filepath.Join(dispatcher.StateDir, "worker.lock"))
	if err := worker.Lock(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.Unlock() }()
	status, err := dispatcher.Status(5)
	if err != nil || status.Worker != "running" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestHkCovStatusRecordsWorkerHealthAndLockFailures(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := os.MkdirAll(filepath.Join(dispatcher.StateDir, "worker-health.json", "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	status, err := dispatcher.Status(5)
	if err != nil || !containsText(status.Findings, "read worker health") {
		t.Fatalf("status=%+v err=%v", status, err)
	}

	locked, _ := hkCovEnv(t)
	if err := locked.ensureState(); err != nil {
		t.Fatal(err)
	}
	hkCovLockFile(t, filepath.Join(locked.StateDir, "queue.lock"))
	if _, err := locked.Status(5); err == nil {
		t.Fatal("expected queue snapshot failure")
	}
}

func TestHkCovStatusReportsReceiptStreamFailure(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	blocker := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)
	dispatcher.ReceiptPath = filepath.Join(blocker, "receipts.jsonl")
	if _, err := dispatcher.Status(5); err == nil {
		t.Fatal("expected receipt stream failure")
	}
}

func TestHkCovQuarantineSnapshotCountsAndLimits(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	hkCovWriteFile(t, filepath.Join(dispatcher.quarantineDir(), "a.bad"), "x", 0o600)
	hkCovWriteFile(t, filepath.Join(dispatcher.quarantineDir(), "b.bad"), "x", 0o600)
	hkCovWriteFile(t, filepath.Join(dispatcher.quarantineDir(), "notes.txt"), "x", 0o600)
	if err := os.MkdirAll(filepath.Join(dispatcher.quarantineDir(), "nested.bad"), 0o700); err != nil {
		t.Fatal(err)
	}
	count, findings, err := dispatcher.quarantineSnapshot(1)
	if err != nil || count != 2 || len(findings) != 1 || !strings.Contains(findings[0], "quarantined lifecycle hook state") {
		t.Fatalf("count=%d findings=%v err=%v", count, findings, err)
	}

	missing := dispatcher
	missing.StateDir = filepath.Join(t.TempDir(), "absent")
	if _, _, err := missing.quarantineSnapshot(1); err == nil {
		t.Fatal("expected missing quarantine directory failure")
	}
}

func TestHkCovQueueSnapshotLockAndReadFailures(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	hkCovLockFile(t, filepath.Join(dispatcher.StateDir, "queue.lock"))
	if _, _, _, err := dispatcher.queueSnapshot(); err == nil {
		t.Fatal("expected queue lock failure")
	}
	if err := os.Remove(filepath.Join(dispatcher.StateDir, "queue.lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dispatcher.pendingDir()); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := dispatcher.queueSnapshot(); err == nil {
		t.Fatal("expected pending directory read failure")
	}
}

func TestHkCovListQueuedSortsAndReportsFindings(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	when := time.Unix(1700000000, 0).UTC()
	alpha := hkCovJob("alpha", "/tmp/checkout-alpha", "b")
	alpha.UpdatedAt = when
	beta := hkCovJob("beta", "/tmp/checkout-beta", "b")
	beta.UpdatedAt = when
	newer := hkCovJob("gamma", "/tmp/checkout-gamma", "b")
	newer.UpdatedAt = when.Add(time.Minute)
	for _, job := range []queuedJob{beta, newer, alpha} {
		hkCovWriteJob(t, dispatcher.pendingDir(), job)
	}
	hkCovWriteFile(t, filepath.Join(dispatcher.pendingDir(), "broken.json"), "{broken", 0o600)
	hkCovWriteFile(t, filepath.Join(dispatcher.pendingDir(), "notes.txt"), "ignore", 0o600)
	if err := os.MkdirAll(filepath.Join(dispatcher.pendingDir(), "nested.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	queued, findings, err := dispatcher.listQueued(dispatcher.pendingDir())
	if err != nil || len(queued) != 3 || len(findings) != 1 || !strings.Contains(findings[0], "decode lifecycle hook queue item") {
		t.Fatalf("queued=%+v findings=%v err=%v", queued, findings, err)
	}
	if queued[0].Executor != "alpha" || queued[1].Executor != "beta" || queued[2].Executor != "gamma" {
		t.Fatalf("order=%s,%s,%s", queued[0].Executor, queued[1].Executor, queued[2].Executor)
	}

	if _, _, err := dispatcher.listQueued(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("expected missing directory failure")
	}
}

func TestHkCovQueueSnapshotReturnsPendingAndRunning(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	hkCovWriteJob(t, dispatcher.pendingDir(), hkCovJob("index", "/tmp/checkout-pending", "b"))
	hkCovWriteJob(t, dispatcher.runningDir(), hkCovJob("index", "/tmp/checkout-running", "b"))
	pending, running, findings, err := dispatcher.queueSnapshot()
	if err != nil || len(pending) != 1 || len(running) != 1 || len(findings) != 0 {
		t.Fatalf("pending=%+v running=%+v findings=%v err=%v", pending, running, findings, err)
	}
}

func TestHkCovRetryRejectsIneligibleReceipts(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if _, err := dispatcher.Retry("a/b"); err == nil || !strings.Contains(err.Error(), "invalid lifecycle hook receipt ID") {
		t.Fatalf("invalid id error=%v", err)
	}
	if _, err := dispatcher.Retry("absent"); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("missing receipt error=%v", err)
	}

	succeeded := hkCovReceipt("succeeded")
	succeeded.Status = "succeeded"
	if err := appendReceipt(dispatcher.ReceiptPath, succeeded); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.Retry(succeeded.ID); err == nil || !strings.Contains(err.Error(), "succeeded, not failed") {
		t.Fatalf("non-failed receipt error=%v", err)
	}
}

func TestHkCovRetryRejectsMissingOrBrokenConfiguration(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dispatcher := hkCovEnvIn(t, root)
	failed := hkCovReceipt("failed")
	if err := appendReceipt(dispatcher.ReceiptPath, failed); err != nil {
		t.Fatal(err)
	}

	broken := dispatcher
	broken.ConfigPath = hkCovWriteFile(t, filepath.Join(root, "broken.yaml"), "hooks: [\n", 0o600)
	if _, err := broken.Retry(failed.ID); err == nil || !strings.Contains(err.Error(), "parse lifecycle hooks config") {
		t.Fatalf("config error=%v", err)
	}

	unconfigured := dispatcher
	unconfigured.ConfigPath = hkCovWriteFile(t, filepath.Join(root, "plain.yaml"), "remote:\n  provider: git\n", 0o600)
	if _, err := unconfigured.Retry(failed.ID); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("unconfigured error=%v", err)
	}

	unknown := dispatcher
	unknown.ConfigPath = hkCovWriteFile(t, filepath.Join(root, "unknown.yaml"), hkCovValidConfigYAML(hkCovWriteFile(t, filepath.Join(root, "bin", "indexer"), "#!/bin/sh\n", 0o755)), 0o600)
	ghost := hkCovReceipt("ghost-failure")
	ghost.Executor = "ghost"
	if err := appendReceipt(unknown.ReceiptPath, ghost); err != nil {
		t.Fatal(err)
	}
	if _, err := unknown.Retry(ghost.ID); err == nil || !strings.Contains(err.Error(), `executor "ghost" is no longer configured`) {
		t.Fatalf("unknown executor error=%v", err)
	}
}

func TestHkCovRetrySurfacesEnqueueAndWorkerFailures(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	failed := hkCovReceipt("retry-me")
	if err := appendReceipt(dispatcher.ReceiptPath, failed); err != nil {
		t.Fatal(err)
	}

	blocked := dispatcher
	blocker := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)
	blocked.StateDir = filepath.Join(blocker, "state")
	if _, err := blocked.Retry(failed.ID); err == nil || !strings.Contains(err.Error(), "create lifecycle hook state") {
		t.Fatalf("enqueue error=%v", err)
	}

	warned := dispatcher
	warned.LaunchWorker = func(WorkerRequest) error { return errors.New("spawn denied") }
	report, err := warned.Retry(failed.ID)
	if err != nil || report.Enqueued != 1 || !containsText(report.Warnings, "background worker did not start") {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestHkCovRetryEnqueuesFailedReceipt(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	failed := hkCovReceipt("retry-ok")
	failed.Checkout = checkout
	if err := appendReceipt(dispatcher.ReceiptPath, failed); err != nil {
		t.Fatal(err)
	}
	report, err := dispatcher.Retry(failed.ID)
	if err != nil || report.Enqueued != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	status, err := dispatcher.Status(5)
	if err != nil || len(status.Pending) != 1 || status.Pending[0].Event.Cause != "retry:"+failed.ID {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestHkCovCheckReportsConfigurationAndExecutors(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	report, err := dispatcher.Check()
	if err != nil || !report.Configured || len(report.Executors) != 1 || report.Executors[0].Status != "ready" || report.Executors[0].Resolved == "" {
		t.Fatalf("report=%+v err=%v", report, err)
	}

	broken := dispatcher
	broken.ConfigPath = hkCovWriteFile(t, filepath.Join(t.TempDir(), "broken.yaml"), "hooks: [\n", 0o600)
	if _, err := broken.Check(); err == nil || !strings.Contains(err.Error(), "parse lifecycle hooks config") {
		t.Fatalf("parse error=%v", err)
	}

	unconfigured := dispatcher
	unconfigured.ConfigPath = hkCovWriteFile(t, filepath.Join(t.TempDir(), "plain.yaml"), "remote:\n  provider: git\n", 0o600)
	report, err = unconfigured.Check()
	if err != nil || report.Configured || len(report.Executors) != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestHkCovCheckReportsUnusableExecutable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	executable := hkCovWriteFile(t, filepath.Join(root, "indexer"), "#!/bin/sh\n", 0o755)
	config := hkCovWriteFile(t, filepath.Join(root, "wb.yaml"), hkCovValidConfigYAML("tools/indexer"), 0o600)
	dispatcher := hkCovDispatcherFor(t, root, config)
	report, err := dispatcher.Check()
	if err != nil || len(report.Findings) != 1 || report.Executors[0].Status != "failed" {
		t.Fatalf("report=%+v err=%v", report, err)
	}

	valid := hkCovWriteFile(t, filepath.Join(root, "valid.yaml"), hkCovValidConfigYAML(executable), 0o600)
	dispatcher.ConfigPath = valid
	report, err = dispatcher.Check()
	if err != nil || len(report.Findings) != 0 || report.Executors[0].Status != "ready" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestHkCovStatusReportsWorkerLockFailure(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	hkCovLockFile(t, filepath.Join(dispatcher.StateDir, "worker.lock"))
	if _, err := dispatcher.Status(1); err == nil {
		t.Fatal("expected worker lock failure")
	}
}
