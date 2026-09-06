package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
)

func TestWorkerExecutesNormalQueueWithoutRawAdministratorPolicy(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "daemon-build", "7", func() error { return errors.New("raw disabled") })
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	operation := submitWorkerTestOperation(t, service, root, "worker-normal", "sandbox-worker")
	if operation.State != daemonv1.OperationState_OPERATION_STATE_QUEUED {
		t.Fatalf("submitted operation = %#v", operation)
	}
	registration := registerWorkerForTest(t, service, "sandbox-worker", root)
	assignment := leaseWorkerTestOperation(t, service, registration)
	if assignment.OperationId != operation.OperationId || assignment.WorkingDirectory != root || len(assignment.Argv) == 0 {
		t.Fatalf("assignment = %#v", assignment)
	}
	now = now.Add(time.Second)
	heartbeat, err := service.HeartbeatOperation(context.Background(), connect.NewRequest(&daemonv1.HeartbeatOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration,
		OperationId: assignment.OperationId, LeaseId: assignment.LeaseId, Progress: "running tests",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if heartbeat.Msg.Operation.Progress != "running tests" || heartbeat.Msg.Operation.LeaseExpiresUnixMilli <= assignment.LeaseExpiresUnixMilli {
		t.Fatalf("heartbeat = %#v", heartbeat.Msg)
	}
	completed, err := service.CompleteOperation(context.Background(), connect.NewRequest(&daemonv1.CompleteOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration,
		OperationId: assignment.OperationId, LeaseId: assignment.LeaseId,
		StdoutTail: []byte(strings.Repeat("x", outputTailLimit+50)),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Msg.Operation.State != daemonv1.OperationState_OPERATION_STATE_SUCCEEDED || len(completed.Msg.Operation.StdoutTail) != outputTailLimit || completed.Msg.Operation.WorkerId != registration.WorkerId {
		t.Fatalf("completed operation = %#v", completed.Msg)
	}
}

func TestWorkerReconnectFencesOldGenerationAndHandsOffQueuedWork(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "daemon-build", "8", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	first := registerWorkerForTest(t, service, "stable-worker", root)
	interrupted := submitWorkerTestOperation(t, service, root, "interrupted", "stable-worker")
	assignment := leaseWorkerTestOperation(t, service, first)
	queued := submitWorkerTestOperation(t, service, root, "queued-after-reconnect", "stable-worker")

	second := registerWorkerForTest(t, service, "stable-worker", root)
	if second.WorkerGeneration == first.WorkerGeneration {
		t.Fatal("worker reconnect reused a generation")
	}
	fenced, err := service.GetOperation(context.Background(), connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: interrupted.OperationId}))
	if err != nil {
		t.Fatal(err)
	}
	if fenced.Msg.State != daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED || !strings.Contains(fenced.Msg.Error, "reconnected") {
		t.Fatalf("interrupted operation = %#v", fenced.Msg)
	}
	if _, err := service.HeartbeatOperation(context.Background(), connect.NewRequest(&daemonv1.HeartbeatOperationRequest{
		WorkerId: first.WorkerId, WorkerGeneration: first.WorkerGeneration,
		OperationId: assignment.OperationId, LeaseId: assignment.LeaseId,
	})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("old generation heartbeat error = %v", err)
	}
	handedOff := leaseWorkerTestOperation(t, service, second)
	if handedOff.OperationId != queued.OperationId {
		t.Fatalf("new generation leased %q, want queued %q", handedOff.OperationId, queued.OperationId)
	}
}

func TestQueuedOperationCannotCrossWorkersThatShareARoot(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "daemon-build", "shared-root", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	workerA := registerWorkerForTest(t, service, "agent-a", root)
	workerB := registerWorkerForTest(t, service, "agent-b", root)
	queued := submitWorkerTestOperation(t, service, root, "agent-a-job", workerA.WorkerId)
	journal, err := os.ReadFile(filepath.Join(root, ".wb", "runtime", "daemon", "operations", queued.OperationId+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(journal), `"target_worker_id": "agent-a"`) {
		t.Fatalf("journal does not bind the target worker: %s", journal)
	}
	_, err = service.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		IdempotencyKey: "agent-a-job", WorkingDirectory: root, Argv: []string{"go", "test", "./..."}, CpuUnits: 1, TargetWorkerId: workerB.WorkerId,
	}))
	if connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("changed target worker idempotency error = %v", err)
	}

	wrongWorker, err := service.LeaseOperation(context.Background(), connect.NewRequest(&daemonv1.LeaseOperationRequest{
		WorkerId: workerB.WorkerId, WorkerGeneration: workerB.WorkerGeneration, WaitMilliseconds: 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if wrongWorker.Msg.Assignment.OperationId != "" {
		t.Fatalf("worker %q leased operation targeted to %q: %#v", workerB.WorkerId, workerA.WorkerId, wrongWorker.Msg.Assignment)
	}
	assignment := leaseWorkerTestOperation(t, service, workerA)
	if assignment.OperationId != queued.OperationId {
		t.Fatalf("target worker leased %q, want %q", assignment.OperationId, queued.OperationId)
	}
}

func TestNormalWorkerSubmissionRequiresExplicitTarget(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "daemon-build", "missing-target", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: root, Argv: []string{"go", "test"},
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "target_worker_id") {
		t.Fatalf("missing target worker error = %v", err)
	}
}

func TestQueuedWorkerOperationSurvivesDaemonRestartAndReconnect(t *testing.T) {
	root := t.TempDir()
	before, err := NewService(root, "old-build", "3", func() error { return errors.New("raw disabled") })
	if err != nil {
		t.Fatal(err)
	}
	queued := submitWorkerTestOperation(t, before, root, "survive-restart", "reconnected-worker")
	after, err := NewService(root, "new-build", "4", func() error { return errors.New("raw disabled") })
	if err != nil {
		t.Fatal(err)
	}
	registration := registerWorkerForTest(t, after, "reconnected-worker", root)
	assignment := leaseWorkerTestOperation(t, after, registration)
	if assignment.OperationId != queued.OperationId || assignment.SchedulerGeneration != "4" {
		t.Fatalf("restarted assignment = %#v", assignment)
	}
}

func TestWorkerLeaseExpiryRequiresRecovery(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "daemon-build", "9", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	service.now = func() time.Time { return now }
	registration := registerWorkerForTest(t, service, "expiring-worker", root)
	operation := submitWorkerTestOperation(t, service, root, "lease-expiry", "expiring-worker")
	_ = leaseWorkerTestOperation(t, service, registration)
	now = now.Add(workerLeaseDuration + time.Second)
	recovered, err := service.GetOperation(context.Background(), connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: operation.OperationId}))
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Msg.State != daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED || !strings.Contains(recovered.Msg.Error, "lease expired") {
		t.Fatalf("expired operation = %#v", recovered.Msg)
	}
}

