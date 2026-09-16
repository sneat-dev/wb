package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
)

func dqCovRegisterRequest(id string, mutate func(*daemonv1.RegisterWorkerRequest)) *daemonv1.RegisterWorkerRequest {
	request := &daemonv1.RegisterWorkerRequest{
		WorkerId: id, Build: "worker-build", ProtocolVersion: ProtocolVersion,
		Os: "test-os", Arch: "test-arch", CpuCapacity: 2,
	}
	if mutate != nil {
		mutate(request)
	}
	return request
}

func dqCovRegisterWorker(t *testing.T, service *Service, request *daemonv1.RegisterWorkerRequest) (*connect.Response[daemonv1.RegisterWorkerResponse], error) {
	t.Helper()
	return service.RegisterWorker(context.Background(), connect.NewRequest(request))
}

func TestDqCovRegisterWorkerValidationFailsClosed(t *testing.T) {
	root := t.TempDir()
	otherRoot := t.TempDir()
	file := filepath.Join(root, "regular-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tooManyRoots := make([]string, maxWorkerRoots+1)
	for index := range tooManyRoots {
		tooManyRoots[index] = root
	}
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}

	checks := []struct {
		name string
		want connect.Code
		text string
		edit func(*daemonv1.RegisterWorkerRequest)
	}{
		{"empty id", connect.CodeInvalidArgument, "worker_id", func(r *daemonv1.RegisterWorkerRequest) { r.WorkerId = "  " }},
		{"unsafe id", connect.CodeInvalidArgument, "worker_id", func(r *daemonv1.RegisterWorkerRequest) { r.WorkerId = "bad/id" }},
		{"oversized id", connect.CodeInvalidArgument, "worker_id", func(r *daemonv1.RegisterWorkerRequest) { r.WorkerId = strings.Repeat("w", maxWorkerIDBytes+1) }},
		{"protocol mismatch", connect.CodeFailedPrecondition, "incompatible with daemon protocol", func(r *daemonv1.RegisterWorkerRequest) { r.ProtocolVersion = ProtocolVersion + 1 }},
		{"missing build", connect.CodeInvalidArgument, "build, os, and arch are required", func(r *daemonv1.RegisterWorkerRequest) { r.Build = " " }},
		{"missing os", connect.CodeInvalidArgument, "build, os, and arch are required", func(r *daemonv1.RegisterWorkerRequest) { r.Os = "" }},
		{"missing arch", connect.CodeInvalidArgument, "build, os, and arch are required", func(r *daemonv1.RegisterWorkerRequest) { r.Arch = "" }},
		{"zero capacity", connect.CodeInvalidArgument, "cpu_capacity must be positive", func(r *daemonv1.RegisterWorkerRequest) { r.CpuCapacity = 0 }},
		{"no roots", connect.CodeInvalidArgument, "1-" + "64 explicitly permitted roots", func(r *daemonv1.RegisterWorkerRequest) { r.PermittedRoots = nil }},
		{"too many roots", connect.CodeInvalidArgument, "explicitly permitted roots", func(r *daemonv1.RegisterWorkerRequest) { r.PermittedRoots = tooManyRoots }},
		{"relative root", connect.CodeInvalidArgument, "permitted root must be absolute", func(r *daemonv1.RegisterWorkerRequest) {
			r.PermittedRoots = []string{"relative"}
		}},
		{"unresolvable root", connect.CodeInvalidArgument, "resolve worker permitted root", func(r *daemonv1.RegisterWorkerRequest) {
			r.PermittedRoots = []string{filepath.Join(root, "absent")}
		}},
		{"root is a file", connect.CodeInvalidArgument, "is not a directory", func(r *daemonv1.RegisterWorkerRequest) {
			r.PermittedRoots = []string{file}
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			request := dqCovRegisterRequest("validation-worker", check.edit)
			_, err := dqCovRegisterWorker(t, service, request)
			if connect.CodeOf(err) != check.want || !strings.Contains(err.Error(), check.text) {
				t.Fatalf("RegisterWorker error = %v, want %v containing %q", err, check.want, check.text)
			}
			if _, ok := service.workers[request.WorkerId]; ok {
				t.Fatalf("rejected registration stored worker %q", request.WorkerId)
			}
		})
	}

	// A worker may legitimately permit the same root twice and in unordered
	// form; the canonical set must be deduplicated and sorted.
	if _, err := dqCovRegisterWorker(t, service, dqCovRegisterRequest("canonical-worker", func(r *daemonv1.RegisterWorkerRequest) {
		r.PermittedRoots = []string{root, otherRoot, root, filepath.Join(root, ".", "")}
	})); err != nil {
		t.Fatal(err)
	}
	roots := service.workers["canonical-worker"].roots
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	resolvedOther, err := filepath.EvalSymlinks(otherRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{resolvedOther, resolvedRoot}
	sort.Strings(want)
	if len(roots) != len(want) {
		t.Fatalf("canonical roots = %#v, want %#v", roots, want)
	}
	for index := range want {
		if roots[index] != want[index] {
			t.Fatalf("canonical roots = %#v, want %#v", roots, want)
		}
	}
}

func TestDqCovRegisterWorkerReconnectRecoveryFailureKeepsOldGeneration(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	registration := registerWorkerForTest(t, service, "recovering-worker", root)
	operation := submitWorkerTestOperation(t, service, root, "recovering", "recovering-worker")
	_ = leaseWorkerTestOperation(t, service, registration)

	dqCovInjectPersist(service, func(item *record) error {
		if item.Operation.State == daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED {
			return errors.New("injected durable store failure")
		}
		return nil
	})
	if _, err := dqCovRegisterWorker(t, service, dqCovRegisterRequest("recovering-worker", func(r *daemonv1.RegisterWorkerRequest) {
		r.PermittedRoots = []string{root}
	})); connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("reconnect with a failed recovery write = %v", err)
	}
	recovered := service.records[operation.OperationId]
	if recovered.Operation.State != daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED ||
		!strings.Contains(recovered.Operation.Error, "reconnected") {
		t.Fatalf("recovery disposition = %#v", recovered.Operation)
	}
	if service.workers["recovering-worker"].generation != registration.WorkerGeneration {
		t.Fatal("failed recovery replaced the worker generation")
	}
}

