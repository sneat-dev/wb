package lifecyclehooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHkCovReadWorkerHealthFailures(t *testing.T) {
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	if health, err := dispatcher.readWorkerHealth(); err != nil || health != nil {
		t.Fatalf("missing health=%+v err=%v", health, err)
	}

	if err := os.MkdirAll(dispatcher.workerHealthPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.readWorkerHealth(); err == nil {
		t.Fatal("expected unreadable health record to fail")
	}
	if err := os.Remove(dispatcher.workerHealthPath()); err != nil {
		t.Fatal(err)
	}

	hkCovWriteFile(t, dispatcher.workerHealthPath(), `{"schema_version":1}`, 0o600)
	if _, err := dispatcher.readWorkerHealth(); err == nil || !strings.Contains(err.Error(), "invalid lifecycle worker health record") {
		t.Fatalf("empty status error=%v", err)
	}
	hkCovWriteFile(t, dispatcher.workerHealthPath(), `{"schema_version":2,"status":"idle"}`, 0o600)
	if _, err := dispatcher.readWorkerHealth(); err == nil {
		t.Fatal("expected wrong schema version to fail")
	}
	hkCovWriteFile(t, dispatcher.workerHealthPath(), "{broken", 0o600)
	if _, err := dispatcher.readWorkerHealth(); err == nil {
		t.Fatal("expected malformed health record to fail")
	}
}

func TestHkCovStartWorkerIfIdleHonoursDisabledLauncher(t *testing.T) {
	dispatcher, _ := hkCovEnv(t)
	dispatcher.LaunchWorker = nil
	started, err := dispatcher.startWorkerIfIdle()
	if err != nil || started {
		t.Fatalf("started=%t err=%v", started, err)
	}
}

func TestHkCovStartWorkerIfIdleStateAndLockFailures(t *testing.T) {
	dispatcher, _ := hkCovEnv(t)
	blocker := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)
	broken := dispatcher
	broken.StateDir = filepath.Join(blocker, "state")
	if _, err := broken.startWorkerIfIdle(); err == nil || !strings.Contains(err.Error(), "create lifecycle hook state") {
		t.Fatalf("state error=%v", err)
	}

	if err := os.MkdirAll(dispatcher.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hkCovLockFile(t, filepath.Join(dispatcher.StateDir, "worker-start.lock"))
	if _, err := dispatcher.startWorkerIfIdle(); err == nil || !strings.Contains(err.Error(), "coordinate lifecycle hook worker start") {
		t.Fatalf("start lock error=%v", err)
	}
	if err := os.Remove(filepath.Join(dispatcher.StateDir, "worker-start.lock")); err != nil {
		t.Fatal(err)
	}

	hkCovLockFile(t, filepath.Join(dispatcher.StateDir, "worker.lock"))
	if _, err := dispatcher.startWorkerIfIdle(); err == nil || !strings.Contains(err.Error(), "inspect lifecycle hook worker") {
		t.Fatalf("worker lock error=%v", err)
	}
}

func TestHkCovStartWorkerIfIdleQuarantinesInvalidHealth(t *testing.T) {
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	hkCovWriteFile(t, dispatcher.workerHealthPath(), "{broken", 0o600)
	launched := 0
	dispatcher.LaunchWorker = func(WorkerRequest) error { launched++; return nil }
	started, err := dispatcher.startWorkerIfIdle()
	if err != nil || !started || launched != 1 {
		t.Fatalf("started=%t launched=%d err=%v", started, launched, err)
	}
	entries, err := os.ReadDir(dispatcher.quarantineDir())
	if err != nil || len(entries) != 2 {
		t.Fatalf("quarantine entries=%v err=%v", entries, err)
	}
	health, err := dispatcher.readWorkerHealth()
	if err != nil || health == nil || health.Status != "starting" {
		t.Fatalf("health=%+v err=%v", health, err)
	}
}

func TestHkCovStartWorkerIfIdleRecordsLaunchFailure(t *testing.T) {
	dispatcher, _ := hkCovEnv(t)
	dispatcher.LaunchWorker = func(WorkerRequest) error { return errors.New("exec format error") }
	started, err := dispatcher.startWorkerIfIdle()
	if err == nil || started || !strings.Contains(err.Error(), "exec format error") {
		t.Fatalf("started=%t err=%v", started, err)
	}
	health, readErr := dispatcher.readWorkerHealth()
	if readErr != nil || health == nil || health.Status != "failed" || !strings.Contains(health.Message, "worker launch failed") {
		t.Fatalf("health=%+v err=%v", health, readErr)
	}
}

func TestHkCovResumeReportsStatusFailure(t *testing.T) {
	dispatcher, _ := hkCovEnv(t)
	blocker := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)
	dispatcher.StateDir = filepath.Join(blocker, "state")
	if _, err := dispatcher.Resume(); err == nil || !strings.Contains(err.Error(), "create lifecycle hook state") {
		t.Fatalf("resume error=%v", err)
	}
}