func TestWorkerDisconnectRequiresRecoveryForRunningLease(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "daemon-build", "9", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	registration := registerWorkerForTest(t, service, "disconnecting-worker", root)
	operation := submitWorkerTestOperation(t, service, root, "disconnect", "disconnecting-worker")
	_ = leaseWorkerTestOperation(t, service, registration)
	disconnected, err := service.DisconnectWorker(context.Background(), connect.NewRequest(&daemonv1.DisconnectWorkerRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if disconnected.Msg.RecoveryRequiredOperations != 1 {
		t.Fatalf("recovered operations = %d, want 1", disconnected.Msg.RecoveryRequiredOperations)
	}
	recovered, err := service.GetOperation(context.Background(), connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: operation.OperationId}))
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Msg.State != daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED || !strings.Contains(recovered.Msg.Error, "disconnected") {
		t.Fatalf("disconnected operation = %#v", recovered.Msg)
	}
}

func TestWorkerRegistrationAndQueueFailClosedOnPermissionsAndSecrets(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	service, err := NewService(root, "daemon-build", "10", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	registration := registerWorkerForTest(t, service, "rooted-worker", root)
	outsideOperation := submitWorkerTestOperation(t, service, outside, "outside", "rooted-worker")
	lease, err := service.LeaseOperation(context.Background(), connect.NewRequest(&daemonv1.LeaseOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, WaitMilliseconds: 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if lease.Msg.Assignment.OperationId != "" {
		t.Fatalf("worker received outside-root operation: %#v", lease.Msg)
	}
	stillQueued, err := service.GetOperation(context.Background(), connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: outsideOperation.OperationId}))
	if err != nil || stillQueued.Msg.State != daemonv1.OperationState_OPERATION_STATE_QUEUED {
		t.Fatalf("outside operation = %#v, %v", stillQueued.Msg, err)
	}
	_, err = service.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: root, Argv: []string{"go", "test"}, TargetWorkerId: "rooted-worker", Environment: map[string]string{"GITHUB_TOKEN": "must-stay-worker-side"},
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "must not enter the daemon request") {
		t.Fatalf("secret environment rejection = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(root, ".wb", "runtime", "daemon", "operations", outsideOperation.OperationId+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "GITHUB_TOKEN") || strings.Contains(string(contents), "must-stay-worker-side") {
		t.Fatal("worker-side environment entered the durable daemon journal")
	}
}

func TestWorkerIdempotencyCannotCrossTrustedRawBoundary(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "daemon-build", "11", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	request := &daemonv1.SubmitOperationRequest{
		IdempotencyKey: "execution-boundary", WorkingDirectory: root, Argv: []string{"go", "test"}, TargetWorkerId: "idempotency-worker",
	}
	if _, err := service.SubmitOperation(context.Background(), connect.NewRequest(request)); err != nil {
		t.Fatal(err)
	}
	request.LocalRawCommand = true
	request.TargetWorkerId = ""
	if _, err := service.SubmitOperation(context.Background(), connect.NewRequest(request)); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("cross-mode idempotency error = %v", err)
	}
}

func submitWorkerTestOperation(t *testing.T, service *Service, cwd, key, targetWorkerID string) *daemonv1.Operation {
	t.Helper()
	response, err := service.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		IdempotencyKey: key, WorkingDirectory: cwd, Argv: []string{"go", "test", "./..."}, CpuUnits: 1, TargetWorkerId: targetWorkerID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	return response.Msg
}

func registerWorkerForTest(t *testing.T, service *Service, id, root string) *daemonv1.WorkerRegistration {
	t.Helper()
	response, err := service.RegisterWorker(context.Background(), connect.NewRequest(&daemonv1.RegisterWorkerRequest{
		WorkerId: id, Build: "worker-build", ProtocolVersion: ProtocolVersion,
		Os: "test-os", Arch: "test-arch", CpuCapacity: 3, PermittedRoots: []string{root},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return response.Msg.Registration
}

func leaseWorkerTestOperation(t *testing.T, service *Service, worker *daemonv1.WorkerRegistration) *daemonv1.WorkerAssignment {
	t.Helper()
	response, err := service.LeaseOperation(context.Background(), connect.NewRequest(&daemonv1.LeaseOperationRequest{
		WorkerId: worker.WorkerId, WorkerGeneration: worker.WorkerGeneration, WaitMilliseconds: 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.Assignment.OperationId == "" {
		t.Fatal("worker received no assignment")
	}
	return response.Msg.Assignment
}
