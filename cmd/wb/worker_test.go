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
	root := t.TempDir()
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
	environment := workerChildEnvironment([]string{"PATH=/bin", "GITHUB_TOKEN=worker-secret"}, "wbo-test", 2)
	joined := strings.Join(environment, "\n")
	for _, want := range []string{"GITHUB_TOKEN=worker-secret", "WB_OPERATION_ID=wbo-test", "WB_CPU_UNITS=2", "GOMAXPROCS=2"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("worker environment is missing %q: %s", want, joined)
		}
	}
}

func TestWorkerRefusesLeasedDirectoryWhenItsOwnRootsDoNotPermitIt(t *testing.T) {
	root := t.TempDir()
	service, err := daemon.NewService(root, "test-build", "worker-refusal", func() error { return errors.New("raw disabled") })
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
		WorkingDirectory: root, Argv: []string{"go", "version"},
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
	command := newWorkerConnectCmd(defaultDaemonDependencies())
	command.SetContext(context.Background())
	if err := executeWorkerAssignment(command, client, registration, []string{t.TempDir()}, leased.Msg.Assignment); err != nil {
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
	command := newWorkerConnectCmd(defaultDaemonDependencies())
	for _, name := range []string{"id", "root", "cpu-capacity", "format", "json"} {
		if command.Flags().Lookup(name) == nil {
			t.Fatalf("worker connect help is missing --%s", name)
		}
	}
}