func TestHkCovResumeIsNoOpWhenQueueIsEmpty(t *testing.T) {
	dispatcher, _ := hkCovEnv(t)
	launched := 0
	dispatcher.LaunchWorker = func(WorkerRequest) error { launched++; return nil }
	report, err := dispatcher.Resume()
	if err != nil || report.Pending != 0 || report.Running != 0 || report.WorkerStarted || launched != 0 {
		t.Fatalf("report=%+v launched=%d err=%v", report, launched, err)
	}
}

func TestHkCovResumeWarnsWhenWorkerCannotStart(t *testing.T) {
	dispatcher, checkout := hkCovEnv(t)
	hkCovSeedJob(t, dispatcher, hkCovJob("index", checkout, "b"))
	dispatcher.LaunchWorker = func(WorkerRequest) error { return errors.New("spawn denied") }
	report, err := dispatcher.Resume()
	if err == nil || report.Pending != 1 || report.WorkerStarted || !containsText(report.Warnings, "lifecycle hook worker did not start") {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestHkCovRecordUnseenFailureRejectsUnusableDirectory(t *testing.T) {
	dispatcher, _ := hkCovEnv(t)
	blocker := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)
	broken := dispatcher
	broken.StateDir = blocker
	if err := broken.recordUnseenFailure(hkCovReceipt("id")); err == nil {
		t.Fatal("expected unseen directory creation failure")
	}

	symlinked := dispatcher
	if err := os.MkdirAll(symlinked.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), symlinked.unseenDir()); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := symlinked.recordUnseenFailure(hkCovReceipt("id")); err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("symlinked unseen directory error=%v", err)
	}
}

