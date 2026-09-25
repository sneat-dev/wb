package lifecyclehooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestPlanRejectsControlStateInsideCheckoutIncludingSymlinkedParent(t *testing.T) {
	t.Parallel()
	dispatcher, repository := testDispatcher(t)
	event := Event{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, OldSHA: "a", NewSHA: "b", Cause: "pull"}
	dispatcher.StateDir = filepath.Join(repository, ".wb-state")
	if _, _, err := dispatcher.Plan([]Event{event}); err == nil || !strings.Contains(err.Error(), "state directory") {
		t.Fatalf("direct state path error=%v", err)
	}

	target := filepath.Join(repository, ".control")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(repository), "state-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	dispatcher.StateDir = filepath.Join(link, "not-created-yet")
	if _, _, err := dispatcher.Plan([]Event{event}); err == nil || !strings.Contains(err.Error(), "state directory") {
		t.Fatalf("symlinked state path error=%v", err)
	}
}

func TestVerifyCheckoutPinsRepositoryAndQueuedHead(t *testing.T) {
	t.Parallel()
	repository, head := initLifecycleGitRepository(t, "acme/app")
	event := Event{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, NewSHA: head}
	physical, info, err := verifyCheckout(event)
	if err != nil || physical == "" || info == nil || !info.IsDir() {
		t.Fatalf("verify checkout physical=%q info=%v err=%v", physical, info, err)
	}
	event.Repository = "github.com/acme/other"
	if _, _, err := verifyCheckout(event); err == nil || !strings.Contains(err.Error(), "repository changed") {
		t.Fatalf("repository mismatch error=%v", err)
	}
	event.Repository = "github.com/acme/app"
	event.NewSHA = strings.Repeat("0", 40)
	if _, _, err := verifyCheckout(event); err == nil || !strings.Contains(err.Error(), "HEAD changed") {
		t.Fatalf("HEAD mismatch error=%v", err)
	}
}

func TestStartWorkerIfIdleDoesNotSpawnDuplicateWorker(t *testing.T) {
	t.Parallel()
	dispatcher, _ := testDispatcher(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	worker := flock.New(filepath.Join(dispatcher.StateDir, "worker.lock"))
	if err := worker.Lock(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Unlock() })
	var launches atomic.Int32
	dispatcher.LaunchWorker = func(WorkerRequest) error { launches.Add(1); return nil }
	started, err := dispatcher.startWorkerIfIdle()
	if err != nil || started || launches.Load() != 0 {
		t.Fatalf("started=%t launches=%d err=%v", started, launches.Load(), err)
	}
}

func TestStartWorkerIfIdleSuppressesStartingProcessStampede(t *testing.T) {
	t.Parallel()
	dispatcher, _ := testDispatcher(t)
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	dispatcher.Now = func() time.Time { return now }
	var launches atomic.Int32
	dispatcher.LaunchWorker = func(WorkerRequest) error { launches.Add(1); return nil }
	if started, err := dispatcher.startWorkerIfIdle(); err != nil || !started {
		t.Fatalf("first start=%t err=%v", started, err)
	}
	if started, err := dispatcher.startWorkerIfIdle(); err != nil || started || launches.Load() != 1 {
		t.Fatalf("duplicate start=%t launches=%d err=%v", started, launches.Load(), err)
	}
	now = now.Add(workerStartGrace)
	if started, err := dispatcher.startWorkerIfIdle(); err != nil || !started || launches.Load() != 2 {
		t.Fatalf("stale start=%t launches=%d err=%v", started, launches.Load(), err)
	}
}

