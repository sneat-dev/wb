package daemonruntime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/daemon"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func startTestDaemonFileBridge(t *testing.T, root, token, generation string) (daemonv1connect.DaemonServiceClient, func()) {
	t.Helper()
	service, err := daemonTestService(t, root, "test-build", generation, func() error { return errors.New("raw disabled") })
	if err != nil {
		t.Fatal(err)
	}
	return startTestDaemonFileBridgeWithService(t, root, token, generation, service, nil)
}
func startTestDaemonFileBridgeWithService(t *testing.T, root, token, generation string, service *daemon.Service, dropResponse func(string) bool) (daemonv1connect.DaemonServiceClient, func()) {
	return startTestDaemonFileBridgeWithServiceAndTimeout(t, root, token, generation, service, dropResponse, daemonFileBridgeTimeout)
}
func startTestDaemonFileBridgeWithServiceAndTimeout(t *testing.T, root, token, generation string, service *daemon.Service, dropResponse func(string) bool, timeout time.Duration) (daemonv1connect.DaemonServiceClient, func()) {
	t.Helper()
	path, handler := daemonv1connect.NewDaemonServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, AuthenticatedHandler(token, handler))
	server, err := NewFileBridgeServer(root, token, generation, mux)
	if err != nil {
		t.Fatal(err)
	}
	server.dropResponse = dropResponse
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	httpClient, err := newDaemonFileBridgeHTTPClientWithTimeout(root, generation, timeout)
	if err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); <-done }) }
	t.Cleanup(stop)
	return daemonv1connect.NewDaemonServiceClient(httpClient, RPCBaseURL), stop
}
func submitTestWorkerOperation(t *testing.T, ctx context.Context, client daemonv1connect.DaemonServiceClient, root, workerID, key string) *daemonv1.Operation {
	t.Helper()
	response, err := client.SubmitOperation(ctx, connect.NewRequest(&daemonv1.SubmitOperationRequest{IdempotencyKey: key, WorkingDirectory: root, Argv: []string{"go", "version"}, TargetWorkerId: workerID}))
	if err != nil {
		t.Fatal(err)
	}
	return response.Msg
}
func registerTestBridgeWorker(t *testing.T, ctx context.Context, client daemonv1connect.DaemonServiceClient, root, workerID string) *daemonv1.WorkerRegistration {
	t.Helper()
	response, err := client.RegisterWorker(ctx, connect.NewRequest(&daemonv1.RegisterWorkerRequest{WorkerId: workerID, Build: "test-build", ProtocolVersion: daemon.ProtocolVersion, Os: "test", Arch: "test", CpuCapacity: 1, PermittedRoots: []string{root}}))
	if err != nil {
		t.Fatal(err)
	}
	return response.Msg.Registration
}
func daemonTestRoot(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}
func mustDaemonPath(t *testing.T, resolve func(string) (string, error), root string) string {
	t.Helper()
	path, err := resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
func daemonTestDirectorySnapshot(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		lines = append(lines, path+" "+info.Mode().String())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}
func daemonTestDependencies(t *testing.T, root string) Dependencies {
	t.Helper()
	executable := filepath.Join(root, "wb")
	if err := testenv.WriteExecutableFile(executable, []byte("old installed binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	alive := map[int]bool{}
	pid := 900
	deps := Dependencies{UsageError: func(message string) error { return fmt.Errorf("%s", message) }, GuardTicker: newRuntimeTicker,
		Now: func() time.Time { return time.Date(2026, 9, 5, 7, 0, 0, 0, time.UTC) },
		// lockNow defaults to the real, monotonic clock (like production),
		// not the fixed now above: stateLock's contention tests that never
		// override this field must still be bounded by a genuine
		// daemonReadyTimeout deadline rather than spinning forever, so a
		// test that forgets to release the lock fails in seconds instead of
		// hanging until the suite's own timeout (sneat-dev/wb#700 review).
		LockNow:    time.Now,
		Executable: func() (string, error) { return executable, nil },
		Alive:      func(pid int) bool { return alive[pid] },
		Stop:       func(pid int, _ daemon.Supervisor, _ string) error { alive[pid] = false; return nil },
		Sleep:      func(time.Duration) {},
		Version:    func() buildinfo.Report { return buildinfo.Report{Version: "test", Revision: "test-revision"} },
		Token:      func() (string, error) { pid++; return strings.Repeat("a", 30) + string(rune(pid)), nil },
		Health:     func(context.Context, string) error { return nil },
		// Point the hub lookup at a path inside this test's own root, so a
		// status assertion never depends on whether the machine running the
		// suite happens to self-host bench.
		HubConfigPath: func() string { return filepath.Join(root, "wb.yaml") },
		RawPolicy: func(string) (bool, string, error) {
			return true, "test-policy", nil
		},
		Getpid:  func() int { return 4242 },
		Getppid: func() int { return 1 },
		// A fixed, always-unknown process start time by default: no test
		// double should depend on the real process table for correctness, and
		// a hard-coded fake PID (900-series, in these fixtures) must never be
		// checked against whatever real process happens to hold that number
		// on the machine running the suite (sneat-dev/wb#622 review minor).
		ProcessStartTime: func(int) (time.Time, bool) { return time.Time{}, false },
		// The recorded supervisor is presumed still present unless a test
		// deliberately exercises the stale-record escape.
		SupervisorPresent: func(daemon.Supervisor, string) (bool, string) { return true, "" },
	}
	deps.Start = func(_ string, args []string, _ string) (int, error) {
		// The supervisor unit no longer pins the lifecycle state path: it
		// passes --managed-start and the daemon resolves its own runtime
		// directory, so this fake resolves it the same way.
		for _, argument := range args {
			if argument == "--lifecycle-state" {
				t.Fatalf("daemon start pinned a resolved lifecycle path: %v", args)
			}
		}
		state, ok, err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Load()
		if err != nil || !ok || state.OwnerToken == "" {
			t.Fatalf("starting state = %#v, %t, %v", state, ok, err)
		}
		alive[pid] = true
		return pid, nil
	}
	return deps
}
func cwWtDaemonRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "wb-cwwt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	// Explicit projects roots derive state at <root>/.wb; TestMain isolates
	// real HOME and discovery. No retired WB_HOME environment pin is needed.
	return root
}
func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
func waitForBridgeRequest(t *testing.T, root string) {
	t.Helper()
	requests := filepath.Join(mustDaemonPath(t, daemonFileBridgeDirectory, root), "requests")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(requests)
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".json") {
				return
			}
		}
		time.Sleep(daemonFileBridgePoll)
	}
	t.Fatal("file bridge request did not appear")
}
func waitForBridgeResponse(t *testing.T, path string) daemonFileEnvelope {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response, err := readDaemonFileEnvelope(path)
		if err == nil {
			return response
		}
		time.Sleep(daemonFileBridgePoll)
	}
	t.Fatalf("file bridge response did not appear: %s", path)
	return daemonFileEnvelope{}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func daemonFileRequestTarget(procedure string, body []byte) (string, error) {
	target, _, err := daemonFilePrepareRequest(procedure, body, "")
	return target, err
}
func newDaemonOperationClient(root, token string) (daemonv1connect.DaemonServiceClient, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("daemon lifecycle state has no authentication token")
	}
	client, err := LocalHTTPClient(root, token)
	if err != nil {
		return nil, err
	}
	return daemonv1connect.NewDaemonServiceClient(client, RPCBaseURL), nil
}
func hubTestConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
func memoryHubConfig(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	token := filepath.Join(state, "github.token")
	if err := os.WriteFile(token, []byte("ghp_test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return hubTestConfig(t, fmt.Sprintf(memoryHubSection, state, token))
}

const memoryHubSection = "hub:\n  store:\n    engine: memory\n    path: %s\n  github:\n    token_file: %s\n"

func daemonLegacyFixture(t *testing.T) string {
	t.Helper()
	legacyParent, err := os.MkdirTemp("/tmp", "wb-legacy-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(legacyParent) })
	if resolved, resolveErr := filepath.EvalSymlinks(legacyParent); resolveErr == nil {
		legacyParent = resolved
	}
	t.Setenv("HOME", legacyParent)
	legacyHome := filepath.Join(legacyParent, ".wb")
	if err := os.MkdirAll(filepath.Join(legacyHome, "worktrees", "legacy-task", "acme", "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	legacyDir := filepath.Join(legacyHome, daemon.RuntimeDirName)
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return legacyDir
}

func daemonLegacyStatePath(legacyDir string) string {
	return filepath.Join(legacyDir, daemon.StateFileName)
}

func daemonTestService(t *testing.T, root, build, generation string, authorizeRaw func() error) (*daemon.Service, error) {
	t.Helper()
	operationsDirectory, err := daemon.OperationsDir(root)
	if err != nil {
		return nil, err
	}
	return daemon.NewService(root, operationsDirectory, build, generation, authorizeRaw)
}

func daemonTestState(t *testing.T, root, listen string, Provenance daemon.Provenance, token string, now time.Time) daemon.State {
	t.Helper()
	return daemon.NewStartingAt(nil, listen, Provenance, token,
		mustDaemonPath(t, func(string) (string, error) { return wbhome.Root(root) }, root),
		mustDaemonPath(t, StatePath, root), now)
}

func daemonTestContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

var errBoomForCmdWB = errors.New("boom")

func assertNoLeftoverDaemonLifecycleOwnerTempFile(t *testing.T, root string) {
	t.Helper()
	path, err := daemonLifecycleOwnerPath(root)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".daemon-lifecycle-owner-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover daemon lifecycle owner temp file(s) after failure: %v", matches)
	}
}
