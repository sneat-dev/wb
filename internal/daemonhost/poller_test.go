package daemonhost

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/narrate"
	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/hubstore"
)

func TestHubMountStartsThePollerOnlyWithATokenFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	withToken := memoryHubConfig(t)
	mount, err := mountHub(ctx, withToken, "127.0.0.1:8791", narrate.Writer{}, nil)
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	t.Cleanup(func() { _ = mount.Close() })
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
	t.Cleanup(func() { _ = silent.Close() })
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
	t.Cleanup(func() { _ = webhook.Close() })
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

func TestHubGitHubTokenIsReadPerTickAndNeverNarrated(t *testing.T) {
	t.Parallel()
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

func TestHubHealthReportsPollingAndDeliveryMarkers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	configPath := memoryHubConfig(t)
	mount, err := mountHub(ctx, configPath, "127.0.0.1:8793", narrate.Writer{}, nil)
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	t.Cleanup(func() { _ = mount.Close() })

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

func TestHubDeliveryMarkerIsOptional(t *testing.T) {
	t.Parallel()
	if hubDeliveryMarker(nil) != nil {
		t.Fatal("an absent marker must stay absent")
	}
	at := time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC)
	got := hubDeliveryMarker(&hub.StatusDeliveryMarker{DeliveryID: "poll:acme_app:default_branch_updated:abc", Event: "default_branch_updated", OccurredAt: at})
	if got == nil || got.ID != "poll:acme_app:default_branch_updated:abc" || got.Event != "default_branch_updated" || !got.OccurredAt.Equal(at) {
		t.Fatalf("marker = %+v", got)
	}
}

func TestServeQuietSilencesTheConsoleWithoutChangingTheHub(t *testing.T) {
	t.Parallel()
	for _, quiet := range []bool{false, true} {
		t.Run(fmt.Sprintf("quiet=%t", quiet), func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			writer := narrate.Writer{Out: &out, Quiet: quiet}
			store, closer, err := hubstore.Open(context.Background(), hubconfig.Store{Engine: hubconfig.EngineMemory})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = closer.Close() })
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
