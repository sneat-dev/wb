package workerrun

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/sneat-dev/wb/internal/daemon"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
	"github.com/sneat-dev/wb/internal/runqueue"
)

func TestWorkerRefusesLeasedDirectoryWhenItsOwnRootsDoNotPermitIt(t *testing.T) {
	root := t.TempDir()
	service, err := workerTestService(t, root, "test-build", "worker-refusal", func() error { return errors.New("raw disabled") })
	if err != nil {
		t.Fatal(err)
	}
	path, handler := daemonv1connect.NewDaemonServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	defer server.Close()
	client := daemonv1connect.NewDaemonServiceClient(server.Client(), server.URL)
	operation, err := client.SubmitOperation(context.Background(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: root, Argv: []string{"go", "version"}, TargetWorkerId: "refusing-worker",
	}))
	if err != nil {
		t.Fatal(err)
	}
	registered, err := client.RegisterWorker(context.Background(), connect.NewRequest(&daemonv1.RegisterWorkerRequest{
		WorkerId: "refusing-worker", Build: "test-build", ProtocolVersion: daemon.ProtocolVersion,
		Os: "test", Arch: "test", CpuCapacity: 1, PermittedRoots: []string{root},
	}))
	if err != nil {
		t.Fatal(err)
	}
	registration := registered.Msg.Registration
	leased, err := client.LeaseOperation(context.Background(), connect.NewRequest(&daemonv1.LeaseOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, WaitMilliseconds: 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := (New(Dependencies{})).assignment(context.Background(), "", &bytes.Buffer{}, client, registration, []string{t.TempDir()}, leased.Msg.Assignment); err != nil {
		t.Fatal(err)
	}
	completed, err := client.GetOperation(context.Background(), connect.NewRequest(&daemonv1.GetOperationRequest{OperationId: operation.Msg.OperationId}))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Msg.State != daemonv1.OperationState_OPERATION_STATE_FAILED || !strings.Contains(completed.Msg.Error, "outside permitted roots") {
		t.Fatalf("refused operation = %#v", completed.Msg)
	}
}
func TestCwCovExecuteWorkerAssignmentRunsAndReports(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	service, err := workerTestService(t, root, "test-build", "cw-worker-run", func() error { return errors.New("raw disabled") })
	if err != nil {
		t.Fatal(err)
	}
	path, handler := daemonv1connect.NewDaemonServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	defer server.Close()
	client := daemonv1connect.NewDaemonServiceClient(server.Client(), server.URL)

	ctx := context.Background()
	operation, err := client.SubmitOperation(ctx, connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: work, Argv: []string{"go", "version"}, TargetWorkerId: "cw-worker",
	}))
	if err != nil {
		t.Fatal(err)
	}
	registered, err := client.RegisterWorker(ctx, connect.NewRequest(&daemonv1.RegisterWorkerRequest{
		WorkerId: "cw-worker", Build: "test-build", ProtocolVersion: daemon.ProtocolVersion,
		Os: runtime.GOOS, Arch: runtime.GOARCH, CpuCapacity: 1, PermittedRoots: []string{root},
	}))
	if err != nil {
		t.Fatal(err)
	}
	registration := registered.Msg.Registration
	leased, err := client.LeaseOperation(ctx, connect.NewRequest(&daemonv1.LeaseOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, WaitMilliseconds: 1,
	}))
	if err != nil {
		t.Fatal(err)
	}

	// An assignment whose generations do not match this registration is
	// refused rather than executed.
	mismatched := &daemonv1.WorkerAssignment{
		WorkerId: "someone-else", WorkerGeneration: registration.WorkerGeneration,
		SchedulerGeneration: registration.SchedulerGeneration, OperationId: "op", LeaseId: "lease",
		WorkingDirectory: work, Argv: []string{"go", "version"},
	}
	if err := (New(Dependencies{})).assignment(ctx, root, &bytes.Buffer{}, client, registration, []string{root}, mismatched); err == nil ||
		!strings.Contains(err.Error(), "different worker") {
		t.Fatalf("mismatched generation error = %v", err)
	}
	// An assignment with no command is refused.
	empty := &daemonv1.WorkerAssignment{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration,
		SchedulerGeneration: registration.SchedulerGeneration, OperationId: "op", LeaseId: "lease",
		WorkingDirectory: work,
	}
	if err := (New(Dependencies{})).assignment(ctx, root, &bytes.Buffer{}, client, registration, []string{root}, empty); err == nil ||
		!strings.Contains(err.Error(), "without a command") {
		t.Fatalf("empty argv error = %v", err)
	}

	assignment := leased.Msg.Assignment
	err = (New(Dependencies{})).assignment(ctx, root, &bytes.Buffer{}, client, registration, []string{root}, assignment)
	if err != nil {
		t.Fatalf("executeWorkerAssignment: %v", err)
	}
	completed, err := client.GetOperation(ctx, connect.NewRequest(&daemonv1.GetOperationRequest{
		OperationId: operation.Msg.OperationId,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Msg.State != daemonv1.OperationState_OPERATION_STATE_SUCCEEDED {
		t.Fatalf("operation state = %v (error %q)", completed.Msg.State, completed.Msg.Error)
	}
	if !strings.Contains(string(completed.Msg.StdoutTail), "go version") {
		t.Errorf("stdout tail = %q, want the child's output", completed.Msg.StdoutTail)
	}
}
func TestCwCovExecuteWorkerAssignmentAdmitsExplicitCpuUnits(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	service, err := workerTestService(t, root, "test-build", "cw-worker-explicit", func() error { return errors.New("raw disabled") })
	if err != nil {
		t.Fatal(err)
	}
	path, handler := daemonv1connect.NewDaemonServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	defer server.Close()
	client := daemonv1connect.NewDaemonServiceClient(server.Client(), server.URL)

	ctx := context.Background()
	operation, err := client.SubmitOperation(ctx, connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: work, Argv: []string{"go", "version"}, CpuUnits: 1, TargetWorkerId: "cw-worker-explicit",
	}))
	if err != nil {
		t.Fatal(err)
	}
	registered, err := client.RegisterWorker(ctx, connect.NewRequest(&daemonv1.RegisterWorkerRequest{
		WorkerId: "cw-worker-explicit", Build: "test-build", ProtocolVersion: daemon.ProtocolVersion,
		Os: runtime.GOOS, Arch: runtime.GOARCH, CpuCapacity: 1, PermittedRoots: []string{root},
	}))
	if err != nil {
		t.Fatal(err)
	}
	registration := registered.Msg.Registration
	leased, err := client.LeaseOperation(ctx, connect.NewRequest(&daemonv1.LeaseOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, WaitMilliseconds: 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	assignment := leased.Msg.Assignment
	if assignment.CpuUnits == 0 {
		t.Fatal("assignment lost the explicit CpuUnits; test fixture no longer proves the explicit path")
	}
	if err := (New(Dependencies{})).assignment(ctx, root, &bytes.Buffer{}, client, registration, []string{root}, assignment); err != nil {
		t.Fatalf("executeWorkerAssignment with explicit CpuUnits: %v", err)
	}
	// Positive proof that the explicit CpuUnits actually went through
	// runqueue.AdmitExplicit's budget-sum pool (internal/runqueue.Acquire),
	// not runqueue.Admit's argv-classified path (review finding, mutant
	// M14: worker.go:215's AdmitExplicit->Admit swap previously survived
	// because nothing here observed which path ran). Acquire's slot lock
	// file is created with os.O_CREATE and is only ever unlocked and
	// closed by Lease.Release, never removed (internal/runqueue/queue.go),
	// so it is still on disk here. This assignment's argv is
	// []string{"go", "version"}, which runqueue.Classify reports as
	// KindNone (no test/vet/build verb) — under the M14 mutant,
	// runqueue.Admit would take the KindNone branch and return an
	// immediate no-op Admission without ever calling Acquire, so no slot
	// lock file would exist. This does not depend on timing or on the
	// host's CPU count: KindNone is Admit's first, unconditional case.
	slotLocks, globErr := filepath.Glob(filepath.Join(runqueue.QueueDirForTest(root), "slot-*.lock"))
	if globErr != nil {
		t.Fatalf("glob CPU admission slot locks: %v", globErr)
	}
	if len(slotLocks) == 0 {
		t.Fatal("no CPU admission slot lock file was left behind; the explicit CpuUnits assignment did not go through runqueue.AdmitExplicit's budget-sum pool")
	}
	completed, err := client.GetOperation(ctx, connect.NewRequest(&daemonv1.GetOperationRequest{
		OperationId: operation.Msg.OperationId,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Msg.State != daemonv1.OperationState_OPERATION_STATE_SUCCEEDED {
		t.Fatalf("operation state = %v (error %q)", completed.Msg.State, completed.Msg.Error)
	}
}