func TestResumeStartsWorkerForStrandedPendingItem(t *testing.T) {
	t.Parallel()
	dispatcher, repository := testDispatcher(t)
	cfg, _, err := Load(dispatcher.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	item := pending{event: Event{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, NewSHA: "b"}, name: "index", executor: cfg.Executors["index"], count: 1}
	if _, err := dispatcher.enqueue(item); err != nil {
		t.Fatal(err)
	}
	var launches atomic.Int32
	dispatcher.LaunchWorker = func(WorkerRequest) error { launches.Add(1); return nil }
	report, err := dispatcher.Resume()
	if err != nil || report.Pending != 1 || !report.WorkerStarted || launches.Load() != 1 {
		t.Fatalf("report=%+v launches=%d err=%v", report, launches.Load(), err)
	}
}

func TestAsyncFailureIsWarnedOnNextDispatchExactlyOnce(t *testing.T) {
	t.Parallel()
	dispatcher, repository := testDispatcher(t)
	dispatcher.Run = func(context.Context, Invocation) error { return errors.New("index unavailable") }
	event := Event{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, OldSHA: "a", NewSHA: "b", Cause: "pull"}
	if _, err := dispatcher.Dispatch(context.Background(), []Event{event}); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.Drain(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	status, err := dispatcher.Status(20)
	if err != nil || status.UnseenFailures != 1 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	report, err := dispatcher.Dispatch(context.Background(), nil)
	if err != nil || len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "index unavailable") {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	report, err = dispatcher.Dispatch(context.Background(), nil)
	if err != nil || len(report.Warnings) != 0 {
		t.Fatalf("second report=%+v err=%v", report, err)
	}
}

func TestCorruptQueueItemIsQuarantinedWithoutBlockingValidWork(t *testing.T) {
	t.Parallel()
	dispatcher, repository := testDispatcher(t)
	// This test is about quarantine behavior, not about executing the valid
	// item's hook for real -- fake it out rather than needing a real process.
	dispatcher.Run = func(context.Context, Invocation) error { return nil }
	event := Event{Name: EventCheckoutUpdated, Repository: "github.com/acme/app", Checkout: repository, OldSHA: "a", NewSHA: "b", Cause: "pull"}
	if _, err := dispatcher.Dispatch(context.Background(), []Event{event}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dispatcher.pendingDir(), "corrupt.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := dispatcher.Drain(context.Background(), 1)
	if err != nil || report.Executed != 1 || !containsText(report.Warnings, "quarantined invalid pending") {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	entries, err := os.ReadDir(dispatcher.quarantineDir())
	if err != nil || len(entries) != 2 {
		t.Fatalf("quarantine entries=%d err=%v", len(entries), err)
	}
	status, err := dispatcher.Status(20)
	if err != nil || status.WorkerHealth == nil || status.WorkerHealth.Status != "warning" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestReceiptIndexSurvivesCorruptStreamAndStatusReportsMalformedLines(t *testing.T) {
	t.Parallel()
	dispatcher, _ := testDispatcher(t)
	receipt := Receipt{SchemaVersion: receiptSchemaVersion, ID: "20260101T000000.000000000Z-indexed", Status: "failed", Executor: "index", Repository: "github.com/acme/app"}
	if err := appendReceipt(dispatcher.ReceiptPath, receipt); err != nil {
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
	status, err := dispatcher.Status(20)
	if err != nil || len(status.Receipts) != 1 || !containsText(status.Findings, "invalid lifecycle hook receipt line") {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if err := os.Remove(dispatcher.ReceiptPath); err != nil {
		t.Fatal(err)
	}
	got, found, err := findReceipt(dispatcher.ReceiptPath, receipt.ID)
	if err != nil || !found || got.ID != receipt.ID {
		t.Fatalf("receipt=%+v found=%t err=%v", got, found, err)
	}
}

func TestCorruptReceiptIndexFallsBackToValidStream(t *testing.T) {
	t.Parallel()
	dispatcher, _ := testDispatcher(t)
	receipt := Receipt{SchemaVersion: receiptSchemaVersion, ID: "20260101T000000.000000000Z-fallback", Status: "failed"}
	if err := appendReceipt(dispatcher.ReceiptPath, receipt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(receiptIndexDir(dispatcher.ReceiptPath), receipt.ID+".json"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, found, err := findReceipt(dispatcher.ReceiptPath, receipt.ID)
	if err != nil || !found || got.ID != receipt.ID {
		t.Fatalf("receipt=%+v found=%t err=%v", got, found, err)
	}
}

func TestGCDryRunThenRemovesOnlyOldSeenReceiptsAndDiagnostics(t *testing.T) {
	t.Parallel()
	dispatcher, _ := testDispatcher(t)
	if err := dispatcher.ensureState(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	receipts := []Receipt{
		{SchemaVersion: receiptSchemaVersion, ID: "20260101T000000.000000000Z-a", Status: "failed", FinishedAt: now.Add(-72 * time.Hour)},
		{SchemaVersion: receiptSchemaVersion, ID: "20260102T000000.000000000Z-b", Status: "succeeded", FinishedAt: now.Add(-48 * time.Hour)},
		{SchemaVersion: receiptSchemaVersion, ID: "20260103T000000.000000000Z-c", Status: "succeeded", FinishedAt: now.Add(-48 * time.Hour)},
	}
	for index := range receipts {
		directory := filepath.Join(dispatcher.diagnosticsDir(), receipts[index].ID)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		receipts[index].StdoutPath = filepath.Join(directory, "stdout.log")
		if err := os.WriteFile(receipts[index].StdoutPath, []byte("output"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := appendReceipt(dispatcher.ReceiptPath, receipts[index]); err != nil {
			t.Fatal(err)
		}
	}
	if err := dispatcher.recordUnseenFailure(receipts[0]); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(dispatcher.ReceiptPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("not-json\n")
	_ = file.Close()

	options := GCOptions{Keep: 1, OlderThan: 24 * time.Hour, Now: now}
	dryRun, err := dispatcher.GC(options)
	if err != nil || strings.Join(dryRun.Candidates, ",") != receipts[1].ID || dryRun.RemovedReceipts != 0 {
		t.Fatalf("dry run=%+v err=%v", dryRun, err)
	}
	options.Apply = true
	applied, err := dispatcher.GC(options)
	if err != nil || applied.RemovedReceipts != 1 || applied.RemovedDiagnostics != 1 {
		t.Fatalf("applied=%+v err=%v", applied, err)
	}
	if _, err := os.Stat(filepath.Join(dispatcher.diagnosticsDir(), receipts[1].ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed diagnostics err=%v", err)
	}
	for _, kept := range []Receipt{receipts[0], receipts[2]} {
		if _, found, err := findReceipt(dispatcher.ReceiptPath, kept.ID); err != nil || !found {
			t.Fatalf("kept %s found=%t err=%v", kept.ID, found, err)
		}
	}
	raw, err := os.ReadFile(dispatcher.ReceiptPath)
	if err != nil || !strings.Contains(string(raw), "not-json") {
		t.Fatalf("receipt stream=%q err=%v", raw, err)
	}
}

func initLifecycleGitRepository(t *testing.T, identity string) (string, string) {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"init"}, {"config", "user.email", "wb@example.test"}, {"config", "user.name", "WB Test"},
		{"remote", "add", "origin", "https://github.com/" + identity + ".git"},
	}
	for _, arguments := range commands {
		command := exec.Command("git", append([]string{"-C", repository}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"add", "README.md"}, {"commit", "-m", "test"}} {
		command := exec.Command("git", append([]string{"-C", repository}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	command := exec.Command("git", "-C", repository, "rev-parse", "HEAD")
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return repository, strings.TrimSpace(string(output))
}

func containsText(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}

func TestReceiptJSONRemainsStableForRetention(t *testing.T) {
	t.Parallel()
	receipt := Receipt{SchemaVersion: receiptSchemaVersion, ID: "id", Status: "succeeded"}
	raw, err := json.Marshal(receipt)
	if err != nil || !strings.Contains(string(raw), `"schema_version":2`) {
		t.Fatalf("raw=%s err=%v", raw, err)
	}
}
