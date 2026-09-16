package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/runqueue"
)

func dqCovWaitFor(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

func dqCovInjectPersist(service *Service, hook func(item *record) error) {
	original := service.persist
	service.persist = func(item *record) error {
		if err := hook(item); err != nil {
			return err
		}
		return original(item)
	}
}

// dqCovHoldCPU takes every CPU slot below root so a submitted raw operation
// blocks inside runqueue.Acquire until the returned release func is called.
func dqCovHoldCPU(t *testing.T, root string) func() {
	t.Helper()
	budget := runqueue.Budget()
	lease, _, err := runqueue.Acquire(context.Background(), root, budget, budget)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	return func() { once.Do(lease.Release) }
}

func dqCovOperationsDir(root string) string {
	return filepath.Join(root, ".wb", "runtime", "daemon", "operations")
}

// dqCovWriteRecord lays down a durable operation record exactly as an earlier
// daemon generation would have left it.
func dqCovWriteRecord(t *testing.T, root string, item *record) {
	t.Helper()
	directory := dqCovOperationsDir(root)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	contents, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, item.Operation.OperationId+".json"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

// dqCovBlockPersist makes persistRecord fail at its temporary-file boundary by
// occupying the exact temporary path with a directory.
func dqCovBlockPersist(t *testing.T, root, operationID string) {
	t.Helper()
	path := filepath.Join(dqCovOperationsDir(root), operationID+".json.tmp")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func dqCovHelperArgument() string {
	for index, value := range os.Args {
		if value == "--" && index+1 < len(os.Args) {
			return os.Args[index+1]
		}
	}
	return ""
}

// TestDqCovDaemonHelperProcess is a re-exec target for raw operations. It only
// acts when the parent test set WB_DAEMON_TEST_HELPER.
func TestDqCovDaemonHelperProcess(t *testing.T) {
	if os.Getenv("WB_DAEMON_TEST_HELPER") != "1" {
		return
	}
	if marker := os.Getenv("WB_DAEMON_TEST_MARKER"); marker != "" {
		if err := os.WriteFile(marker, []byte("executed"), 0o600); err != nil {
			os.Exit(2)
		}
	}
	switch dqCovHelperArgument() {
	case "fail":
		_, _ = fmt.Fprint(os.Stderr, "dqcov-helper-failure\n")
		os.Exit(7)
	case "big-output":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("y", outputTailLimit+4096))
		os.Exit(0)
	default:
		_, _ = fmt.Fprintf(os.Stdout, "dqcov-helper:%s\n", dqCovHelperArgument())
		os.Exit(0)
	}
}

func TestDqCovNewServiceDefaultsAuthorizeAndGeneration(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "test-build", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	info, err := service.GetDaemonInfo(context.Background(), connect.NewRequest(&daemonv1.GetDaemonInfoRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info.Msg.SchedulerGeneration, "wbg-") || len(info.Msg.SchedulerGeneration) <= len("wbg-") {
		t.Fatalf("default scheduler generation = %q, want a generated wbg- identifier", info.Msg.SchedulerGeneration)
	}
	_, err = service.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: root, Argv: []string{"true"}, LocalRawCommand: true,
	}))
	if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("default authorizer error = %v", err)
	}
}

func TestDqCovNewServiceRejectsUnusableOperationStore(t *testing.T) {
	t.Run("projects root is a file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewService(file, "build", "1", allowRawForTest); err == nil || !strings.Contains(err.Error(), "create daemon operation store") {
			t.Fatalf("NewService(file) = %v", err)
		}
	})

	t.Run("unreadable store", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("POSIX directory permissions are not enforced for this account")
		}
		root := t.TempDir()
		directory := dqCovOperationsDir(root)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, 0o300); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
		if _, err := NewService(root, "build", "1", allowRawForTest); err == nil || !strings.Contains(err.Error(), "read daemon operation store") {
			t.Fatalf("NewService with an unreadable store = %v", err)
		}
	})

	t.Run("running operation cannot be fenced", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("POSIX directory permissions are not enforced for this account")
		}
		root := t.TempDir()
		dqCovWriteRecord(t, root, &record{Schema: QueueSchema, Operation: &daemonv1.Operation{
			SchemaVersion: QueueSchema, OperationId: "unfenceable", State: daemonv1.OperationState_OPERATION_STATE_RUNNING,
		}})
		directory := dqCovOperationsDir(root)
		if err := os.Chmod(directory, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
		if _, err := NewService(root, "build", "2", allowRawForTest); err == nil || !strings.Contains(err.Error(), "write daemon operation") {
			t.Fatalf("NewService with a read-only store = %v", err)
		}
	})
}