func TestHkCovRecordUnseenFailureIsReadBackByStatus(t *testing.T) {
	dispatcher, _ := hkCovEnv(t)
	receipt := hkCovReceipt("unseen")
	if err := dispatcher.recordUnseenFailure(receipt); err != nil {
		t.Fatal(err)
	}
	count, err := dispatcher.unseenFailureCount()
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestHkCovClaimUnseenWarningsLimitAndStateErrors(t *testing.T) {
	dispatcher, _ := hkCovEnv(t)
	if warnings, err := dispatcher.claimUnseenWarnings(0); err != nil || len(warnings) != 0 {
		t.Fatalf("warnings=%v err=%v", warnings, err)
	}
	if warnings, err := dispatcher.claimUnseenWarnings(-3); err != nil || len(warnings) != 0 {
		t.Fatalf("negative limit warnings=%v err=%v", warnings, err)
	}
	if warnings, err := dispatcher.claimUnseenWarnings(10); err != nil || len(warnings) != 0 {
		t.Fatalf("missing state warnings=%v err=%v", warnings, err)
	}

	blocker := hkCovWriteFile(t, filepath.Join(t.TempDir(), "blocker"), "x", 0o600)
	broken := dispatcher
	broken.StateDir = filepath.Join(blocker, "state")
	if _, err := broken.claimUnseenWarnings(10); err == nil {
		t.Fatal("expected state inspection failure")
	}

	partial := dispatcher
	if err := os.MkdirAll(partial.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hkCovWriteFile(t, filepath.Join(partial.StateDir, "pending"), "not a dir", 0o600)
	if _, err := partial.claimUnseenWarnings(10); err == nil || !strings.Contains(err.Error(), "create lifecycle hook state") {
		t.Fatalf("partial state error=%v", err)
	}

	locked, _ := hkCovEnv(t)
	if err := os.MkdirAll(locked.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hkCovLockFile(t, filepath.Join(locked.StateDir, "unseen.lock"))
	if _, err := locked.claimUnseenWarnings(10); err == nil {
		t.Fatal("expected unseen lock failure")
	}
}

func TestHkCovClaimUnseenWarningsSortsSkipsAndQuarantines(t *testing.T) {
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dispatcher.unseenDir(), "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	hkCovWriteFile(t, filepath.Join(dispatcher.unseenDir(), "notes.txt"), "ignore", 0o600)
	hkCovWriteFile(t, filepath.Join(dispatcher.unseenDir(), "b.json"), "{broken", 0o600)
	hkCovWriteFile(t, filepath.Join(dispatcher.unseenDir(), "a.json"), hkCovMustJSON(t, hkCovReceipt("a")), 0o600)

	warnings, err := dispatcher.claimUnseenWarnings(10)
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "previous lifecycle hook index for github.com/acme/app failed") {
		t.Fatalf("warnings=%v err=%v", warnings, err)
	}
	entries, err := os.ReadDir(dispatcher.quarantineDir())
	if err != nil || len(entries) != 2 {
		t.Fatalf("quarantine entries=%v err=%v", entries, err)
	}
	remaining, err := os.ReadDir(dispatcher.unseenDir())
	if err != nil || len(remaining) != 2 {
		t.Fatalf("remaining=%v err=%v", remaining, err)
	}
}

func TestHkCovClaimUnseenWarningsHonoursLimit(t *testing.T) {
	dispatcher, _ := hkCovEnv(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		hkCovWriteFile(t, filepath.Join(dispatcher.unseenDir(), id+".json"), hkCovMustJSON(t, hkCovReceipt(id)), 0o600)
	}
	warnings, err := dispatcher.claimUnseenWarnings(2)
	if err != nil || len(warnings) != 2 {
		t.Fatalf("warnings=%v err=%v", warnings, err)
	}
	count, err := dispatcher.unseenFailureCount()
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestHkCovUnseenFailureCountErrors(t *testing.T) {
	dispatcher, _ := hkCovEnv(t)
	if count, err := dispatcher.unseenFailureCount(); err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}

	if err := os.MkdirAll(dispatcher.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hkCovWriteFile(t, dispatcher.unseenDir(), "not a dir", 0o600)
	if _, err := dispatcher.unseenFailureCount(); err == nil {
		t.Fatal("expected directory read failure")
	}

	if err := os.Remove(dispatcher.unseenDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dispatcher.unseenDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dispatcher.unseenDir(), "nested.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	hkCovWriteFile(t, filepath.Join(dispatcher.unseenDir(), "notes.txt"), "ignore", 0o600)
	if count, err := dispatcher.unseenFailureCount(); err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestHkCovWorkerHealthRoundTrip(t *testing.T) {
	dispatcher, _ := hkCovEnv(t)
	started := time.Unix(1700000000, 0).UTC()
	if err := dispatcher.writeWorkerHealth(WorkerHealth{Status: "running", StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	health, err := dispatcher.readWorkerHealth()
	if err != nil || health == nil || health.Status != "running" || health.SchemaVersion != 1 || !health.StartedAt.Equal(started) {
		t.Fatalf("health=%+v err=%v", health, err)
	}
}
