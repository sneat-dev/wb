package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/daemon"
)

// TestCockpitIsMountedOnTheLoopbackListenerWithoutAHub serves the real daemon
// with no hub: section and requests Cockpit over TCP with chosen Host headers
// (cockpit#ac:foreign-host-is-refused, cockpit#ac:unbuilt-application-says-so,
// cockpit#ac:dashboard-command-is-unchanged).
func TestCockpitIsMountedOnTheLoopbackListenerWithoutAHub(t *testing.T) {
	root := daemonShutdownTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.hubConfigPath = cockpitConfigFile(t, "cockpit:\n  refresh_interval: 45s\n  anonymous_metadata: false\n")
	address := freeLoopbackAddress(t)
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)

	served := make(chan error, 1)
	go func() {
		served <- serveDashboard(&invocation{projectsRoot: root}, command, deps, address, daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}, "owner-token", true, false)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("serveDashboard: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("serveDashboard did not return after its context was cancelled")
		}
	})
	waitForHealth(t, address)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(host, path string) (*http.Response, string) {
		t.Helper()
		request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+address+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = host
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response, string(body)
	}

	port := address[strings.LastIndex(address, ":")+1:]

	response, body := get(address, "/cockpit/")
	if response.StatusCode != http.StatusOK || (!strings.Contains(body, "pnpm install && pnpm build") && !strings.Contains(body, "<")) {
		t.Fatalf("/cockpit/ = %s %q, want the not-built page naming the build command", response.Status, body)
	}
	response, body = get("attacker.example:"+port, "/api/v1/cockpit/fleet")
	if response.StatusCode != http.StatusMisdirectedRequest || !strings.Contains(body, "misdirected request") || strings.Contains(body, "fleet") {
		t.Fatalf("foreign host = %s %q, want 421 and no fleet data", response.Status, body)
	}
	response, body = get("localhost:"+port, "/cockpit/")
	if want := "http://127.0.0.1:" + port + "/cockpit/"; response.StatusCode != http.StatusTemporaryRedirect || response.Header.Get("Location") != want {
		t.Fatalf("loopback alias = %s %q %q, want a redirect to %s", response.Status, response.Header.Get("Location"), body, want)
	}
	response, body = get(address, "/api/v1/cockpit/fleet")
	if response.StatusCode != http.StatusNotFound || !strings.Contains(body, `"error"`) {
		t.Fatalf("unknown cockpit api route = %s %q, want a JSON 404", response.Status, body)
	}
	response, body = get("attacker.example:"+port, "/")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "WB operations") {
		t.Fatalf("/ on a foreign host = %s %q, want it unchanged", response.Status, body)
	}
	response, body = get(address, "/metrics")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "WB Metrics") {
		t.Fatalf("/metrics = %s %q", response.Status, body)
	}
	response, body = get(address, "/api/v1/overview")
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `"schema_version"`) {
		t.Fatalf("/api/v1/overview = %s %q", response.Status, body)
	}
}

// cockpitConfigFile writes a wb.yaml and returns the path function the daemon's
// dependencies take.
func cockpitConfigFile(t *testing.T, content string) func() string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return func() string { return path }
}

func TestCockpitInvalidConfigurationStopsTheDaemonFromServing(t *testing.T) {
	root := daemonShutdownTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.hubConfigPath = cockpitConfigFile(t, "cockpit:\n  hosted_url: not-a-url\n")
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	err := serveDashboard(&invocation{projectsRoot: root}, command, deps, freeLoopbackAddress(t), daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}, "owner-token", true, false)
	if err == nil || !strings.Contains(err.Error(), "cockpit configuration") || !strings.Contains(err.Error(), "cockpit.hosted_url") {
		t.Fatalf("serveDashboard = %v, want an error naming the cockpit section", err)
	}
}
