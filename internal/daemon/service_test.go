package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
)

func TestDaemonServiceRunsDurableOperationWithBoundedReceipt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WB_DAEMON_TEST_HELPER", "1")
	service, err := NewService(root, "test-build", "17", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	info, err := service.GetDaemonInfo(context.Background(), connect.NewRequest(&daemonv1.GetDaemonInfoRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if info.Msg.SchedulerGeneration != "17" {
		t.Fatalf("scheduler generation = %q", info.Msg.SchedulerGeneration)
	}
	request := &daemonv1.SubmitOperationRequest{
		IdempotencyKey: "test-operation", WorkingDirectory: root,
		Argv:     []string{os.Args[0], "-test.run=TestDaemonServiceHelperProcess", "--", "hello"},
		CpuUnits: 1, LocalRawCommand: true, Environment: map[string]string{"NO_COLOR": "1"},
	}
	response, err := service.SubmitOperation(context.Background(), connect.NewRequest(request))
	if err != nil {
		t.Fatal(err)
	}
	operation := waitForTestOperation(t, service, response.Msg)
	if operation.State != daemonv1.OperationState_OPERATION_STATE_SUCCEEDED || operation.ExitCode != 0 {
		t.Fatalf("operation = %#v", operation)
	}
	if !strings.Contains(string(operation.StdoutTail), "helper:hello") {
		t.Fatalf("stdout tail = %q", operation.StdoutTail)
	}
	if len(operation.StdoutTail) > outputTailLimit || len(operation.StderrTail) > outputTailLimit {
		t.Fatalf("unbounded output: stdout=%d stderr=%d", len(operation.StdoutTail), len(operation.StderrTail))
	}
	duplicate, err := service.SubmitOperation(context.Background(), connect.NewRequest(request))
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Msg.OperationId != operation.OperationId {
		t.Fatalf("idempotent retry created %q after %q", duplicate.Msg.OperationId, operation.OperationId)
	}
	if _, err := os.Stat(filepath.Join(root, ".wb", "runtime", "daemon", "operations", operation.OperationId+".json")); err != nil {
		t.Fatalf("durable receipt missing: %v", err)
	}
}

func TestDaemonServiceRejectsIdempotencyPayloadMismatch(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "test-build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	base := &daemonv1.SubmitOperationRequest{
		IdempotencyKey: "same-key", WorkingDirectory: root, Argv: []string{"echo", "hello"},
		CpuUnits: 2, LocalRawCommand: true, Environment: map[string]string{"NO_COLOR": "1"},
	}
	if _, err := service.SubmitOperation(context.Background(), connect.NewRequest(base)); err != nil {
		t.Fatal(err)
	}
	checks := []*daemonv1.SubmitOperationRequest{
		{IdempotencyKey: "same-key", WorkingDirectory: root, Argv: []string{"echo", "different"}, CpuUnits: 2, LocalRawCommand: true, Environment: map[string]string{"NO_COLOR": "1"}},
		{IdempotencyKey: "same-key", WorkingDirectory: filepath.Dir(root), Argv: []string{"echo", "hello"}, CpuUnits: 2, LocalRawCommand: true, Environment: map[string]string{"NO_COLOR": "1"}},
		{IdempotencyKey: "same-key", WorkingDirectory: root, Argv: []string{"echo", "hello"}, CpuUnits: 3, LocalRawCommand: true, Environment: map[string]string{"NO_COLOR": "1"}},
		{IdempotencyKey: "same-key", WorkingDirectory: root, Argv: []string{"echo", "hello"}, CpuUnits: 2, LocalRawCommand: true, Environment: map[string]string{"TERM": "dumb"}},
	}
	for _, changed := range checks {
		_, err := service.SubmitOperation(context.Background(), connect.NewRequest(changed))
		if connect.CodeOf(err) != connect.CodeAlreadyExists || !strings.Contains(err.Error(), "different operation payload") {
			t.Fatalf("payload mismatch error = %v", err)
		}
	}
}

func TestDaemonServiceRejectsOversizedRequestFields(t *testing.T) {
	service, err := NewService(t.TempDir(), "test-build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	manyArguments := make([]string, maxArgumentCount+1)
	manyArguments[0] = "true"
	checks := []struct {
		name    string
		request *daemonv1.SubmitOperationRequest
		want    string
	}{
		{name: "idempotency key", request: &daemonv1.SubmitOperationRequest{IdempotencyKey: strings.Repeat("k", maxIdempotencyKeySize+1), WorkingDirectory: root, Argv: []string{"true"}, LocalRawCommand: true}, want: "idempotency_key exceeds"},
		{name: "argument count", request: &daemonv1.SubmitOperationRequest{WorkingDirectory: root, Argv: manyArguments, LocalRawCommand: true}, want: "argv exceeds 1024 arguments"},
		{name: "argument bytes", request: &daemonv1.SubmitOperationRequest{WorkingDirectory: root, Argv: []string{"echo", strings.Repeat("x", maxArgumentBytes)}, LocalRawCommand: true}, want: "argv exceeds 131072 aggregate bytes"},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			_, err := service.SubmitOperation(context.Background(), connect.NewRequest(check.request))
			if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), check.want) {
				t.Fatalf("oversize error = %v", err)
			}
		})
	}
}

