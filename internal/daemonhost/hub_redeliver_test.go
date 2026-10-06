package daemonhost

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/hub/narrate"
)

func TestMountHubWiresAndRunsTheRedeliverySweep(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	server := fakeAppDeliveriesServer(t, now.Add(-1*time.Hour))
	configPath := appHubConfigWithKey(t, webhookSecret, realTestAppPrivateKeyPEM)

	var console bytes.Buffer
	mount, err := mountHub(context.Background(), configPath, "127.0.0.1:8806", narrate.Writer{Out: &console}, &Tuning{APIBaseURL: server.URL, Now: func() time.Time { return now }})
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	t.Cleanup(func() { _ = mount.Close() })

	sweeper := mount.Webhook.Sweeper()
	if sweeper == nil {
		t.Fatal("webhook mode must wire a redelivery sweeper")
	}
	sweeper.Sweep(context.Background())

	if !strings.Contains(console.String(), "redeliver") || !strings.Contains(console.String(), "redelivered (attempt 1 of 3)") {
		t.Fatalf("narration = %q", console.String())
	}

	health := mount.health(context.Background())
	if health.WebhookRedelivery == nil || health.WebhookRedelivery.Redelivered != 1 || health.WebhookRedelivery.Abandoned != 0 {
		t.Fatalf("health.WebhookRedelivery = %+v", health.WebhookRedelivery)
	}
	if health.WebhookRedelivery.LastSweepAt == nil || !health.WebhookRedelivery.LastSweepAt.Equal(now) {
		t.Fatalf("health.WebhookRedelivery.LastSweepAt = %v, want %s", health.WebhookRedelivery.LastSweepAt, now)
	}
}

func TestNoRedeliverySweepWithoutAnApp(t *testing.T) {
	t.Parallel()
	mount, err := mountHub(context.Background(), memoryHubConfig(t), "127.0.0.1:8807", narrate.Writer{}, nil)
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	t.Cleanup(func() { _ = mount.Close() })

	if mount.Webhook != nil {
		t.Fatal("a hub with no hub.github.app must not be in webhook mode")
	}
	if got := mount.Webhook.Sweeper(); got != nil {
		t.Fatalf("Sweeper() on a nil webhook mode = %v, want nil", got)
	}
	// startRedeliverySweep must not panic reaching into a nil webhookMode.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mount.startRedeliverySweep(ctx)

	if health := mount.health(context.Background()); health.WebhookRedelivery != nil {
		t.Fatalf("health.WebhookRedelivery = %+v, want nil without an App", health.WebhookRedelivery)
	}
}