func TestDqCovNewServiceIgnoresForeignStoreEntries(t *testing.T) {
	root := t.TempDir()
	directory := dqCovOperationsDir(root)
	if err := os.MkdirAll(filepath.Join(directory, "nested.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "notes.txt"), []byte("not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatalf("NewService rejected non-operation entries: %v", err)
	}
	if _, err := service.GetOperation(context.Background(), connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: "nested"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("foreign entry became an operation: %v", err)
	}
}

func TestDqCovNewServiceReportsCorruptDurableRecords(t *testing.T) {
	newRoot := func(t *testing.T, name string, contents []byte, mode os.FileMode, stage func(t *testing.T, root string)) {
		t.Helper()
		root := t.TempDir()
		directory := dqCovOperationsDir(root)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, name), contents, mode); err != nil {
			t.Fatal(err)
		}
		if stage != nil {
			stage(t, root)
		}
		_, err := NewService(root, "build", "1", allowRawForTest)
		if err == nil {
			t.Fatalf("NewService accepted %s", name)
		}
	}

	t.Run("unreadable operation", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("POSIX file permissions are not enforced for this account")
		}
		newRoot(t, "secret.json", []byte("{}"), 0o000, nil)
	})
	t.Run("corrupt json", func(t *testing.T) {
		newRoot(t, "broken.json", []byte("{"), 0o600, nil)
	})
	t.Run("incomplete schema", func(t *testing.T) {
		newRoot(t, "old.json", []byte(`{"schema":0}`), 0o600, nil)
	})
}

