package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemonhost"
	"github.com/sneat-dev/wb/internal/daemonruntime"

	"github.com/spf13/cobra"
)

// TestHubMountStartsThePollerOnlyWithATokenFile is the configuration contract:
// a token is the one thing polling needs, and without it the hub still serves
// the dashboard and waits for webhooks instead.

// TestHubGitHubTokenIsReadPerTickAndNeverNarrated proves the token reaches
// GitHub and nothing else: an unreadable or empty file is an error naming the
// path, and a good file yields the trimmed token.

// TestHubHealthReportsPollingAndDeliveryMarkers is what `wb daemon status`
// reads from another process: the serving daemon is the only one that knows
// how many repositories the last tick read, and the delivery markers come
// from the hub's own StatusService rather than a second reader of the store.

// TestHubDeliveryMarkerIsOptional keeps a hub that has received but not
// acknowledged anything from inventing a marker.

// TestDaemonStatusReportsPollingFromTheRunningDaemon is the status half of
// Task 2: the declaration supplies polling and interval, and the live health
// endpoint supplies what only the serving process knows.

// status is a small shim so each case above reads as one line.

// TestDaemonHubHealthReadsTheServingDaemon covers the HTTP read itself,
// including the answers it must refuse.

// TestServeQuietSilencesTheConsoleWithoutChangingTheHub is the --quiet
// contract: the flag reaches the narrator and nothing else.

// TestDaemonServeAcceptsQuietAndStartNeverPassesIt is the flag's whole
// surface: `wb daemon serve` takes it, and `wb daemon start` must not, so the
// detached daemon's log keeps every line.
func TestDaemonServeAcceptsQuietAndStartNeverPassesIt(t *testing.T) {
	root := daemonTestRoot(t)
	serve := daemonCommandForTest("serve", &invocation{projectsRoot: root}, defaultDaemonDependencies())
	if serve.Flags().Lookup("quiet") == nil {
		t.Fatal("wb daemon serve has no --quiet")
	}

	deps := daemonTestDependencies(t, root)
	var launched []string
	previousStart := deps.Start
	deps.Start = func(executable string, args []string, logPath string) (int, error) {
		launched = append([]string(nil), args...)
		return previousStart(executable, args, logPath)
	}
	_, _ = newDaemonController(deps, root).Start(context.Background(), daemonruntime.DefaultListen)
	for _, argument := range launched {
		if argument == "--quiet" {
			t.Fatalf("daemon start passed --quiet: %v", launched)
		}
	}
	if len(launched) == 0 || launched[3] != "serve" {
		t.Fatalf("daemon start args = %v", launched)
	}
}

// TestServeDashboardPublishesHubHealth proves the hub block actually reaches
// /api/v1/health on a running loopback daemon, which is the only way
// `wb daemon status` can see it.
func TestServeDashboardPublishesHubHealth(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "wb-poll-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	pinDaemonHome(t, root)
	projectsRoot := root

	configPath := memoryHubConfig(t)
	deps := daemonTestDependencies(t, root)
	deps.Token = func() (string, error) { return "owner-token", nil }
	deps.HubConfigPath = func() string { return configPath }

	address := freeLoopbackAddress(t)
	command := &cobra.Command{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command.SetContext(ctx)
	var stdout bytes.Buffer
	// Several of the daemon's goroutines write to stderr while the test reads it.
	stderr := &lockedBuffer{}
	command.SetOut(&stdout)
	command.SetErr(stderr)
	served := make(chan error, 1)
	go func() {
		served <- newDaemonHost(deps).Serve(command.Context(), daemonhost.Request{ProjectsRoot: projectsRoot, Listen: address, Quiet: true, ManagedStart: false}, command.OutOrStdout(), command.ErrOrStderr())
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

	_, body := fetch(t, "http://"+address+"/api/v1/health")
	var payload struct {
		Hub *struct {
			Mounted             bool    `json:"mounted"`
			Polling             bool    `json:"polling"`
			PollIntervalSeconds float64 `json:"poll_interval_seconds"`
		} `json:"hub"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("health is not JSON: %v\n%s", err, body)
	}
	if payload.Hub == nil || !payload.Hub.Mounted || !payload.Hub.Polling || payload.Hub.PollIntervalSeconds != 1200 {
		t.Fatalf("health hub block = %+v (%s)", payload.Hub, body)
	}
	// --quiet was passed, so the hub's per-event lines are silenced while the
	// daemon's own start line, which is not narration, still appears.
	if !strings.Contains(stderr.String(), "WB hub: engine=memory") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
