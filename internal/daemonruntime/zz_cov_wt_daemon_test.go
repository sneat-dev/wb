//go:build !windows

package daemonruntime

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
)

func TestCwWtDefaultDaemonDependenciesClosures(t *testing.T) {
	root := cwWtDaemonRoot(t)
	t.Setenv("WB_HOME", filepath.Join(root, "wb-home"))
	deps := DefaultDependencies(func(message string) error { return fmt.Errorf("%s", message) })

	if got := deps.Now(); got.IsZero() {
		t.Fatal("default now() returned the zero time")
	}
	if _, err := deps.Executable(); err != nil {
		t.Fatalf("default executable(): %v", err)
	}
	if got := deps.Version(); got.Version == "" {
		t.Fatal("default version() returned no version")
	}
	if token, err := deps.Token(); err != nil || len(token) != 32 {
		t.Fatalf("default token() = (%q, %v)", token, err)
	}
	if pid := deps.Alive(-1); pid {
		t.Fatal("a negative pid must not be reported alive")
	}
	if path := deps.HubConfigPath(); path == "" {
		t.Fatal("default hubConfigPath() returned an empty path")
	}

	// The restart ticker delivers and stops cleanly.
	ticks, stopTicker := deps.RestartTicker(time.Millisecond)
	select {
	case <-ticks:
	case <-time.After(2 * time.Second):
		t.Fatal("restart ticker did not tick")
	}
	stopTicker()

	// The raw-execution policy closure resolves its path and loads the policy.
	allowed, path, err := deps.RawPolicy(root)
	if err != nil {
		t.Fatalf("default rawPolicy(): %v", err)
	}
	if allowed || path == "" {
		t.Fatalf("default rawPolicy() = (%t, %q)", allowed, path)
	}

	// A local HTTP client can be built for the fixture root.
	client, err := deps.LocalClient(root, "token")
	if err == nil {
		if client == nil {
			t.Fatal("default localClient() returned a nil client with no error")
		}
		client.CloseIdleConnections()
	}
}

func TestCwWtDaemonHealthyAndOwnedHealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/health":
			response.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(response, `{"daemon_pid":%d,"scheduler_generation":7}`, os.Getpid())
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	listen := strings.TrimPrefix(server.URL, "http://")

	if err := daemonHealthy(context.Background(), listen); err != nil {
		t.Fatalf("daemonHealthy: %v", err)
	}
	if err := daemonOwnedHealthy(context.Background(), listen, os.Getpid(), 7); err != nil {
		t.Fatalf("daemonOwnedHealthy: %v", err)
	}
	if err := daemonOwnedHealthy(context.Background(), listen, os.Getpid(), 8); err == nil {
		t.Fatal("a generation mismatch must fail")
	}
	if err := daemonOwnedHealthy(context.Background(), listen, os.Getpid()+1, 7); err == nil {
		t.Fatal("a pid mismatch must fail")
	}

	// A non-200 health endpoint is reported.
	bad := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "nope", http.StatusInternalServerError)
	}))
	defer bad.Close()
	badListen := strings.TrimPrefix(bad.URL, "http://")
	if err := daemonHealthy(context.Background(), badListen); err == nil || !strings.Contains(err.Error(), "health endpoint returned") {
		t.Fatalf("non-200 health = %v", err)
	}
	if err := daemonOwnedHealthy(context.Background(), badListen, 1, 1); err == nil {
		t.Fatal("owned health against a non-200 endpoint must fail")
	}

	// An undecodable body is reported.
	broken := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("not json"))
	}))
	defer broken.Close()
	if err := daemonOwnedHealthy(context.Background(), strings.TrimPrefix(broken.URL, "http://"), 1, 1); err == nil {
		t.Fatal("an undecodable health body must fail")
	}

	// A connection that cannot be established is reported.
	if err := daemonHealthy(context.Background(), "127.0.0.1:1"); err == nil {
		t.Fatal("an unreachable health endpoint must fail")
	}
	// An unparsable URL is reported by request construction.
	if err := daemonHealthy(context.Background(), "bad host:port"); err == nil {
		t.Fatal("an invalid health URL must fail")
	}
}

func TestCwWtDaemonRuntimeGuardStopsOnCancellation(t *testing.T) {
	guardInterval := 10 * time.Second
	guardTicker := func(time.Duration) (<-chan time.Time, func()) {
		ticker := time.NewTicker(guardInterval)
		return ticker.C, ticker.Stop
	}

	root := cwWtDaemonRoot(t)
	store := daemon.Store{Path: mustDaemonPath(t, StatePath, root)}
	owned := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "cwWt", Version: "cwWt"}, "cw-wt-token", time.Now().UTC())

	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	var guardErr error
	go func() {
		defer close(done)
		guardErr = RuntimeGuard(&out, ctx, "127.0.0.1:0", store, owned, "cw-wt-token", guardTicker)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("daemonRuntimeGuard did not return after cancellation")
	}
	if guardErr != nil {
		t.Fatalf("daemonRuntimeGuard after cancellation = %v", guardErr)
	}
	if out.Len() != 0 {
		t.Fatalf("daemonRuntimeGuard wrote %q after immediate cancellation", out.String())
	}
}