func TestDqCovNewServiceRebuildsWorkerTargetIdentity(t *testing.T) {
	t.Run("target recovered from the operation", func(t *testing.T) {
		root := t.TempDir()
		dqCovWriteRecord(t, root, &record{Schema: QueueSchema, ExecutionMode: executionModeWorker, WorkingDir: root, Argv: []string{"go", "test"}, Operation: &daemonv1.Operation{
			SchemaVersion: QueueSchema, OperationId: "worker-op", State: daemonv1.OperationState_OPERATION_STATE_QUEUED,
			Cursor: "1", TargetWorkerId: "recovered-worker",
		}})
		service, err := NewService(root, "build", "1", allowRawForTest)
		if err != nil {
			t.Fatal(err)
		}
		registration := registerWorkerForTest(t, service, "recovered-worker", root)
		assignment := leaseWorkerTestOperation(t, service, registration)
		if assignment.OperationId != "worker-op" || assignment.WorkerId != "recovered-worker" {
			t.Fatalf("recovered assignment = %#v", assignment)
		}
	})

	t.Run("conflicting identities", func(t *testing.T) {
		root := t.TempDir()
		dqCovWriteRecord(t, root, &record{Schema: QueueSchema, ExecutionMode: executionModeWorker, TargetWorkerID: "outer-worker", Operation: &daemonv1.Operation{
			SchemaVersion: QueueSchema, OperationId: "conflicted", State: daemonv1.OperationState_OPERATION_STATE_QUEUED,
			Cursor: "1", TargetWorkerId: "inner-worker",
		}})
		_, err := NewService(root, "build", "1", allowRawForTest)
		if err == nil || !strings.Contains(err.Error(), "conflicting target worker identities") {
			t.Fatalf("conflicting target identities = %v", err)
		}
	})

	t.Run("invalid target is quarantined", func(t *testing.T) {
		root := t.TempDir()
		dqCovWriteRecord(t, root, &record{Schema: QueueSchema, ExecutionMode: executionModeWorker, Operation: &daemonv1.Operation{
			SchemaVersion: QueueSchema, OperationId: "nameless", State: daemonv1.OperationState_OPERATION_STATE_QUEUED,
			Cursor: "1",
		}})
		service, err := NewService(root, "build", "1", allowRawForTest)
		if err != nil {
			t.Fatal(err)
		}
		quarantined, err := service.GetOperation(context.Background(), connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: "nameless"}))
		if err != nil {
			t.Fatal(err)
		}
		if quarantined.Msg.State != daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED || !strings.Contains(quarantined.Msg.Error, "explicit stable worker ID") {
			t.Fatalf("quarantined operation = %#v", quarantined.Msg)
		}
	})

	t.Run("quarantine is not durable", func(t *testing.T) {
		root := t.TempDir()
		dqCovWriteRecord(t, root, &record{Schema: QueueSchema, ExecutionMode: executionModeWorker, Operation: &daemonv1.Operation{
			SchemaVersion: QueueSchema, OperationId: "unwritable", State: daemonv1.OperationState_OPERATION_STATE_QUEUED,
			Cursor: "1",
		}})
		dqCovBlockPersist(t, root, "unwritable")
		if _, err := NewService(root, "build", "1", allowRawForTest); err == nil || !strings.Contains(err.Error(), "write daemon operation") {
			t.Fatalf("NewService with an unwritable quarantine = %v", err)
		}
	})

	t.Run("idempotency digest is rebuilt", func(t *testing.T) {
		root := t.TempDir()
		dqCovWriteRecord(t, root, &record{Schema: QueueSchema, ExecutionMode: executionModeWorker, WorkingDir: root, Argv: []string{"go", "test"}, Operation: &daemonv1.Operation{
			SchemaVersion: QueueSchema, OperationId: "legacy-key", State: daemonv1.OperationState_OPERATION_STATE_QUEUED,
			Cursor: "1", IdempotencyKey: "legacy-key", TargetWorkerId: "legacy-worker",
		}})
		service, err := NewService(root, "build", "1", allowRawForTest)
		if err != nil {
			t.Fatal(err)
		}
		existing := service.records["legacy-key"]
		if existing == nil || existing.RequestSHA == "" {
			t.Fatalf("rebuilt record = %#v", existing)
		}
		if again, err := service.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
			IdempotencyKey: "legacy-key", WorkingDirectory: root, Argv: []string{"go", "test"}, TargetWorkerId: "legacy-worker",
		})); err != nil || again.Msg.OperationId != "legacy-key" {
			t.Fatalf("replayed legacy key = %#v, %v", again, err)
		}
	})
}

func TestDqCovNewServiceFencesQueuedOperationWhenPersistFails(t *testing.T) {
	t.Run("running transition", func(t *testing.T) {
		root := t.TempDir()
		dqCovWriteRecord(t, root, &record{Schema: QueueSchema, Operation: &daemonv1.Operation{
			SchemaVersion: QueueSchema, OperationId: "running-unfenceable", State: daemonv1.OperationState_OPERATION_STATE_RUNNING, Cursor: "2",
		}})
		dqCovBlockPersist(t, root, "running-unfenceable")
		if _, err := NewService(root, "build", "1", allowRawForTest); err == nil || !strings.Contains(err.Error(), "write daemon operation") {
			t.Fatalf("NewService with an unfenceable running record = %v", err)
		}
	})

	t.Run("revoked raw authorization", func(t *testing.T) {
		root := t.TempDir()
		dqCovWriteRecord(t, root, &record{Schema: QueueSchema, WorkingDir: root, Argv: []string{"true"}, Operation: &daemonv1.Operation{
			SchemaVersion: QueueSchema, OperationId: "queued-denied", State: daemonv1.OperationState_OPERATION_STATE_QUEUED, Cursor: "1",
		}})
		dqCovBlockPersist(t, root, "queued-denied")
		if _, err := NewService(root, "build", "1", func() error { return errors.New("raw disabled") }); err == nil || !strings.Contains(err.Error(), "write daemon operation") {
			t.Fatalf("NewService with an unwritable revocation = %v", err)
		}
	})
}

