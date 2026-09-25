package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/sneat-dev/wb/internal/daemon"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
)

func TestWorkerIndependentlyRefusesAssignedDirectoryOutsidePermittedRoots(t *testing.T) {
	root := daemonTestRoot(t)
	inside := filepath.Join(root, "repo")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	permitted, err := canonicalWorkerRoots([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := workerPermitsDirectory(permitted, inside); err != nil || !ok {
		t.Fatalf("inside directory = %t, %v", ok, err)
	}
	if ok, err := workerPermitsDirectory(permitted, outside); err != nil || ok {
		t.Fatalf("outside directory = %t, %v", ok, err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if ok, err := workerPermitsDirectory(permitted, link); err != nil || ok {
		t.Fatalf("symlink escape = %t, %v", ok, err)
	}
}

func TestWorkerChildKeepsInheritedSecretsLocal(t *testing.T) {
	environment := workerChildEnvironment([]string{"PATH=/bin", "GITHUB_TOKEN=worker-secret"}, []string{"/bin/true"}, "wbo-test", 2)
	joined := strings.Join(environment, "\n")
	for _, want := range []string{"GITHUB_TOKEN=worker-secret", "WB_OPERATION_ID=wbo-test", "WB_CPU_UNITS=2", "GOMAXPROCS=2"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("worker environment is missing %q: %s", want, joined)
		}
	}
}

// TestWorkerChildSetsGoParallelismForGoCommands proves workerChildEnvironment
// applies the same GOFLAGS -p=<units> rule cmd/wb/run.go's governedEnvironment
// does, preserving the caller's own GOFLAGS and letting an explicit -p win —
// so a worker-executed `go test` gets the same child parallelism a
// synchronous `wb run --` command does (sneat-dev/wb#621).
func TestWorkerChildSetsGoParallelismForGoCommands(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=readonly")
	environment := workerChildEnvironment([]string{"PATH=/bin"}, []string{"go", "test", "./..."}, "wbo-test", 4)
	joined := strings.Join(environment, "\n")
	if !strings.Contains(joined, "GOFLAGS=-mod=readonly -p=4") {
		t.Fatalf("worker environment is missing the derived GOFLAGS: %s", joined)
	}
}

// TestWorkerChildNeverSetsGOFLAGSForNonGoCommands proves the GOFLAGS
// injection is scoped to the go tool: a worker running some other command
// must not pick up a stray GOFLAGS it never asked for.
func TestWorkerChildNeverSetsGOFLAGSForNonGoCommands(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	environment := workerChildEnvironment([]string{"PATH=/bin"}, []string{"pytest", "-q"}, "wbo-test", 2)
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "GOFLAGS=") {
		t.Fatalf("worker environment set GOFLAGS for a non-go command: %s", joined)
	}
}

func TestWorkerRefusesLeasedDirectoryWhenItsOwnRootsDoNotPermitIt(t *testing.T) {
	root := daemonTestRoot(t)
	service, err := daemonTestService(t, root, "test-build", "worker-refusal", func() error { return errors.New("raw disabled") })
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
	command := newWorkerConnectCmd(&invocation{}, defaultDaemonDependencies())
	command.SetContext(context.Background())
	if err := executeWorkerAssignment(&invocation{}, command, client, registration, []string{t.TempDir()}, leased.Msg.Assignment); err != nil {
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

func TestWorkerConnectHelpExposesStableIdentityRootsAndFormats(t *testing.T) {
	command := newWorkerConnectCmd(&invocation{}, defaultDaemonDependencies())
	for _, name := range []string{"id", "root", "cpu-capacity", "format", "json"} {
		if command.Flags().Lookup(name) == nil {
			t.Fatalf("worker connect help is missing --%s", name)
		}
	}
}
