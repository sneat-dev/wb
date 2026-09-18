package lifecyclehooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func hkCovJob(executor, checkout, newSHA string) queuedJob {
	when := time.Unix(1700000000, 0).UTC()
	return queuedJob{
		SchemaVersion: jobSchemaVersion,
		Key:           queueKey(executor, checkout),
		Executor:      executor,
		Event: Event{
			Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: checkout,
			OldSHA: "a", NewSHA: newSHA, Cause: "pull",
		},
		CoalescedCount: 1,
		QueuedAt:       when,
		UpdatedAt:      when,
	}
}

func hkCovReceipt(id string) Receipt {
	return Receipt{
		SchemaVersion: receiptSchemaVersion, ID: id, Status: "failed", Event: EventCheckoutUpdated,
		Repository: "github.com/acme/app", Executor: "index", NewSHA: "b",
		FinishedAt: time.Unix(1700000000, 0).UTC(),
	}
}

func hkCovWriteJob(t *testing.T, directory string, job queuedJob) string {
	t.Helper()
	path := filepath.Join(directory, queueFileName(job.Key))
	if err := writeJSONAtomic(path, job, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHkCovEnqueueRejectsUnusableStateDirectories(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	item := pending{event: hkCovEvent(checkout), name: "index", count: 1}

	file := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)
	nested := dispatcher
	nested.StateDir = filepath.Join(file, "state")
	if _, err := nested.enqueue(item); err == nil || !strings.Contains(err.Error(), "create lifecycle hook state") {
		t.Fatalf("state dir under a file error=%v", err)
	}

	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "state-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	symlinked := dispatcher
	symlinked.StateDir = link
	if _, err := symlinked.enqueue(item); err == nil || !strings.Contains(err.Error(), "protect lifecycle hook state") {
		t.Fatalf("symlinked state dir error=%v", err)
	}
}

func TestHkCovEnqueueSurfacesLockAndDecodeFailures(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	item := pending{event: hkCovEvent(checkout), name: "index", count: 1}
	if err := os.MkdirAll(dispatcher.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hkCovLockFile(t, filepath.Join(dispatcher.StateDir, "queue.lock"))
	if _, err := dispatcher.enqueue(item); err == nil || !strings.Contains(err.Error(), "lock lifecycle hook queue") {
		t.Fatalf("queue lock directory error=%v", err)
	}
	if err := os.Remove(filepath.Join(dispatcher.StateDir, "queue.lock")); err != nil {
		t.Fatal(err)
	}

	hkCovWriteFile(t, filepath.Join(dispatcher.pendingDir(), queueFileName(queueKey("index", checkout))), "{broken", 0o600)
	if _, err := dispatcher.enqueue(item); err == nil || !strings.Contains(err.Error(), "decode lifecycle hook queue item") {
		t.Fatalf("corrupt pending item error=%v", err)
	}
}

func TestHkCovEnqueueCoalescesOntoExistingPendingJob(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	first := pending{event: hkCovEvent(checkout), name: "index", count: 1}
	if coalesced, err := dispatcher.enqueue(first); err != nil || coalesced != 0 {
		t.Fatalf("first enqueue coalesced=%d err=%v", coalesced, err)
	}
	second := pending{event: hkCovEvent(checkout), name: "index", count: 3}
	coalesced, err := dispatcher.enqueue(second)
	if err != nil || coalesced != 3 {
		t.Fatalf("second enqueue coalesced=%d err=%v, want 3", coalesced, err)
	}
	status, err := dispatcher.Status(20)
	if err != nil || len(status.Pending) != 1 || status.Pending[0].CoalescedCount != 4 {
		t.Fatalf("pending=%+v err=%v", status.Pending, err)
	}
}

func TestHkCovDrainClampsParallelismAndReleasesIdleWorker(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	for _, parallel := range []int{0, -1, 17, 64} {
		report, err := dispatcher.Drain(context.Background(), parallel)
		if err != nil || report.Executed != 0 || report.Enqueued != 0 {
			t.Fatalf("parallel=%d report=%+v err=%v", parallel, report, err)
		}
	}
	health, err := dispatcher.readWorkerHealth()
	if err != nil || health == nil || health.Status != "idle" {
		t.Fatalf("health=%+v err=%v", health, err)
	}
}

func TestHkCovDrainReportsStateAndLockFailures(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	file := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)

	broken := dispatcher
	broken.StateDir = filepath.Join(file, "state")
	if _, err := broken.Drain(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "create lifecycle hook state") {
		t.Fatalf("state dir error=%v", err)
	}

	if err := os.MkdirAll(dispatcher.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hkCovLockFile(t, filepath.Join(dispatcher.StateDir, "worker.lock"))
	if _, err := dispatcher.Drain(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "lock lifecycle hook worker") {
		t.Fatalf("worker lock dir error=%v", err)
	}
}

func TestHkCovDrainReturnsQuietlyWhenWorkerLockHeld(t *testing.T) {
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
	report, err := dispatcher.Drain(context.Background(), 1)
	if err != nil || report.Executed != 0 || report.Enqueued != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestHkCovDrainRecordsFailedHealthWhenStartCannotBeWritten(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := os.MkdirAll(filepath.Join(dispatcher.StateDir, "worker-health.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dispatcher.StateDir, "worker-health.json", "child"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.Drain(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "record lifecycle hook worker start") {
		t.Fatalf("error=%v", err)
	}
}

func TestHkCovDrainMarksWorkerFailedAndUnlocksOnRecoveryError(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	hkCovLockFile(t, filepath.Join(dispatcher.StateDir, "queue.lock"))
	if _, err := dispatcher.Drain(context.Background(), 1); err == nil {
		t.Fatal("expected queue lock failure to propagate")
	}
	health, err := dispatcher.readWorkerHealth()
	if err != nil || health == nil || health.Status != "failed" || health.Message == "" {
		t.Fatalf("health=%+v err=%v, want failed worker with message", health, err)
	}
	// The worker lock must have been released so a later drain can take it.
	if err := os.Remove(filepath.Join(dispatcher.StateDir, "queue.lock")); err != nil {
		t.Fatal(err)
	}
	if report, err := dispatcher.Drain(context.Background(), 1); err != nil || report.Executed != 0 {
		t.Fatalf("second drain report=%+v err=%v", report, err)
	}
}

func TestHkCovDrainReportsErrorWhenCompletionHealthCannotBeWritten(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	healthPath := dispatcher.workerHealthPath()
	dispatcher.Run = func(context.Context, Invocation) error {
		if err := os.Remove(healthPath); err != nil {
			return err
		}
		return os.MkdirAll(healthPath, 0o700)
	}
	hkCovEnqueueOne(t, dispatcher, checkout)
	_, err := dispatcher.Drain(context.Background(), 1)
	if err == nil || !strings.Contains(err.Error(), "record lifecycle hook worker completion") {
		t.Fatalf("error=%v, want completion health failure", err)
	}
}

func TestHkCovDrainWarnsWhenReceiptCannotBeRecorded(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	blocker := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)
	dispatcher.ReceiptPath = filepath.Join(blocker, "receipts.jsonl")
	dispatcher.Run = func(context.Context, Invocation) error { return nil }
	hkCovSeedJob(t, dispatcher, hkCovJob("index", checkout, "b"))
	report, err := dispatcher.Drain(context.Background(), 1)
	if err != nil || report.Executed != 1 || !containsText(report.Warnings, "record lifecycle hook receipt") {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if len(report.Warnings) != 1 {
		t.Fatalf("warnings=%v", report.Warnings)
	}
}

func TestHkCovDrainSkipsNonJobEntriesAndRenamesToRunning(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	dispatcher.Run = func(context.Context, Invocation) error { return nil }
	hkCovEnqueueOne(t, dispatcher, checkout)
	hkCovWriteFile(t, filepath.Join(dispatcher.pendingDir(), "notes.txt"), "ignore me", 0o600)
	if err := os.MkdirAll(filepath.Join(dispatcher.pendingDir(), "subdir.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := dispatcher.Drain(context.Background(), 1)
	if err != nil || report.Executed != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	entries, err := os.ReadDir(dispatcher.pendingDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("pending entries=%v, want the ignored entries kept", entries)
	}
}

func TestHkCovDrainReportsClaimRenameFailure(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	hkCovEnqueueOne(t, dispatcher, checkout)
	entries, err := os.ReadDir(dispatcher.pendingDir())
	if err != nil || len(entries) != 1 {
		t.Fatalf("pending entries=%d err=%v", len(entries), err)
	}
	// A non-empty directory at the running destination makes the rename fail.
	blocked := filepath.Join(dispatcher.runningDir(), entries[0].Name())
	if err := os.MkdirAll(filepath.Join(blocked, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.Drain(context.Background(), 1); err == nil {
		t.Fatal("expected claim rename failure")
	}
}

func TestHkCovRecoverRunningSkipsNonJobEntries(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dispatcher.runningDir(), "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	hkCovWriteFile(t, filepath.Join(dispatcher.runningDir(), "notes.txt"), "ignore", 0o600)
	warnings, err := dispatcher.recoverRunning()
	if err != nil || len(warnings) != 0 {
		t.Fatalf("warnings=%v err=%v", warnings, err)
	}
}

func TestHkCovRecoverRunningQuarantinesInvalidRunningItem(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	hkCovWriteFile(t, filepath.Join(dispatcher.runningDir(), "bad.json"), "{broken", 0o600)
	warnings, err := dispatcher.recoverRunning()
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "quarantined invalid running lifecycle-hook item") {
		t.Fatalf("warnings=%v err=%v", warnings, err)
	}
	entries, err := os.ReadDir(dispatcher.quarantineDir())
	if err != nil || len(entries) != 2 {
		t.Fatalf("quarantine entries=%v err=%v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(dispatcher.runningDir(), "bad.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid running item still present: %v", err)
	}
}

func TestHkCovRecoverRunningMergesIntoExistingPendingItem(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	running := hkCovJob("index", checkout, "newer")
	running.QueuedAt = time.Unix(1600000000, 0).UTC()
	running.CoalescedCount = 2
	hkCovWriteJob(t, dispatcher.runningDir(), running)
	pendingJob := hkCovJob("index", checkout, "older")
	pendingJob.CoalescedCount = 3
	hkCovWriteJob(t, dispatcher.pendingDir(), pendingJob)

	warnings, err := dispatcher.recoverRunning()
	if err != nil || len(warnings) != 0 {
		t.Fatalf("warnings=%v err=%v", warnings, err)
	}
	merged, err := readJob(filepath.Join(dispatcher.pendingDir(), queueFileName(pendingJob.Key)))
	if err != nil {
		t.Fatal(err)
	}
	if merged.CoalescedCount != 5 || merged.Event.NewSHA != "older" || !merged.QueuedAt.Equal(running.QueuedAt) {
		t.Fatalf("merged=%+v", merged)
	}
	if _, err := os.Stat(filepath.Join(dispatcher.runningDir(), queueFileName(pendingJob.Key))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("running item still present: %v", err)
	}
}

func TestHkCovRecoverRunningQuarantinesCorruptPendingAndRestoresRunning(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	running := hkCovJob("index", checkout, "new")
	name := queueFileName(running.Key)
	hkCovWriteJob(t, dispatcher.runningDir(), running)
	hkCovWriteFile(t, filepath.Join(dispatcher.pendingDir(), name), "{broken", 0o600)

	warnings, err := dispatcher.recoverRunning()
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "quarantined invalid pending lifecycle-hook item") {
		t.Fatalf("warnings=%v err=%v", warnings, err)
	}
	restored, err := readJob(filepath.Join(dispatcher.pendingDir(), name))
	if err != nil || restored.Event.NewSHA != "new" {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}
	if _, err := os.Stat(filepath.Join(dispatcher.runningDir(), name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("running item still present: %v", err)
	}
}

func TestHkCovRecoverRunningMovesRunningWithoutPending(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	running := hkCovJob("index", checkout, "orphan")
	name := queueFileName(running.Key)
	hkCovWriteJob(t, dispatcher.runningDir(), running)
	warnings, err := dispatcher.recoverRunning()
	if err != nil || len(warnings) != 0 {
		t.Fatalf("warnings=%v err=%v", warnings, err)
	}
	moved, err := readJob(filepath.Join(dispatcher.pendingDir(), name))
	if err != nil || moved.Event.NewSHA != "orphan" {
		t.Fatalf("moved=%+v err=%v", moved, err)
	}
}

func TestHkCovClaimBatchLockAndReadFailures(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	worker := flock.New(filepath.Join(dispatcher.StateDir, "worker.lock"))
	hkCovLockFile(t, filepath.Join(dispatcher.StateDir, "queue.lock"))
	if _, _, _, err := dispatcher.claimBatch(1, worker); err == nil {
		t.Fatal("expected queue lock failure")
	}
	if err := os.Remove(filepath.Join(dispatcher.StateDir, "queue.lock")); err != nil {
		t.Fatal(err)
	}
	detached := dispatcher
	detached.StateDir = filepath.Join(t.TempDir(), "absent-state")
	if err := os.MkdirAll(detached.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := detached.claimBatch(1, worker); err == nil {
		t.Fatal("expected pending directory read failure")
	}
}

func TestHkCovClaimBatchReleasesIdleWorker(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	worker := flock.New(filepath.Join(dispatcher.StateDir, "worker.lock"))
	if owned, err := worker.TryLock(); err != nil || !owned {
		t.Fatalf("owned=%t err=%v", owned, err)
	}
	jobs, released, warnings, err := dispatcher.claimBatch(1, worker)
	if err != nil || !released || len(jobs) != 0 || len(warnings) != 0 {
		t.Fatalf("jobs=%v released=%t warnings=%v err=%v", jobs, released, warnings, err)
	}
	if owned, err := worker.TryLock(); err != nil || !owned {
		t.Fatalf("worker lock still held after release: owned=%t err=%v", owned, err)
	}
}

func TestHkCovRunJobConfigurationFailures(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	executable := hkCovWriteFile(t, filepath.Join(root, "indexer"), "#!/bin/sh\nexit 0\n", 0o755)
	checkout := hkCovCheckout(t, root)

	t.Run("unparseable config", func(t *testing.T) {
		t.Parallel()
		config := hkCovWriteFile(t, filepath.Join(root, "bad.yaml"), "hooks: [\n", 0o600)
		dispatcher := hkCovDispatcherFor(t, root, config)
		result := dispatcher.runJob(context.Background(), hkCovJob("index", checkout, "b"))
		if result.receipt.Status != "failed" || result.receipt.Failure != "configuration" || !strings.Contains(result.receipt.Message, "parse") {
			t.Fatalf("receipt=%+v", result.receipt)
		}
	})

	t.Run("config removed", func(t *testing.T) {
		t.Parallel()
		dispatcher := hkCovDispatcherFor(t, root, filepath.Join(root, "absent.yaml"))
		result := dispatcher.runJob(context.Background(), hkCovJob("index", checkout, "b"))
		if result.receipt.Status != "failed" || !strings.Contains(result.receipt.Message, "no longer exists") {
			t.Fatalf("receipt=%+v", result.receipt)
		}
	})

	t.Run("executor removed", func(t *testing.T) {
		t.Parallel()
		config := hkCovWriteFile(t, filepath.Join(root, "wb.yaml"), hkCovValidConfigYAML(executable), 0o600)
		dispatcher := hkCovDispatcherFor(t, root, config)
		result := dispatcher.runJob(context.Background(), hkCovJob("ghost", checkout, "b"))
		if result.receipt.Status != "failed" || !strings.Contains(result.receipt.Message, `executor "ghost" is no longer configured`) {
			t.Fatalf("receipt=%+v", result.receipt)
		}
	})
}

func TestHkCovRunJobTimesOutAndClassifiesDeadline(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	executable := hkCovWriteFile(t, filepath.Join(root, "indexer"), "#!/bin/sh\nexit 0\n", 0o755)
	checkout := hkCovCheckout(t, root)
	config := hkCovWriteFile(t, filepath.Join(root, "wb.yaml"), strings.Replace(hkCovValidConfigYAML(executable), "timeout: 2m", "timeout: 1ns", 1), 0o600)
	dispatcher := hkCovDispatcherFor(t, root, config)
	dispatcher.Run = func(ctx context.Context, _ Invocation) error {
		<-ctx.Done()
		return ctx.Err()
	}
	result := dispatcher.runJob(context.Background(), hkCovJob("index", checkout, "b"))
	if result.receipt.Status != "failed" || result.receipt.Failure != "timeout" {
		t.Fatalf("receipt=%+v", result.receipt)
	}
	if failureClass(context.DeadlineExceeded) != "timeout" {
		t.Fatal("deadline must classify as timeout")
	}
}

func TestHkCovCappedFileTruncatesAndSurfacesWriteErrors(t *testing.T) {
	t.Parallel()
	saturated := &cappedFile{written: maxDiagnosticBytes}
	written, err := saturated.Write([]byte("more"))
	if err != nil || written != 4 || !saturated.truncated {
		t.Fatalf("written=%d truncated=%t err=%v", written, saturated.truncated, err)
	}

	path := filepath.Join(t.TempDir(), "log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	closed := &cappedFile{file: file}
	if _, err := closed.Write([]byte("data")); err == nil {
		t.Fatal("writing to a closed diagnostic file must fail")
	}
}

func TestHkCovCappedFilePartialWrite(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	writer := &cappedFile{file: file, written: maxDiagnosticBytes - 2}
	written, err := writer.Write([]byte("abcd"))
	if err != nil || written != 4 || !writer.truncated || writer.written != maxDiagnosticBytes {
		t.Fatalf("written=%d truncated=%t total=%d err=%v", written, writer.truncated, writer.written, err)
	}
	info, err := file.Stat()
	if err != nil || info.Size() != 2 {
		t.Fatalf("size=%v err=%v", info, err)
	}
}

func TestHkCovOpenDiagnosticsFailures(t *testing.T) {
	t.Parallel()
	blocker := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)
	if _, _, err := openDiagnostics(filepath.Join(blocker, "diag")); err == nil {
		t.Fatal("expected MkdirAll failure under a regular file")
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "stdout.log"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openDiagnostics(root); err == nil {
		t.Fatal("expected stdout open failure")
	}

	root2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root2, "stderr.log"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openDiagnostics(root2); err == nil {
		t.Fatal("expected stderr open failure")
	}
	if _, err := os.Stat(filepath.Join(root2, "stdout.log")); err != nil {
		t.Fatalf("stdout.log should have been created and closed: %v", err)
	}
}

func TestHkCovOpenDiagnosticsCreatesPrivateLogs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stdout, stderr, err := openDiagnostics(filepath.Join(root, "diag"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdout.file.Close() }()
	defer func() { _ = stderr.file.Close() }()
	for _, path := range []string{stdout.file.Name(), stderr.file.Name()} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("diagnostic %s info=%v err=%v", path, info, err)
		}
	}
}

func TestHkCovBoundedMessageCollapsesAndTruncates(t *testing.T) {
	t.Parallel()
	if got := boundedMessage("  line one\nline two  ", 100); got != "line one line two" {
		t.Fatalf("bounded=%q", got)
	}
	if got := boundedMessage(strings.Repeat("x", 10), 4); got != "xxxx…" {
		t.Fatalf("truncated=%q", got)
	}
}

func TestHkCovCompleteLockAndRemoveFailures(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	job := hkCovJob("index", checkout, "b")
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	hkCovLockFile(t, filepath.Join(dispatcher.StateDir, "queue.lock"))
	if err := dispatcher.complete(job); err == nil {
		t.Fatal("expected queue lock failure")
	}
	if err := os.Remove(filepath.Join(dispatcher.StateDir, "queue.lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dispatcher.runningDir(), queueFileName(job.Key), "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.complete(job); err == nil {
		t.Fatal("expected removal failure for a non-empty directory")
	}
}

func TestHkCovCompleteIgnoresMissingJob(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.complete(hkCovJob("index", checkout, "b")); err != nil {
		t.Fatalf("completing an absent job must succeed: %v", err)
	}
}

func TestHkCovReadJobRejectsInvalidRecords(t *testing.T) {
	t.Parallel()
	job := hkCovJob("index", "/tmp/checkout", "b")
	cases := map[string]queuedJob{}
	broken := job
	broken.SchemaVersion = 9
	cases["wrong schema"] = broken
	noKey := job
	noKey.Key = ""
	cases["empty key"] = noKey
	tampered := job
	tampered.Key = "other"
	cases["tampered key"] = tampered
	noCheckout := job
	noCheckout.Event.Checkout = ""
	cases["empty checkout"] = noCheckout
	noExecutor := job
	noExecutor.Executor = ""
	cases["empty executor"] = noExecutor

	for name, invalid := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := hkCovWriteFile(t, filepath.Join(t.TempDir(), "job.json"), hkCovMustJSON(t, invalid), 0o600)
			if _, err := readJob(path); err == nil {
				t.Fatalf("expected %s to be rejected", name)
			}
		})
	}

	t.Run("broken json", func(t *testing.T) {
		t.Parallel()
		path := hkCovWriteFile(t, filepath.Join(t.TempDir(), "job.json"), "{broken", 0o600)
		if _, err := readJob(path); err == nil {
			t.Fatal("expected broken JSON to be rejected")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		t.Parallel()
		if _, err := readJob(filepath.Join(t.TempDir(), "absent.json")); err == nil {
			t.Fatal("expected error for missing file")
		}
	})
}

func TestHkCovReadJobAcceptsValidRecord(t *testing.T) {
	t.Parallel()
	job := hkCovJob("index", "/tmp/checkout", "b")
	path := hkCovWriteFile(t, filepath.Join(t.TempDir(), "job.json"), hkCovMustJSON(t, job), 0o600)
	got, err := readJob(path)
	if err != nil || got.Key != job.Key || got.Event.NewSHA != "b" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func hkCovMustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestHkCovWriteJSONAtomicFailures(t *testing.T) {
	t.Parallel()
	if err := writeJSONAtomic(filepath.Join(t.TempDir(), "x.json"), make(chan int), 0o600); err == nil {
		t.Fatal("expected marshal failure")
	}
	blocker := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)
	if err := writeJSONAtomic(filepath.Join(blocker, "x.json"), map[string]int{"a": 1}, 0o600); err == nil {
		t.Fatal("expected MkdirAll failure")
	}
	destination := filepath.Join(t.TempDir(), "x.json")
	if err := os.MkdirAll(filepath.Join(destination, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(destination, map[string]int{"a": 1}, 0o600); err == nil {
		t.Fatal("expected rename failure onto a non-empty directory")
	}
}

func TestHkCovWriteJSONAtomicWritesPrivateFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "value.json")
	if err := writeJSONAtomic(path, map[string]string{"k": "v"}, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), `"k":"v"`) {
		t.Fatalf("raw=%q err=%v", raw, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestHkCovQuarantineFileFailures(t *testing.T) {
	t.Parallel()
	t.Run("quarantine path is a regular file", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		dispatcher := hkCovDispatcherFor(t, root, filepath.Join(root, "wb.yaml"))
		dispatcher.StateDir = filepath.Join(root, "state")
		source := hkCovWriteFile(t, filepath.Join(root, "item.json"), "{}", 0o600)
		hkCovWriteFile(t, filepath.Join(dispatcher.StateDir, "quarantine"), "x", 0o600)
		if _, err := dispatcher.quarantineFile(source, "because"); err == nil {
			t.Fatal("expected quarantine directory creation failure")
		}
	})

	t.Run("quarantine path is a symlink", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		dispatcher := hkCovDispatcherFor(t, root, filepath.Join(root, "wb.yaml"))
		dispatcher.StateDir = filepath.Join(root, "state")
		if err := os.MkdirAll(dispatcher.StateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		source := hkCovWriteFile(t, filepath.Join(root, "item.json"), "{}", 0o600)
		if err := os.Symlink(t.TempDir(), filepath.Join(dispatcher.StateDir, "quarantine")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := dispatcher.quarantineFile(source, "because"); err == nil || !strings.Contains(err.Error(), "non-symlink") {
			t.Fatalf("symlinked quarantine error=%v", err)
		}
	})

	t.Run("source vanished", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		dispatcher := hkCovDispatcherFor(t, root, filepath.Join(root, "wb.yaml"))
		dispatcher.StateDir = filepath.Join(root, "state")
		if err := os.MkdirAll(dispatcher.StateDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := dispatcher.quarantineFile(filepath.Join(root, "vanished.json"), "because"); err == nil {
			t.Fatal("expected rename failure for a missing source")
		}
	})
}

func TestHkCovQuarantineFileWritesReasonAndPrivateDestination(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dispatcher := hkCovDispatcherFor(t, root, filepath.Join(root, "wb.yaml"))
	dispatcher.StateDir = filepath.Join(root, "state")
	dispatcher.Now = func() time.Time { return time.Unix(1700000000, 0).UTC() }
	source := hkCovWriteFile(t, filepath.Join(root, "item.json"), "{}", 0o600)
	destination, err := dispatcher.quarantineFile(source, "  bad\nstuff  ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(destination, ".bad") {
		t.Fatalf("destination=%q", destination)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("info=%v err=%v", info, err)
	}
	reason, err := os.ReadFile(destination + ".reason.txt")
	if err != nil || strings.TrimSpace(string(reason)) != "bad stuff" {
		t.Fatalf("reason=%q err=%v", reason, err)
	}
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source still present: %v", err)
	}
}

func TestHkCovAppendReceiptRejectsMalformedInput(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := appendReceipt(dispatcher.ReceiptPath, Receipt{ID: "id"}); err == nil || !strings.Contains(err.Error(), "unsupported receipt schema version") {
		t.Fatalf("schema error=%v", err)
	}

	blocker := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)
	blocked := dispatcher
	blocked.ReceiptPath = filepath.Join(blocker, "receipts.jsonl")
	if err := appendReceipt(blocked.ReceiptPath, hkCovReceipt("id")); err == nil {
		t.Fatal("expected trusted parent failure")
	}
}

func TestHkCovAppendReceiptRejectsUntrustedIndexDirectory(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	root := t.TempDir()
	dispatcher.ReceiptPath = filepath.Join(root, "receipts.jsonl")
	hkCovWriteFile(t, receiptIndexDir(dispatcher.ReceiptPath), "not a dir", 0o600)
	if err := appendReceipt(dispatcher.ReceiptPath, hkCovReceipt("id")); err == nil {
		t.Fatal("expected index directory creation failure")
	}

	dispatcher2, _ := hkCovEnv(t)
	root2 := t.TempDir()
	dispatcher2.ReceiptPath = filepath.Join(root2, "receipts.jsonl")
	target := t.TempDir()
	if err := os.Symlink(target, receiptIndexDir(dispatcher2.ReceiptPath)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := appendReceipt(dispatcher2.ReceiptPath, hkCovReceipt("id2")); err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("symlinked index error=%v", err)
	}
}

func TestHkCovAppendReceiptRejectsUntrustedStreamAndLock(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	root := t.TempDir()
	dispatcher.ReceiptPath = filepath.Join(root, "receipts.jsonl")
	target := hkCovWriteFile(t, filepath.Join(root, "target.jsonl"), "", 0o600)
	if err := os.Symlink(target, dispatcher.ReceiptPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := appendReceipt(dispatcher.ReceiptPath, hkCovReceipt("id")); err == nil || !strings.Contains(err.Error(), "regular non-symlink") {
		t.Fatalf("symlinked stream error=%v", err)
	}

	dispatcher2, _ := hkCovEnv(t)
	root2 := t.TempDir()
	dispatcher2.ReceiptPath = filepath.Join(root2, "receipts.jsonl")
	hkCovLockFile(t, dispatcher2.ReceiptPath+".lock")
	if err := appendReceipt(dispatcher2.ReceiptPath, hkCovReceipt("id")); err == nil {
		t.Fatal("expected receipt lock failure")
	}
}

func TestHkCovAppendReceiptIndexWriteFailure(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	receipt := hkCovReceipt("hk-index")
	if err := appendReceipt(dispatcher.ReceiptPath, receipt); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(receiptIndexDir(dispatcher.ReceiptPath), receipt.ID+".json")
	if err := os.Remove(indexPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(indexPath, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := appendReceipt(dispatcher.ReceiptPath, receipt); err == nil || !strings.Contains(err.Error(), "index lifecycle hook receipt") {
		t.Fatalf("index error=%v", err)
	}
}

func TestHkCovAppendReceiptWritesIndexAndStream(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	receipt := hkCovReceipt("hk-written")
	if err := appendReceipt(dispatcher.ReceiptPath, receipt); err != nil {
		t.Fatal(err)
	}
	indexed, err := os.ReadFile(filepath.Join(receiptIndexDir(dispatcher.ReceiptPath), receipt.ID+".json"))
	if err != nil || !strings.Contains(string(indexed), receipt.ID) {
		t.Fatalf("indexed=%q err=%v", indexed, err)
	}
	stream, err := os.ReadFile(dispatcher.ReceiptPath)
	if err != nil || !strings.Contains(string(stream), receipt.ID) {
		t.Fatalf("stream=%q err=%v", stream, err)
	}
}

func TestHkCovSyncQueueDirectoriesPropagatesFailure(t *testing.T) {
	t.Parallel()
	if err := syncQueueDirectories(t.TempDir()); err != nil {
		t.Fatalf("valid directory error=%v", err)
	}
	if err := syncQueueDirectories(t.TempDir(), filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("expected failure for a missing directory")
	}
}

func TestHkCovReadRecentReceiptsKeepsNewestAndHonoursLimit(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	for _, id := range []string{"a", "b", "c"} {
		if err := appendReceipt(dispatcher.ReceiptPath, hkCovReceipt(id)); err != nil {
			t.Fatal(err)
		}
	}
	receipts, findings, err := readRecentReceipts(dispatcher.ReceiptPath, 2)
	if err != nil || len(findings) != 0 {
		t.Fatalf("findings=%v err=%v", findings, err)
	}
	if len(receipts) != 2 || receipts[0].ID != "b" || receipts[1].ID != "c" {
		t.Fatalf("receipts=%+v", receipts)
	}
	all, _, err := readRecentReceipts(dispatcher.ReceiptPath, 10)
	if err != nil || len(all) != 3 || all[0].ID != "a" {
		t.Fatalf("all=%+v err=%v", all, err)
	}
}

func TestHkCovFindReceiptRejectsInvalidAndUnreadableIndex(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	for _, id := range []string{"", "a/b", `a\b`} {
		if _, _, err := findReceipt(dispatcher.ReceiptPath, id); err == nil {
			t.Fatalf("id %q should be rejected", id)
		}
	}
	if err := os.MkdirAll(filepath.Join(receiptIndexDir(dispatcher.ReceiptPath), "blocked.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := findReceipt(dispatcher.ReceiptPath, "blocked"); err == nil {
		t.Fatal("expected unreadable index error")
	}
}

func TestHkCovFindReceiptPrefersValidIndex(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	receipt := hkCovReceipt("hk-find")
	if err := appendReceipt(dispatcher.ReceiptPath, receipt); err != nil {
		t.Fatal(err)
	}
	got, found, err := findReceipt(dispatcher.ReceiptPath, receipt.ID)
	if err != nil || !found || got.ID != receipt.ID || got.Status != "failed" {
		t.Fatalf("got=%+v found=%t err=%v", got, found, err)
	}
	if _, found, err := findReceipt(dispatcher.ReceiptPath, "absent"); err != nil || found {
		t.Fatalf("found=%t err=%v", found, err)
	}
}

func TestHkCovScanReceiptsRejectsUntrustedParentAndLock(t *testing.T) {
	t.Parallel()
	blocker := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)
	if _, err := scanReceipts(filepath.Join(blocker, "receipts.jsonl"), func(Receipt) {}); err == nil {
		t.Fatal("expected trusted parent failure")
	}

	dispatcher, _ := hkCovEnv(t)
	hkCovLockFile(t, dispatcher.ReceiptPath+".lock")
	if _, err := scanReceipts(dispatcher.ReceiptPath, func(Receipt) {}); err == nil {
		t.Fatal("expected receipt lock failure")
	}
}

func TestHkCovScanReceiptsReportsInvalidLines(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := appendReceipt(dispatcher.ReceiptPath, hkCovReceipt("good")); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(dispatcher.ReceiptPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("{malformed\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	var seen []string
	findings, err := scanReceipts(dispatcher.ReceiptPath, func(receipt Receipt) { seen = append(seen, receipt.ID) })
	if err != nil || len(seen) != 1 || seen[0] != "good" {
		t.Fatalf("seen=%v err=%v", seen, err)
	}
	if len(findings) != 1 || !strings.Contains(findings[0], "ignored invalid lifecycle hook receipt line 2") {
		t.Fatalf("findings=%v", findings)
	}
}

func TestHkCovValidateReceiptAndDecodeReceipt(t *testing.T) {
	t.Parallel()
	if err := validateReceipt(hkCovReceipt("ok")); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	if err := validateReceipt(Receipt{SchemaVersion: 1, ID: "ok"}); err == nil || !strings.Contains(err.Error(), "unsupported receipt schema version") {
		t.Fatalf("schema error=%v", err)
	}
	for _, id := range []string{"", "a/b", `a\b`} {
		if err := validateReceipt(Receipt{SchemaVersion: receiptSchemaVersion, ID: id}); err == nil {
			t.Fatalf("id %q should be rejected", id)
		}
	}
	if _, err := decodeReceipt([]byte(`{"schema_version":9,"id":"x"}`)); err == nil {
		t.Fatal("expected decode to reject an unsupported schema")
	}
	if _, err := decodeReceipt([]byte(`not-json`)); err == nil {
		t.Fatal("expected decode to reject malformed JSON")
	}
	decoded, err := decodeReceipt([]byte(hkCovMustJSON(t, hkCovReceipt("decoded"))))
	if err != nil || decoded.ID != "decoded" {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
}

func TestHkCovRecoverRunningReportsMissingDirectory(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	if err := os.MkdirAll(dispatcher.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.recoverRunning(); err == nil {
		t.Fatal("expected missing running directory failure")
	}
}

func TestHkCovRecoverRunningReportsRunningQuarantineFailure(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	hkCovWriteFile(t, filepath.Join(dispatcher.runningDir(), "bad.json"), "{broken", 0o600)
	hkCovWriteFile(t, dispatcher.quarantineDir(), "not a dir", 0o600)
	if _, err := dispatcher.recoverRunning(); err == nil {
		t.Fatal("expected quarantine failure for an invalid running item")
	}
}

func TestHkCovRecoverRunningReportsPendingQuarantineFailure(t *testing.T) {
	t.Parallel()
	dispatcher, checkout := hkCovEnv(t)
	job := hkCovJob("index", checkout, "new")
	hkCovWriteJob(t, dispatcher.runningDir(), job)
	hkCovWriteFile(t, filepath.Join(dispatcher.pendingDir(), queueFileName(job.Key)), "{broken", 0o600)
	hkCovWriteFile(t, dispatcher.quarantineDir(), "not a dir", 0o600)
	if _, err := dispatcher.recoverRunning(); err == nil {
		t.Fatal("expected quarantine failure for an invalid pending item")
	}
}

func TestHkCovClaimBatchReportsQuarantineFailure(t *testing.T) {
	t.Parallel()
	dispatcher, _ := hkCovEnv(t)
	hkCovWriteFile(t, filepath.Join(dispatcher.pendingDir(), "bad.json"), "{broken", 0o600)
	hkCovWriteFile(t, dispatcher.quarantineDir(), "not a dir", 0o600)
	worker := flock.New(filepath.Join(dispatcher.StateDir, "worker.lock"))
	if _, _, _, err := dispatcher.claimBatch(1, worker); err == nil {
		t.Fatal("expected quarantine failure while claiming a corrupt job")
	}
}