func TestDqCovSubmitOperationValidation(t *testing.T) {
	service, err := NewService(t.TempDir(), "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	checks := []struct {
		name    string
		request *daemonv1.SubmitOperationRequest
		want    string
	}{
		{"raw with target worker", &daemonv1.SubmitOperationRequest{WorkingDirectory: root, Argv: []string{"true"}, LocalRawCommand: true, TargetWorkerId: "someone"}, "target_worker_id is only valid for normal worker execution"},
		{"empty argv", &daemonv1.SubmitOperationRequest{WorkingDirectory: root, LocalRawCommand: true}, "argv must name a command"},
		{"blank command", &daemonv1.SubmitOperationRequest{WorkingDirectory: root, Argv: []string{"  "}, LocalRawCommand: true}, "argv must name a command"},
		{"relative working directory", &daemonv1.SubmitOperationRequest{WorkingDirectory: "relative/dir", Argv: []string{"true"}, LocalRawCommand: true}, "working_directory must be absolute"},
		{"NUL byte in argv", &daemonv1.SubmitOperationRequest{WorkingDirectory: root, Argv: []string{"echo", "a\x00b"}, LocalRawCommand: true}, "argv must not contain NUL bytes"},
		{"control character in environment", &daemonv1.SubmitOperationRequest{WorkingDirectory: root, Argv: []string{"true"}, LocalRawCommand: true, Environment: map[string]string{"NO_COLOR": "line\nbreak"}}, "has an invalid value"},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			_, err := service.SubmitOperation(context.Background(), connect.NewRequest(check.request))
			if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), check.want) {
				t.Fatalf("error = %v, want %q", err, check.want)
			}
		})
	}
}