func TestDaemonServiceRejectsRawExecutionWithoutPolicy(t *testing.T) {
	projectsRoot := t.TempDir()
	policyPath := filepath.Join(t.TempDir(), "missing-policy.json")
	service, err := NewService(projectsRoot, "test-build", "1", func() error {
		return RequireRawExecutionPolicy(policyPath, projectsRoot)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: t.TempDir(), Argv: []string{"true"}, LocalRawCommand: true,
	}))
	if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "administrator") {
		t.Fatalf("raw execution denial = %v", err)
	}
}

func TestDaemonServiceDoesNotRunAfterRunningTransitionPersistenceFailure(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	t.Setenv("WB_DAEMON_TEST_HELPER", "1")
	t.Setenv("WB_DAEMON_TEST_MARKER", marker)
	service, err := NewService(root, "test-build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	originalPersist := service.persist
	calls := 0
	service.persist = func(item *record) error {
		calls++
		if calls == 2 {
			return errors.New("injected durable store failure")
		}
		return originalPersist(item)
	}
	response, err := service.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: root,
		Argv:             []string{os.Args[0], "-test.run=TestDaemonServiceHelperProcess", "--", "must-not-run"},
		CpuUnits:         1, LocalRawCommand: true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	operation := waitForTestOperation(t, service, response.Msg)
	if operation.State != daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED || !strings.Contains(operation.Error, "persist running transition") {
		t.Fatalf("operation = %#v", operation)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command ran despite failed durable boundary: %v", err)
	}
}

func TestDaemonServiceRejectsUntrustedEnvironmentPersistence(t *testing.T) {
	service, err := NewService(t.TempDir(), "test-build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: t.TempDir(), Argv: []string{"go", "version"}, LocalRawCommand: true,
		Environment: map[string]string{"GITHUB_TOKEN": "must-not-be-written"},
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Fatalf("environment error = %v", err)
	}
}

func TestDaemonServiceRestartResumesQueuedAndFencesRunningOperations(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WB_DAEMON_TEST_HELPER", "1")
	before, err := NewService(root, "old-build", "3", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	queued := &record{Schema: QueueSchema, WorkingDir: root,
		Argv:      []string{os.Args[0], "-test.run=TestDaemonServiceHelperProcess", "--", "resumed"},
		Operation: &daemonv1.Operation{SchemaVersion: QueueSchema, OperationId: "queued", State: daemonv1.OperationState_OPERATION_STATE_QUEUED, Cursor: "1", CpuUnits: 1, SubmittedUnixMilli: now}}
	running := &record{Schema: QueueSchema, WorkingDir: root, Argv: []string{"unknown"},
		Operation: &daemonv1.Operation{SchemaVersion: QueueSchema, OperationId: "running", State: daemonv1.OperationState_OPERATION_STATE_RUNNING, Cursor: "2", SubmittedUnixMilli: now}}
	if err := before.persistRecord(queued); err != nil {
		t.Fatal(err)
	}
	if err := before.persistRecord(running); err != nil {
		t.Fatal(err)
	}

	after, err := NewService(root, "new-build", "4", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	resumed := waitForTestOperation(t, after, queued.Operation)
	if resumed.State != daemonv1.OperationState_OPERATION_STATE_SUCCEEDED || !strings.Contains(string(resumed.StdoutTail), "helper:resumed") {
		t.Fatalf("resumed queued operation = %#v", resumed)
	}
	contents, err := os.ReadFile(filepath.Join(root, ".wb", "runtime", "daemon", "operations", "queued.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), `"scheduler_generation": "4"`) {
		t.Fatalf("resumed operation did not bind new generation:\n%s", contents)
	}
	fenced, err := after.GetOperation(context.Background(), connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: "running"}))
	if err != nil {
		t.Fatal(err)
	}
	if fenced.Msg.State != daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED || !strings.Contains(fenced.Msg.Error, "inspect side effects") {
		t.Fatalf("fenced running operation = %#v", fenced.Msg)
	}
}

func TestDaemonServiceRestartDoesNotResumeQueuedOperationAfterPolicyRemoval(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	t.Setenv("WB_DAEMON_TEST_HELPER", "1")
	t.Setenv("WB_DAEMON_TEST_MARKER", marker)
	before, err := NewService(root, "old-build", "3", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	queued := &record{Schema: QueueSchema, WorkingDir: root,
		Argv:      []string{os.Args[0], "-test.run=TestDaemonServiceHelperProcess", "--", "must-not-resume"},
		Operation: &daemonv1.Operation{SchemaVersion: QueueSchema, OperationId: "queued-denied", State: daemonv1.OperationState_OPERATION_STATE_QUEUED, Cursor: "1", CpuUnits: 1, SubmittedUnixMilli: time.Now().UnixMilli()}}
	if err := before.persistRecord(queued); err != nil {
		t.Fatal(err)
	}

	missingPolicy := filepath.Join(t.TempDir(), "missing-policy.json")
	after, err := NewService(root, "new-build", "4", func() error {
		return RequireRawExecutionPolicy(missingPolicy, root)
	})
	if err != nil {
		t.Fatal(err)
	}
	fenced, err := after.GetOperation(context.Background(), connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: queued.Operation.OperationId}))
	if err != nil {
		t.Fatal(err)
	}
	if fenced.Msg.State != daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED || !strings.Contains(fenced.Msg.Error, "authorization failed") {
		t.Fatalf("queued operation after policy removal = %#v", fenced.Msg)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("queued command ran after policy removal: %v", err)
	}
}

func TestDaemonServiceRevalidatesPolicyImmediatelyBeforeQueuedLaunch(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	t.Setenv("WB_DAEMON_TEST_HELPER", "1")
	t.Setenv("WB_DAEMON_TEST_MARKER", marker)
	before, err := NewService(root, "old-build", "3", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	queued := &record{Schema: QueueSchema, WorkingDir: root,
		Argv:      []string{os.Args[0], "-test.run=TestDaemonServiceHelperProcess", "--", "revoked-before-launch"},
		Operation: &daemonv1.Operation{SchemaVersion: QueueSchema, OperationId: "queued-revoked", State: daemonv1.OperationState_OPERATION_STATE_QUEUED, Cursor: "1", CpuUnits: 1, SubmittedUnixMilli: time.Now().UnixMilli()}}
	if err := before.persistRecord(queued); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	writeRawExecutionPolicy(t, policyPath, []byte(enabledRawExecutionPolicy), 0o600)
	checks := 0
	after, err := NewService(root, "new-build", "4", func() error {
		checks++
		if err := RequireRawExecutionPolicy(policyPath, root); err != nil {
			return err
		}
		if checks == 1 {
			if err := os.Remove(policyPath); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fenced := waitForTestOperation(t, after, queued.Operation)
	if fenced.State != daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED || !strings.Contains(fenced.Error, "immediately before launch") {
		t.Fatalf("queued operation after live revocation = %#v", fenced)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("queued command ran after live revocation: %v", err)
	}
}

func waitForTestOperation(t *testing.T, service *Service, operation *daemonv1.Operation) *daemonv1.Operation {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for !terminal(operation.State) {
		response, err := service.WaitOperation(ctx, connect.NewRequest(&daemonv1.WaitOperationRequest{
			OperationId: operation.OperationId, AfterCursor: operation.Cursor, WaitMilliseconds: 1_000,
		}))
		if err != nil {
			t.Fatal(err)
		}
		operation = response.Msg
	}
	return operation
}

func TestDaemonServiceHelperProcess(t *testing.T) {
	if os.Getenv("WB_DAEMON_TEST_HELPER") != "1" {
		return
	}
	if marker := os.Getenv("WB_DAEMON_TEST_MARKER"); marker != "" {
		if err := os.WriteFile(marker, []byte("executed"), 0o600); err != nil {
			os.Exit(2)
		}
	}
	argument := ""
	for index, value := range os.Args {
		if value == "--" && index+1 < len(os.Args) {
			argument = os.Args[index+1]
			break
		}
	}
	_, _ = fmt.Fprintf(os.Stdout, "helper:%s\n", argument)
	os.Exit(0)
}

func allowRawForTest() error { return nil }