func TestDqCovLeaseOperationRejectsUnknownCancelledAndIdleWorkers(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	registration := registerWorkerForTest(t, service, "lease-worker", root)

	if _, err := service.LeaseOperation(context.Background(), connect.NewRequest(&daemonv1.LeaseOperationRequest{
		WorkerId: "absent", WorkerGeneration: "1", WaitMilliseconds: 1,
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("lease for an unknown worker = %v", err)
	}
	if _, err := service.LeaseOperation(context.Background(), connect.NewRequest(&daemonv1.LeaseOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: "superseded", WaitMilliseconds: 1,
	})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("lease for a superseded generation = %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.LeaseOperation(cancelled, connect.NewRequest(&daemonv1.LeaseOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration,
	})); connect.CodeOf(err) != connect.CodeCanceled {
		t.Fatalf("lease with a cancelled context = %v", err)
	}

	idle, err := service.LeaseOperation(context.Background(), connect.NewRequest(&daemonv1.LeaseOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, WaitMilliseconds: 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if idle.Msg.Assignment.OperationId != "" || idle.Msg.Assignment.WorkerId != registration.WorkerId {
		t.Fatalf("idle lease = %#v", idle.Msg.Assignment)
	}
}

func TestDqCovLeaseOperationWakesWhenMatchingWorkArrives(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	registration := registerWorkerForTest(t, service, "waiting-worker", root)
	queued := submitWorkerTestOperation(t, service, root, "arrives-later", "waiting-worker")
	// Remove the queued work from eligibility first so the lease really blocks.
	service.mu.Lock()
	service.records[queued.OperationId].TargetWorkerID = "somebody-else"
	service.mu.Unlock()

	type outcome struct {
		response *connect.Response[daemonv1.LeaseOperationResponse]
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		response, err := service.LeaseOperation(context.Background(), connect.NewRequest(&daemonv1.LeaseOperationRequest{
			WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, WaitMilliseconds: 3_000,
		}))
		done <- outcome{response: response, err: err}
	}()
	time.Sleep(50 * time.Millisecond)
	service.mu.Lock()
	service.records[queued.OperationId].TargetWorkerID = "waiting-worker"
	service.notifyLocked()
	service.mu.Unlock()

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.response.Msg.Assignment.OperationId != queued.OperationId {
			t.Fatalf("woken lease returned %#v", result.response.Msg.Assignment)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lease did not wake when matching work arrived")
	}
}

func TestDqCovLeaseOperationReportsAssignmentPersistenceFailure(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	registration := registerWorkerForTest(t, service, "assign-worker", root)
	queued := submitWorkerTestOperation(t, service, root, "assign-failure", "assign-worker")

	dqCovInjectPersist(service, func(item *record) error {
		if item.Operation.State == daemonv1.OperationState_OPERATION_STATE_RUNNING {
			return errors.New("injected durable store failure")
		}
		return nil
	})
	if _, err := service.LeaseOperation(context.Background(), connect.NewRequest(&daemonv1.LeaseOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, WaitMilliseconds: 1,
	})); connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("lease with a failed assignment write = %v", err)
	}
	restored := service.records[queued.OperationId]
	if restored.Operation.State != daemonv1.OperationState_OPERATION_STATE_QUEUED || restored.LeaseID != "" {
		t.Fatalf("assignment was not rolled back: %#v", restored)
	}

	service.persist = service.persistRecord
	assignment := leaseWorkerTestOperation(t, service, registration)
	if assignment.OperationId != queued.OperationId {
		t.Fatalf("retry leased %#v", assignment)
	}
}

