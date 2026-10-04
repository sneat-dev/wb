package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemonruntime"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/narrate"
	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/hubstore"
)

// TestHubMountStartsThePollerOnlyWithATokenFile is the configuration contract:
// a token is the one thing polling needs, and without it the hub still serves
// the dashboard and waits for webhooks instead.
func TestHubMountStartsThePollerOnlyWithATokenFile(t *testing.T) {
	ctx := context.Background()

	withToken := memoryHubConfig(t)
	mount, err := mountHub(ctx, withToken, "127.0.0.1:8791", narrate.Writer{}, nil)
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	defer func() { _ = mount.Close() }()
	if mount.Poller == nil {
		t.Fatal("a hub with a token file must poll")
	}
	if mount.Interval != hubconfig.DefaultPollInterval {
		t.Fatalf("interval = %s", mount.Interval)
	}

	withoutToken := hubTestConfig(t, "hub:\n  store:\n    engine: memory\n")
	silent, err := mountHub(ctx, withoutToken, "127.0.0.1:8792", narrate.Writer{}, nil)
	if err != nil || silent == nil {
		t.Fatalf("mountHub = %v, %v", silent, err)
	}
	defer func() { _ = silent.Close() }()
	if silent.Poller != nil {
		t.Fatal("a hub with no token file has nothing to ask GitHub with")
	}

	// Webhook mode replaces polling even when a token file is present: the
	// App reports every push, so only the repository an event names is pulled.
	withApp := appHubConfig(t, "0123456789abcdef0123456789abcdef")
	webhook, err := mountHub(ctx, withApp, "127.0.0.1:8793", narrate.Writer{}, nil)
	if err != nil || webhook == nil {
		t.Fatalf("mountHub = %v, %v", webhook, err)
	}
	defer func() { _ = webhook.Close() }()
	if webhook.Poller != nil {
		t.Fatal("a hub in webhook mode must not poll GitHub")
	}
	// startPolling is safe on both, and on a mount that does not exist.
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	silent.startPolling(stopped)
	mount.startPolling(stopped)
	var absent *hubMount
	absent.startPolling(stopped)
	if absent.hubHealth() != nil {
		t.Fatal("a daemon with no hub must not publish a hub health block")
	}
}

// TestHubGitHubTokenIsReadPerTickAndNeverNarrated proves the token reaches
// GitHub and nothing else: an unreadable or empty file is an error naming the
// path, and a good file yields the trimmed token.
func TestHubGitHubTokenIsReadPerTickAndNeverNarrated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "github.token")
	read := hubGitHubToken(path)

	if _, err := read(); err == nil || strings.Contains(err.Error(), "ghp_") {
		t.Fatalf("missing token file = %v", err)
	}
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := read(); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("empty token file = %v", err)
	}
	if err := os.WriteFile(path, []byte("ghp_secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := read()
	if err != nil || token != "ghp_secret" {
		t.Fatalf("token = %q, %v", token, err)
	}
}

// TestHubHealthReportsPollingAndDeliveryMarkers is what `wb daemon status`
// reads from another process: the serving daemon is the only one that knows
// how many repositories the last tick read, and the delivery markers come
// from the hub's own StatusService rather than a second reader of the store.
func TestHubHealthReportsPollingAndDeliveryMarkers(t *testing.T) {
	ctx := context.Background()
	configPath := memoryHubConfig(t)
	mount, err := mountHub(ctx, configPath, "127.0.0.1:8793", narrate.Writer{}, nil)
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	defer func() { _ = mount.Close() }()

	health := mount.health(ctx)
	if !health.Mounted || !health.Polling || health.PollIntervalSeconds != 1200 {
		t.Fatalf("health = %+v", health)
	}
	if health.LastEventReceived != nil || health.LastEventAcknowledged != nil {
		t.Fatalf("a hub that has handled nothing must report no markers: %+v", health)
	}

	// A status service that cannot answer must not fail the health endpoint.
	mount.status = &hub.StatusService{}
	if degraded := mount.health(ctx); !degraded.Mounted || degraded.LastEventReceived != nil {
		t.Fatalf("degraded health = %+v", degraded)
	}
	mount.status = nil
	if bare := mount.health(ctx); !bare.Mounted {
		t.Fatalf("bare health = %+v", bare)
	}
}

// TestHubDeliveryMarkerIsOptional keeps a hub that has received but not
// acknowledged anything from inventing a marker.
func TestHubDeliveryMarkerIsOptional(t *testing.T) {
	if hubDeliveryMarker(nil) != nil {
		t.Fatal("an absent marker must stay absent")
	}
	at := time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC)
	got := hubDeliveryMarker(&hub.StatusDeliveryMarker{DeliveryID: "poll:acme_app:default_branch_updated:abc", Event: "default_branch_updated", OccurredAt: at})
	if got == nil || got.ID != "poll:acme_app:default_branch_updated:abc" || got.Event != "default_branch_updated" || !got.OccurredAt.Equal(at) {
		t.Fatalf("marker = %+v", got)
	}
}

// TestDaemonStatusReportsPollingFromTheRunningDaemon is the status half of
// Task 2: the declaration supplies polling and interval, and the live health
// endpoint supplies what only the serving process knows.

// status is a small shim so each case above reads as one line.

// TestDaemonHubHealthReadsTheServingDaemon covers the HTTP read itself,
// including the answers it must refuse.

// TestServeQuietSilencesTheConsoleWithoutChangingTheHub is the --quiet
// contract: the flag reaches the narrator and nothing else.
func TestServeQuietSilencesTheConsoleWithoutChangingTheHub(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		t.Run(fmt.Sprintf("quiet=%t", quiet), func(t *testing.T) {
			var out bytes.Buffer
			writer := narrate.Writer{Out: &out, Quiet: quiet}
			store, closer, err := hubstore.Open(context.Background(), hubconfig.Store{Engine: hubconfig.EngineMemory})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = closer.Close() }()
			events, _ := hub.NewRepositoryEventStore(store)
			service := hub.RepositoryEventService{
				Snapshots: emptySnapshots{}, Entitlements: noEntitlements{}, Store: events, Narrate: writer.Write,
			}
			payload := []byte(`{"ref":"refs/heads/main","after":"0123456789abcdef0123456789abcdef01234567","repository":{"id":987,"full_name":"acme/app","default_branch":"main"},"installation":{"id":123}}`)
			if _, err := service.EnqueueWebhook(context.Background(), hub.WebhookDelivery{ID: "delivery-1", Event: "push", Payload: payload}); err != nil {
				t.Fatal(err)
			}
			if quiet && out.Len() != 0 {
				t.Fatalf("--quiet still narrated %q", out.String())
			}
			if !quiet && !strings.Contains(out.String(), "ignored: no entitled machine") {
				t.Fatalf("narration = %q", out.String())
			}
		})
	}
}

type emptySnapshots struct{}

func (emptySnapshots) StoreLatest(context.Context, hub.StoredMachineSnapshot) (hub.MachineSnapshotStoreResult, error) {
	panic("not used")
}
func (emptySnapshots) ListLatest(context.Context) ([]hub.StoredMachineSnapshot, error) {
	return nil, nil
}

type noEntitlements struct{}

func (noEntitlements) IdentityHasRepositoryEntitlement(context.Context, string, int64, int64) (bool, error) {
	return false, nil
}

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
		served <- serveDashboard(projectsRoot, command.Context(), command.OutOrStdout(), command.ErrOrStderr(), deps, address, daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}, "owner-token", true, false)
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
