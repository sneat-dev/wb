package daemonruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
)

func TestOperationClientUsesStoredOwnerAuthorityAndReportsCurrentFailures(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"native-handler", "local-client-refusal", "cancelled-probe", "start-refusal", "supervised-warning", "supervised-health-refusal"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root := cwWtDaemonRoot(t)
			deps := daemonTestDependencies(t, root)
			controller := NewController(deps, root)
			provenance, err := controller.Provenance()
			if err != nil {
				t.Fatal(err)
			}
			state := daemonTestState(t, root, DefaultListen, provenance, "owner", deps.Now())
			state.MarkReady(900, deps.Now())
			deps.Alive = func(pid int) bool { return pid == 900 }
			if mode == "supervised-warning" || mode == "supervised-health-refusal" {
				state.Provenance.Executable = "other-wb"
				state.Provenance.SHA256 = "other"
				state.Supervisor = daemon.SupervisorSystemd
				state.SupervisorLabel = "private.service"
			}
			if err := controller.store.Save(state); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("private operation client refused")
			if mode == "supervised-health-refusal" {
				deps.Health = func(context.Context, string) error { return failure }
			}
			calls := 0
			service, err := daemonTestService(t, root, "test", "generation", func() error { return errors.New("raw disabled") })
			if err != nil {
				t.Fatal(err)
			}
			path, handler := daemonv1connect.NewDaemonServiceHandler(service)
			mux := http.NewServeMux()
			mux.Handle(path, AuthenticatedHandler("owner", handler))
			deps.LocalClient = func(actualRoot, token string) (*http.Client, error) {
				calls++
				if actualRoot != root || token != "owner" {
					t.Errorf("client authority=%q,%q", actualRoot, token)
				}
				if mode == "local-client-refusal" {
					return nil, failure
				}
				return &http.Client{Transport: WithOwnerToken(token, roundTripFunc(func(request *http.Request) (*http.Response, error) {
					if mode == "cancelled-probe" {
						return nil, failure
					}
					response := httptest.NewRecorder()
					mux.ServeHTTP(response, request)
					return response.Result(), nil
				}))}, nil
			}
			ctx := context.Background()
			if mode == "cancelled-probe" {
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelCtx
			}
			if mode == "start-refusal" {
				deps.Executable = func() (string, error) { return "", failure }
			}
			var progress bytes.Buffer
			client, err := OperationClient(ctx, deps, root, &progress)
			switch mode {
			case "native-handler", "supervised-warning", "supervised-health-refusal":
				if err != nil || client == nil || calls != 1 {
					t.Fatalf("client=%v err=%v calls=%d", client, err, calls)
				}
				if (mode == "supervised-warning" || mode == "supervised-health-refusal") && !strings.Contains(progress.String(), "leaving it running") {
					t.Fatalf("warning=%q", progress.String())
				}
			case "start-refusal":
				if !errors.Is(err, failure) || calls != 0 {
					t.Fatalf("error=%v calls=%d", err, calls)
				}
			case "cancelled-probe":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error=%v", err)
				}
			default:
				if !errors.Is(err, failure) {
					t.Fatalf("error=%v", err)
				}
			}
		})
	}
}

func TestBridgeFallbackAndNativeHealthRefusals(t *testing.T) {
	t.Parallel()
	for _, err := range []error{nil, errors.New("ordinary transport error")} {
		if daemonFileBridgeFallbackAllowed(err) {
			t.Fatalf("unsafe fallback for %v", err)
		}
	}
	if !daemonFileBridgeFallbackAllowed(errors.New("private socket: permission denied")) {
		t.Fatal("permission diagnostic refused fallback")
	}
	if err := daemonHealthy(context.Background(), "bad\naddress"); err == nil {
		t.Fatal("malformed health address accepted")
	}
	if err := daemonOwnedHealthy(context.Background(), "bad\naddress", 12, 1); err == nil {
		t.Fatal("malformed owner health address accepted")
	}
	if err := RequireLoopbackAddress("127.0.0.1"); err == nil {
		t.Fatal("missing port accepted")
	}
	root := cwWtDaemonRoot(t)
	if err := daemonFileBridgeHealthy(context.Background(), root, "generation"); err == nil {
		t.Fatal("missing protected bridge accepted")
	}
}

func TestOperationProbeRefusalsPreserveActualStartupAndStoredAuthority(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"post-start-load", "not-ready", "ordinary-probe", "missing-bridge", "bridge-probe"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root := cwWtDaemonRoot(t)
			deps := daemonTestDependencies(t, root)
			deps.Alive = func(pid int) bool { return pid == 900 }
			controller := NewController(deps, root)
			provenance, err := controller.Provenance()
			if err != nil {
				t.Fatal(err)
			}
			state := daemonTestState(t, root, DefaultListen, provenance, "owner", deps.Now())
			state.MarkReady(900, deps.Now())
			if err := controller.store.Save(state); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("private probe failure")
			calls := 0
			load := controller.state.load
			controller.state.load = func() (daemon.State, bool, error) {
				calls++
				actual, found, err := load()
				if calls == 2 && mode == "post-start-load" {
					return daemon.State{}, false, failure
				}
				if calls == 2 && mode == "not-ready" {
					actual.Status = daemon.StatusStarting
				}
				return actual, found, err
			}
			controller.deps.LocalClient = func(string, string) (*http.Client, error) {
				return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					if mode == "missing-bridge" || mode == "bridge-probe" {
						return nil, os.ErrPermission
					}
					return nil, failure
				})}, nil
			}
			if mode == "bridge-probe" {
				if _, _, err := prepareDaemonFileBridge(root); err != nil {
					t.Fatal(err)
				}
				if _, err := daemonFileBridgeKey(root, true); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now()
			ticks := make(chan time.Time, 1)
			ticks <- now
			probe := operationProbe{bridgeClient: func(actualRoot, generation string) (*http.Client, error) {
				if actualRoot != root || generation != fmt.Sprint(state.Queue.Generation) {
					t.Fatalf("bridge authority=%q,%q", actualRoot, generation)
				}
				return newDaemonFileBridgeHTTPClientWithTimeout(actualRoot, generation, time.Millisecond)
			}, now: func() time.Time { now = now.Add(2 * time.Second); return now }, after: func(time.Duration) <-chan time.Time { return ticks }}
			ctx := context.Background()
			var output bytes.Buffer
			client, err := operationClientWithProbe(ctx, controller, &output, probe)
			if client != nil || err == nil {
				t.Fatalf("client=%v error=%v", client, err)
			}
			switch mode {
			case "post-start-load":
				if !errors.Is(err, failure) {
					t.Fatalf("error=%v", err)
				}
			case "not-ready":
				if !strings.Contains(err.Error(), "not ready") {
					t.Fatalf("error=%v", err)
				}
			case "ordinary-probe":
				if !errors.Is(err, failure) {
					t.Fatalf("error=%v", err)
				}
			case "missing-bridge":
				if !strings.Contains(err.Error(), "protected file bridge unavailable") {
					t.Fatalf("error=%v", err)
				}
			case "bridge-probe":
				if !strings.Contains(err.Error(), "connect to daemon through protected file bridge") || !strings.Contains(output.String(), "using protected project-root file bridge") {
					t.Fatalf("error=%v output=%q", err, output.String())
				}
			}
		})
	}
}