func TestDqCovLeaseOperationOrdersEqualTimestampsByOperationID(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	frozen := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	service.now = func() time.Time { return frozen }
	registration := registerWorkerForTest(t, service, "ordering-worker", root)
	first := submitWorkerTestOperation(t, service, root, "ordering-a", "ordering-worker")
	second := submitWorkerTestOperation(t, service, root, "ordering-b", "ordering-worker")
	want := first.OperationId
	if second.OperationId < want {
		want = second.OperationId
	}
	assignment := leaseWorkerTestOperation(t, service, registration)
	if assignment.OperationId != want {
		t.Fatalf("leased %q, want the deterministic first operation %q", assignment.OperationId, want)
	}
}

// TestDqCovLeaseOperationOrdersDistinctTimestampsChronologically proves the
// queue is first-in-first-out when timestamps differ, and by operation id only
// when they collide.
func TestDqCovLeaseOperationOrdersDistinctTimestampsChronologically(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	service.now = func() time.Time { return clock }
	registration := registerWorkerForTest(t, service, "chronological-worker", root)
	older := submitWorkerTestOperation(t, service, root, "chronological-older", "chronological-worker")
	clock = clock.Add(time.Minute)
	newer := submitWorkerTestOperation(t, service, root, "chronological-newer", "chronological-worker")
	if older.OperationId == newer.OperationId {
		t.Fatal("submissions collided")
	}
	for attempt := 0; attempt < 2; attempt++ {
		assignment := leaseWorkerTestOperation(t, service, registration)
		want := []string{older.OperationId, newer.OperationId}[attempt]
		if assignment.OperationId != want {
			t.Fatalf("lease %d returned %q, want %q", attempt, assignment.OperationId, want)
		}
		completed, err := service.CompleteOperation(context.Background(), connect.NewRequest(&daemonv1.CompleteOperationRequest{
			WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration,
			OperationId: assignment.OperationId, LeaseId: assignment.LeaseId,
		}))
		if err != nil {
			t.Fatal(err)
		}
		if completed.Msg.Operation.State != daemonv1.OperationState_OPERATION_STATE_SUCCEEDED {
			t.Fatalf("completed operation = %#v", completed.Msg.Operation)
		}
	}
}

