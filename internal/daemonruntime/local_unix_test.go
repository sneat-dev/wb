//go:build !windows

package daemonruntime

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
)

func TestDaemonLocalTransportRequiresTokenAndProtectsSocket(t *testing.T) {
	t.Parallel()
	root, err := os.MkdirTemp("/tmp", "wb-daemon-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	listener, err := ListenLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	info, err := os.Stat(mustDaemonPath(t, daemonLocalAddress, root))
	if err != nil {
		t.Fatal(err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("socket permissions = %o", permissions)
	}
	service, err := daemonTestService(t, root, "test-build", "9", func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	path, handler := daemonv1connect.NewDaemonServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, AuthenticatedHandler("expected-token", handler))
	server := &http.Server{Handler: mux}
	t.Cleanup(func() { _ = server.Close() })
	go func() { _ = server.Serve(listener) }()

	wrong, err := newDaemonOperationClient(root, "wrong-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.GetDaemonInfo(context.Background(), connect.NewRequest(&daemonv1.GetDaemonInfoRequest{})); err == nil || connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("wrong-token error = %v", err)
	}
	right, err := newDaemonOperationClient(root, "expected-token")
	if err != nil {
		t.Fatal(err)
	}
	response, err := right.GetDaemonInfo(context.Background(), connect.NewRequest(&daemonv1.GetDaemonInfoRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.SchedulerGeneration != "9" {
		t.Fatalf("generation = %q", response.Msg.SchedulerGeneration)
	}
}

func TestDaemonLocalTransportRejectsOverlongSocketPath(t *testing.T) {
	t.Parallel()
	// The endpoint derives from the projects root, so it is the *root* that has
	// to be long: a hundred characters below /tmp leaves the socket path above
	// the platform's ~104-byte sockaddr_un cap. The long root is passed as the
	// argument rather than through the environment, because the argument is what
	// selects the root; pinning only the environment left the endpoint short
	// enough to bind on platforms with shorter temporary paths.
	root := filepath.Join("/tmp", strings.Repeat("a", 100))
	if _, err := ListenLocal(root); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("overlong socket path error = %v", err)
	}
}

func TestDaemonStartRefusesWhileTheLegacySocketStillAnswers(t *testing.T) {
	root := daemonTestRoot(t)
	// The current home is <root>/.wb now, so the accepting legacy socket lives
	// in the retired default state home $HOME/.wb. The fixture keeps HOME under
	// /tmp for the filesystem socket length limit.
	legacyDir := daemonLegacyFixture(t)
	socketPath, ok := daemonSocketPathIn(legacyDir)
	if !ok {
		t.Skip("this platform has no filesystem endpoint for the legacy runtime directory")
	}
	listener, listenErr := net.Listen(daemonLocalNetwork, socketPath)
	if listenErr != nil {
		t.Fatal(listenErr)
	}
	t.Cleanup(func() { _ = listener.Close() })

	deps := daemonTestDependencies(t, root)
	deps.Start = func(string, []string, string) (int, error) {
		t.Fatal("a refused start must not launch a daemon")
		return 0, nil
	}
	result, err := NewController(deps, root).Start(context.Background(), DefaultListen)
	if err == nil || !strings.Contains(err.Error(), socketPath) {
		t.Fatalf("refusal = %v, want it to name %s", err, socketPath)
	}
	if result.LegacyRuntime == nil || !result.LegacyRuntime.SocketAnswers {
		t.Fatalf("refusal did not report the accepting legacy socket: %#v", result.LegacyRuntime)
	}
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("the legacy socket was disturbed: %v", err)
	}
}