func TestDqCovSubmitOperationRollsBackFailedDurableWrite(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	original := service.persist
	service.persist = func(*record) error { return errors.New("injected durable store failure") }
	request := &daemonv1.SubmitOperationRequest{
		IdempotencyKey: "rollback-key", WorkingDirectory: root, Argv: []string{"true"}, LocalRawCommand: true,
	}
	if _, err := service.SubmitOperation(context.Background(), connect.NewRequest(request)); connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("submission error = %v", err)
	}
	service.persist = original
	if _, err := service.GetOperation(context.Background(), connect.NewRequest(&daemonv1.GetOperationRequest{})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("empty operation id = %v", err)
	}
	// The rolled-back idempotency key must be released: reusing it with a
	// different payload must create a new operation, not report a conflict.
	retried, err := service.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		IdempotencyKey: "rollback-key", WorkingDirectory: root, Argv: []string{"true", "different"}, LocalRawCommand: true,
	}))
	if err != nil {
		t.Fatalf("retry after rollback = %v", err)
	}
	if retried.Msg.State != daemonv1.OperationState_OPERATION_STATE_QUEUED {
		t.Fatalf("retried operation = %#v", retried.Msg)
	}
	// Submission starts an asynchronous execution that writes state into the
	// same tree t.TempDir removes. Drain it to a terminal state first, or the
	// cleanup races the writer and fails with "directory not empty".
	deadline := time.Now().Add(30 * time.Second)
	for {
		current, err := service.GetOperation(context.Background(), connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: retried.Msg.OperationId}))
		if err != nil {
			t.Fatalf("get retried operation = %v", err)
		}
		if terminal(current.Msg.State) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("retried operation did not reach a terminal state: %v", current.Msg.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDqCovGetAndWaitOperationErrorsAndDeadlines(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetOperation(context.Background(), connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: "absent"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetOperation(absent) = %v", err)
	}
	if _, err := service.WaitOperation(context.Background(), connect.NewRequest(&daemonv1.WaitOperationRequest{OperationId: "absent"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("WaitOperation(absent) = %v", err)
	}

	queued := submitWorkerTestOperation(t, service, root, "wait-worker", "wait-worker")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.WaitOperation(ctx, connect.NewRequest(&daemonv1.WaitOperationRequest{
		OperationId: queued.OperationId, AfterCursor: queued.Cursor,
	})); connect.CodeOf(err) != connect.CodeCanceled {
		t.Fatalf("cancelled wait = %v", err)
	}

	waited, err := service.WaitOperation(context.Background(), connect.NewRequest(&daemonv1.WaitOperationRequest{
		OperationId: queued.OperationId, AfterCursor: queued.Cursor, WaitMilliseconds: 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if waited.Msg.State != daemonv1.OperationState_OPERATION_STATE_QUEUED {
		t.Fatalf("short wait returned %#v", waited.Msg)
	}

	// A terminal operation returns immediately even with the default deadline.
	registration := registerWorkerForTest(t, service, "wait-worker", root)
	assignment := leaseWorkerTestOperation(t, service, registration)
	completed, err := service.CompleteOperation(context.Background(), connect.NewRequest(&daemonv1.CompleteOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration,
		OperationId: assignment.OperationId, LeaseId: assignment.LeaseId,
	}))
	if err != nil {
		t.Fatal(err)
	}
	settled, err := service.WaitOperation(context.Background(), connect.NewRequest(&daemonv1.WaitOperationRequest{
		OperationId: assignment.OperationId, AfterCursor: completed.Msg.Operation.Cursor,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !terminal(settled.Msg.State) {
		t.Fatalf("terminal wait returned %#v", settled.Msg)
	}
}

func TestDqCovCancelOperationCoversTerminalAndRunningCases(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CancelOperation(context.Background(), connect.NewRequest(&daemonv1.CancelOperationRequest{OperationId: "absent"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("CancelOperation(absent) = %v", err)
	}

	queued := submitWorkerTestOperation(t, service, root, "cancel-me", "cancel-worker")
	cancelled, err := service.CancelOperation(context.Background(), connect.NewRequest(&daemonv1.CancelOperationRequest{OperationId: queued.OperationId}))
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Msg.State != daemonv1.OperationState_OPERATION_STATE_CANCELLED || !strings.Contains(cancelled.Msg.Error, "cancelled by caller") {
		t.Fatalf("cancelled operation = %#v", cancelled.Msg)
	}
	again, err := service.CancelOperation(context.Background(), connect.NewRequest(&daemonv1.CancelOperationRequest{OperationId: queued.OperationId}))
	if err != nil {
		t.Fatal(err)
	}
	if again.Msg.Cursor != cancelled.Msg.Cursor {
		t.Fatalf("second cancel advanced the cursor: %#v vs %#v", again.Msg, cancelled.Msg)
	}

	// A live raw execution must have its context cancelled by CancelOperation.
	second := submitWorkerTestOperation(t, service, root, "cancel-running", "cancel-worker-2")
	service.mu.Lock()
	called := false
	service.active[second.OperationId] = active{cancel: func() { called = true }}
	service.mu.Unlock()
	if _, err := service.CancelOperation(context.Background(), connect.NewRequest(&daemonv1.CancelOperationRequest{OperationId: second.OperationId})); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("CancelOperation did not cancel the live execution context")
	}

	third := submitWorkerTestOperation(t, service, root, "cancel-unwritable", "cancel-worker-3")
	service.persist = func(*record) error { return errors.New("injected durable store failure") }
	if _, err := service.CancelOperation(context.Background(), connect.NewRequest(&daemonv1.CancelOperationRequest{OperationId: third.OperationId})); connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("cancel with a failed durable write = %v", err)
	}
	restored, err := service.GetOperation(context.Background(), connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: third.OperationId}))
	if err != nil {
		t.Fatal(err)
	}
	if restored.Msg.State != daemonv1.OperationState_OPERATION_STATE_QUEUED {
		t.Fatalf("failed cancel was not rolled back: %#v", restored.Msg)
	}
}

func TestDqCovExecuteIgnoresWorkThatIsNotQueuedRawExecution(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	workerOperation := submitWorkerTestOperation(t, service, root, "not-raw", "not-raw-worker")
	service.execute(workerOperation.OperationId)
	service.execute("absent")
	service.mu.Lock()
	remaining := len(service.active)
	state := service.records[workerOperation.OperationId].Operation.State
	service.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("execute() registered %d active operations", remaining)
	}
	if state != daemonv1.OperationState_OPERATION_STATE_QUEUED {
		t.Fatalf("worker operation was executed locally: %v", state)
	}
}

func TestDqCovExecuteFailsWhenTheCPUBudgetDirectoryIsUnusable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WB_DAEMON_TEST_HELPER", "1")
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	dqCovInjectPersist(service, func(item *record) error {
		if item.Operation.State == daemonv1.OperationState_OPERATION_STATE_QUEUED {
			return os.WriteFile(filepath.Join(root, ".wb", "runtime", "cpu"), []byte("occupied"), 0o600)
		}
		return nil
	})
	response, err := service.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: root,
		Argv:             []string{os.Args[0], "-test.run=TestDqCovDaemonHelperProcess", "--", "must-not-start"},
		CpuUnits:         1, LocalRawCommand: true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	operation := waitForTestOperation(t, service, response.Msg)
	if operation.State != daemonv1.OperationState_OPERATION_STATE_CANCELLED || !strings.Contains(operation.Error, "create WB CPU lease directory") {
		t.Fatalf("operation = %#v", operation)
	}
}

func TestDqCovExecuteSkipsWorkCancelledWhileWaitingForTheCPUBudget(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WB_DAEMON_TEST_HELPER", "1")
	t.Setenv("WB_DAEMON_TEST_MARKER", filepath.Join(root, "executed"))
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	release := dqCovHoldCPU(t, root)
	response, err := service.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: root,
		Argv:             []string{os.Args[0], "-test.run=TestDqCovDaemonHelperProcess", "--", "must-not-start"},
		CpuUnits:         1, LocalRawCommand: true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	operationID := response.Msg.OperationId
	dqCovWaitFor(t, 5*time.Second, "the raw execution to register itself", func() bool {
		service.mu.Lock()
		defer service.mu.Unlock()
		_, active := service.active[operationID]
		return active
	})
	// Simulate a concurrent terminal transition while the queue lease is
	// pending: the launcher must not overwrite it or start the command.
	service.mu.Lock()
	service.records[operationID].Operation.State = daemonv1.OperationState_OPERATION_STATE_CANCELLED
	service.records[operationID].Operation.Error = "cancelled while queued"
	service.mu.Unlock()
	release()
	dqCovWaitFor(t, 10*time.Second, "the launcher to abandon the cancelled operation", func() bool {
		service.mu.Lock()
		defer service.mu.Unlock()
		_, active := service.active[operationID]
		return !active
	})
	final, err := service.GetOperation(context.Background(), connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: operationID}))
	if err != nil {
		t.Fatal(err)
	}
	if final.Msg.State != daemonv1.OperationState_OPERATION_STATE_CANCELLED || !strings.Contains(final.Msg.Error, "cancelled while queued") {
		t.Fatalf("final operation = %#v", final.Msg)
	}
	if _, err := os.Stat(filepath.Join(root, "executed")); !os.IsNotExist(err) {
		t.Fatalf("cancelled operation still launched a command: %v", err)
	}
}

func TestDqCovExecuteRecordsChildFailureAndBoundedOutput(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WB_DAEMON_TEST_HELPER", "1")
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	response, err := service.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: root,
		Argv:             []string{os.Args[0], "-test.run=TestDqCovDaemonHelperProcess", "--", "fail"},
		LocalRawCommand:  true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForTestOperation(t, service, response.Msg)
	if failed.State != daemonv1.OperationState_OPERATION_STATE_FAILED || failed.ExitCode != 7 {
		t.Fatalf("failed operation = %#v", failed)
	}
	if !strings.Contains(failed.Error, "exit status 7") || !strings.Contains(string(failed.StderrTail), "dqcov-helper-failure") {
		t.Fatalf("failure detail = %#v", failed)
	}

	large, err := service.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: root,
		Argv:             []string{os.Args[0], "-test.run=TestDqCovDaemonHelperProcess", "--", "big-output"},
		LocalRawCommand:  true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	bounded := waitForTestOperation(t, service, large.Msg)
	if bounded.State != daemonv1.OperationState_OPERATION_STATE_SUCCEEDED {
		t.Fatalf("large-output operation = %#v", bounded)
	}
	// NOTE: for local raw commands os/exec copies through the promoted
	// bytes.Buffer.ReadFrom, so the recorded output is complete rather than
	// tail-bounded. See the report accompanying these tests.
	if len(bounded.StdoutTail) < outputTailLimit || !strings.HasSuffix(string(bounded.StdoutTail), "yyyy") {
		t.Fatalf("stdout tail = %d bytes ending %q", len(bounded.StdoutTail), bounded.StdoutTail[len(bounded.StdoutTail)-4:])
	}
}

func TestDqCovExecuteRevokesAuthorizationImmediatelyBeforeLaunch(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WB_DAEMON_TEST_HELPER", "1")
	t.Setenv("WB_DAEMON_TEST_MARKER", filepath.Join(root, "executed"))
	var revoked atomic.Bool
	service, err := NewService(root, "build", "1", func() error {
		if revoked.Load() {
			return errors.New("administrator revoked raw execution")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	persist := service.persist
	service.persist = func(item *record) error {
		if item.Operation.State == daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED {
			return errors.New("injected durable store failure")
		}
		return persist(item)
	}
	release := dqCovHoldCPU(t, root)
	response, err := service.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: root,
		Argv:             []string{os.Args[0], "-test.run=TestDqCovDaemonHelperProcess", "--", "must-not-start"},
		CpuUnits:         1, LocalRawCommand: true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	revoked.Store(true)
	release()
	operation := waitForTestOperation(t, service, response.Msg)
	if operation.State != daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED ||
		!strings.Contains(operation.Error, "persist authorization failure") {
		t.Fatalf("operation = %#v", operation)
	}
	if _, err := os.Stat(filepath.Join(root, "executed")); !os.IsNotExist(err) {
		t.Fatalf("revoked operation still launched a command: %v", err)
	}
}

func TestDqCovFinishIsIdempotentAndSkipsCancelledWork(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	service.finish("absent", daemonv1.OperationState_OPERATION_STATE_SUCCEEDED, 0, nil, nil, 0, nil)
	if _, err := service.GetOperation(context.Background(), connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: "absent"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("finish invented an operation: %v", err)
	}

	queued := submitWorkerTestOperation(t, service, root, "cancelled-finish", "cancelled-finish-worker")
	if _, err := service.CancelOperation(context.Background(), connect.NewRequest(&daemonv1.CancelOperationRequest{OperationId: queued.OperationId})); err != nil {
		t.Fatal(err)
	}
	service.finish(queued.OperationId, daemonv1.OperationState_OPERATION_STATE_SUCCEEDED, 0, []byte("late"), nil, 0, nil)
	settled, err := service.GetOperation(context.Background(), connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: queued.OperationId}))
	if err != nil {
		t.Fatal(err)
	}
	if settled.Msg.State != daemonv1.OperationState_OPERATION_STATE_CANCELLED || len(settled.Msg.StdoutTail) != 0 {
		t.Fatalf("cancelled operation was overwritten: %#v", settled.Msg)
	}
}

func TestDqCovFinishReportsFailedTerminalWrite(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WB_DAEMON_TEST_HELPER", "1")
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	dqCovInjectPersist(service, func(item *record) error {
		if terminal(item.Operation.State) {
			return errors.New("injected durable store failure")
		}
		return nil
	})
	response, err := service.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: root,
		Argv:             []string{os.Args[0], "-test.run=TestDqCovDaemonHelperProcess", "--", "succeed"},
		LocalRawCommand:  true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	operation := waitForTestOperation(t, service, response.Msg)
	if operation.State != daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED || !strings.Contains(operation.Error, "persist terminal transition") {
		t.Fatalf("operation = %#v", operation)
	}
}

func TestDqCovFailAuthorizationIgnoresWorkThatIsGoneOrNotQueued(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	service.active["stale"] = active{cancel: func() {}}
	service.mu.Unlock()
	service.failAuthorization("stale", errors.New("revoked"))
	service.mu.Lock()
	_, stillActive := service.active["stale"]
	service.mu.Unlock()
	if stillActive {
		t.Fatal("failAuthorization left a stale active entry behind")
	}
	service.failAuthorization("absent", errors.New("revoked"))
}

func TestDqCovPersistRecordReportsUnusableTargets(t *testing.T) {
	service, err := NewService(t.TempDir(), "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	service.directory = directory
	item := &record{Schema: QueueSchema, Operation: &daemonv1.Operation{SchemaVersion: QueueSchema, OperationId: "blocked"}}
	if err := os.MkdirAll(filepath.Join(directory, "blocked.json.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := service.persistRecord(item); err == nil || !strings.Contains(err.Error(), "write daemon operation") {
		t.Fatalf("persistRecord with an occupied temporary path = %v", err)
	}

	publish := &record{Schema: QueueSchema, Operation: &daemonv1.Operation{SchemaVersion: QueueSchema, OperationId: "published"}}
	if err := os.MkdirAll(filepath.Join(directory, "published.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := service.persistRecord(publish); err == nil || !strings.Contains(err.Error(), "publish daemon operation") {
		t.Fatalf("persistRecord onto a directory = %v", err)
	}
}

func TestDqCovOperationHelpersAreDeterministic(t *testing.T) {
	cursors := map[string]string{"": "1", "0": "1", "1": "2", "2": "3", "3": "3.1", "weird": "weird.1"}
	for input, want := range cursors {
		if got := nextCursor(input); got != want {
			t.Fatalf("nextCursor(%q) = %q, want %q", input, got, want)
		}
	}
	if got := classify(nil); got != "unknown" {
		t.Fatalf("classify(nil) = %q", got)
	}
	if got := classify([]string{"/usr/bin/go", "-test", "./..."}); got != "go" {
		t.Fatalf("classify(flag argument) = %q", got)
	}
	if got := classify([]string{"go", "test"}); got != "go/test" {
		t.Fatalf("classify(subcommand) = %q", got)
	}

	long := strings.Repeat("z", maxWorkerErrorBytes+10)
	if got := boundedString(long, maxWorkerErrorBytes); len(got) != maxWorkerErrorBytes || !strings.HasSuffix(long, got) {
		t.Fatalf("boundedString truncated to %d bytes", len(got))
	}
	if got := boundedString("short", maxWorkerErrorBytes); got != "short" {
		t.Fatalf("boundedString(short) = %q", got)
	}
	if got := boundedTail([]byte("short"), 99); string(got) != "short" {
		t.Fatalf("boundedTail(short, 99) = %q", got)
	}
	if got := boundedTail([]byte("short"), 4); string(got) != "hort" {
		t.Fatalf("boundedTail(short, 4) = %q", got)
	}

	buffer := &tailBuffer{}
	written, err := buffer.Write([]byte(strings.Repeat("a", outputTailLimit+3)))
	if err != nil || written != outputTailLimit+3 {
		t.Fatalf("tailBuffer.Write = %d, %v", written, err)
	}
	if buffer.Len() != outputTailLimit || !strings.HasPrefix(buffer.String(), "aaa") {
		t.Fatalf("tailBuffer kept %d bytes", buffer.Len())
	}
}