func TestDqCovHeartbeatOperationValidation(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	registration := registerWorkerForTest(t, service, "heartbeat-worker", root)
	queued := submitWorkerTestOperation(t, service, root, "heartbeat", "heartbeat-worker")
	assignment := leaseWorkerTestOperation(t, service, registration)

	if _, err := service.HeartbeatOperation(context.Background(), connect.NewRequest(&daemonv1.HeartbeatOperationRequest{
		WorkerId: "absent", WorkerGeneration: registration.WorkerGeneration, OperationId: assignment.OperationId, LeaseId: assignment.LeaseId,
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("heartbeat from an unknown worker = %v", err)
	}
	if _, err := service.HeartbeatOperation(context.Background(), connect.NewRequest(&daemonv1.HeartbeatOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, OperationId: "absent", LeaseId: assignment.LeaseId,
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("heartbeat for an unknown operation = %v", err)
	}
	if _, err := service.HeartbeatOperation(context.Background(), connect.NewRequest(&daemonv1.HeartbeatOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, OperationId: assignment.OperationId, LeaseId: "stale",
	})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("heartbeat with a stale lease = %v", err)
	}
	if _, err := service.HeartbeatOperation(context.Background(), connect.NewRequest(&daemonv1.HeartbeatOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, OperationId: assignment.OperationId, LeaseId: assignment.LeaseId,
		Progress: strings.Repeat("p", maxWorkerProgressBytes+1),
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("oversized heartbeat progress = %v", err)
	}
	if _, err := service.HeartbeatOperation(context.Background(), connect.NewRequest(&daemonv1.HeartbeatOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, OperationId: assignment.OperationId, LeaseId: assignment.LeaseId,
		Progress: "bad\x00progress",
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("heartbeat progress with NUL = %v", err)
	}

	accepted, err := service.HeartbeatOperation(context.Background(), connect.NewRequest(&daemonv1.HeartbeatOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, OperationId: assignment.OperationId, LeaseId: assignment.LeaseId,
		Progress: "compiling",
	}))
	if err != nil {
		t.Fatal(err)
	}
	dqCovInjectPersist(service, func(item *record) error {
		if item.Operation.Progress == "still compiling" {
			return errors.New("injected durable store failure")
		}
		return nil
	})
	if _, err := service.HeartbeatOperation(context.Background(), connect.NewRequest(&daemonv1.HeartbeatOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, OperationId: assignment.OperationId, LeaseId: assignment.LeaseId,
		Progress: "still compiling",
	})); connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("heartbeat with a failed durable write = %v", err)
	}
	unchanged := service.records[queued.OperationId]
	if unchanged.Operation.Progress != "compiling" || unchanged.Operation.Cursor != accepted.Msg.Operation.Cursor {
		t.Fatalf("failed heartbeat was not rolled back: %#v", unchanged.Operation)
	}
}

func TestDqCovCompleteOperationValidationAndFailure(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	registration := registerWorkerForTest(t, service, "complete-worker", root)
	queued := submitWorkerTestOperation(t, service, root, "complete", "complete-worker")
	assignment := leaseWorkerTestOperation(t, service, registration)

	if _, err := service.CompleteOperation(context.Background(), connect.NewRequest(&daemonv1.CompleteOperationRequest{
		WorkerId: "absent", WorkerGeneration: registration.WorkerGeneration, OperationId: assignment.OperationId, LeaseId: assignment.LeaseId,
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("completion from an unknown worker = %v", err)
	}
	if _, err := service.CompleteOperation(context.Background(), connect.NewRequest(&daemonv1.CompleteOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, OperationId: "absent", LeaseId: assignment.LeaseId,
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("completion for an unknown operation = %v", err)
	}
	if _, err := service.CompleteOperation(context.Background(), connect.NewRequest(&daemonv1.CompleteOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, OperationId: assignment.OperationId, LeaseId: "stale",
	})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("completion with a stale lease = %v", err)
	}

	dqCovInjectPersist(service, func(item *record) error {
		if terminal(item.Operation.State) {
			return errors.New("injected durable store failure")
		}
		return nil
	})
	if _, err := service.CompleteOperation(context.Background(), connect.NewRequest(&daemonv1.CompleteOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration,
		OperationId: assignment.OperationId, LeaseId: assignment.LeaseId, ExitCode: 0,
	})); connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("completion with a failed durable write = %v", err)
	}
	restored := service.records[queued.OperationId]
	if restored.Operation.State != daemonv1.OperationState_OPERATION_STATE_RUNNING {
		t.Fatalf("failed completion was not rolled back: %#v", restored.Operation)
	}
}

func TestDqCovCompleteOperationMarksNonZeroExitAsFailure(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	registration := registerWorkerForTest(t, service, "failing-worker", root)
	submitWorkerTestOperation(t, service, root, "worker-failure", "failing-worker")
	assignment := leaseWorkerTestOperation(t, service, registration)
	completed, err := service.CompleteOperation(context.Background(), connect.NewRequest(&daemonv1.CompleteOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration,
		OperationId: assignment.OperationId, LeaseId: assignment.LeaseId,
		ExitCode: 3, Error: "tests failed",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Msg.Operation.State != daemonv1.OperationState_OPERATION_STATE_FAILED ||
		completed.Msg.Operation.ExitCode != 3 ||
		!strings.Contains(completed.Msg.Operation.Error, "tests failed") ||
		completed.Msg.Operation.LeaseExpiresUnixMilli != 0 {
		t.Fatalf("failed completion = %#v", completed.Msg.Operation)
	}
}

func TestDqCovDisconnectWorkerValidationAndPersistence(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DisconnectWorker(context.Background(), connect.NewRequest(&daemonv1.DisconnectWorkerRequest{
		WorkerId: "absent", WorkerGeneration: "1",
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("disconnect of an unknown worker = %v", err)
	}

	registration := registerWorkerForTest(t, service, "disconnect-worker", root)
	operation := submitWorkerTestOperation(t, service, root, "disconnect", "disconnect-worker")
	_ = leaseWorkerTestOperation(t, service, registration)
	dqCovInjectPersist(service, func(item *record) error {
		if item.Operation.State == daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED {
			return errors.New("injected durable store failure")
		}
		return nil
	})
	if _, err := service.DisconnectWorker(context.Background(), connect.NewRequest(&daemonv1.DisconnectWorkerRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration,
	})); connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("disconnect with a failed recovery write = %v", err)
	}
	if _, ok := service.workers[registration.WorkerId]; !ok {
		t.Fatal("failed disconnect still removed the worker")
	}
	recovered := service.records[operation.OperationId]
	if recovered.Operation.State != daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED ||
		!strings.Contains(recovered.Operation.Error, "disconnected") {
		t.Fatalf("recovery disposition = %#v", recovered.Operation)
	}
}

func TestDqCovReapExpiredLeaseReportsPersistenceFailure(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(5000, 0).UTC()
	service.now = func() time.Time { return now }
	registration := registerWorkerForTest(t, service, "expiring-worker", root)
	operation := submitWorkerTestOperation(t, service, root, "expiry", "expiring-worker")
	_ = leaseWorkerTestOperation(t, service, registration)

	dqCovInjectPersist(service, func(item *record) error {
		if item.Operation.Progress == "worker unavailable" {
			return errors.New("injected durable store failure")
		}
		return nil
	})
	now = now.Add(workerLeaseDuration + time.Second)
	recovered, err := service.GetOperation(context.Background(), connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: operation.OperationId}))
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Msg.State != daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED ||
		!strings.Contains(recovered.Msg.Error, "persist expired worker lease") {
		t.Fatalf("expired operation = %#v", recovered.Msg)
	}
}

func TestDqCovPathWithinAnyResolvesPermittedRoots(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if permitted, err := pathWithinAny([]string{resolvedRoot}, nested); err != nil || !permitted {
		t.Fatalf("pathWithinAny(nested) = %t, %v", permitted, err)
	}
	if permitted, err := pathWithinAny([]string{resolvedRoot}, filepath.Dir(resolvedRoot)); err != nil || permitted {
		t.Fatalf("pathWithinAny(parent) = %t, %v", permitted, err)
	}
	if _, err := pathWithinAny([]string{resolvedRoot}, "relative"); err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("pathWithinAny(relative) = %v", err)
	}
	if _, err := pathWithinAny([]string{resolvedRoot}, filepath.Join(root, "absent")); err == nil {
		t.Fatal("pathWithinAny accepted an unresolvable candidate")
	}
}

// TestDqCovStartLeaseRecoveryReapsExpiredLeasesWithoutAClient drives the
// daemon-owned recovery clock: an expired worker lease must be fenced even
// though no client ever polls the operation.
func TestDqCovStartLeaseRecoveryReapsExpiredLeasesWithoutAClient(t *testing.T) {
	root := t.TempDir()
	service, err := NewService(root, "build", "1", allowRawForTest)
	if err != nil {
		t.Fatal(err)
	}
	registration := registerWorkerForTest(t, service, "background-worker", root)
	operation := submitWorkerTestOperation(t, service, root, "background-expiry", "background-worker")
	_ = leaseWorkerTestOperation(t, service, registration)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service.StartLeaseRecovery(ctx)

	// Move the daemon clock beyond the lease without touching any API: only
	// the recovery goroutine can observe the expiry from here.
	service.mu.Lock()
	service.now = func() time.Time { return time.Now().Add(time.Hour) }
	service.mu.Unlock()

	dqCovWaitFor(t, workerHeartbeatInterval+15*time.Second, "the recovery clock to fence the expired lease", func() bool {
		service.mu.Lock()
		defer service.mu.Unlock()
		item := service.records[operation.OperationId]
		return item != nil && item.Operation.State == daemonv1.OperationState_OPERATION_STATE_RECOVERY_REQUIRED
	})
	cancel()
	recovered := service.records[operation.OperationId]
	if !strings.Contains(recovered.Operation.Error, "lease expired") {
		t.Fatalf("recovered operation = %#v", recovered.Operation)
	}
}
