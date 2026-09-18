package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/hub/narrate"
)

// realTestAppPrivateKeyPEM is a freshly generated RSA key, unlike the
// package's testPrivateKeyPEM placeholder: the missed-webhook recovery sweep
// actually signs a JWT with it, which the placeholder cannot parse.
var realTestAppPrivateKeyPEM = generateRealTestAppPrivateKeyPEM()

func generateRealTestAppPrivateKeyPEM() string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

// fakeAppDeliveriesServer serves just enough of GET /app/hook/deliveries and
// POST /app/hook/deliveries/{id}/attempts for a wiring test: one failed
// delivery, redelivered once. A second, already-successful delivery is
// included as the reachability evidence the sweep now requires (S1) before
// it will spend a counted attempt on the failing one.
func fakeAppDeliveriesServer(t *testing.T, deliveredAt time.Time) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/app/hook/deliveries", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `[{"id":2,"guid":"wiring-evidence","delivered_at":%q,"redelivery":false,"status_code":200,"event":"push"},{"id":1,"guid":"wiring-guid","delivered_at":%q,"redelivery":false,"status_code":500,"event":"push"}]`,
			deliveredAt.Add(1*time.Minute).UTC().Format(time.RFC3339), deliveredAt.UTC().Format(time.RFC3339))
	})
	mux.HandleFunc("/app/hook/deliveries/1/attempts", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusAccepted)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// TestMountHubWiresAndRunsTheRedeliverySweep proves the sweeper mountHub
// builds is the real thing: pointed at a fake GitHub App API, one Sweep call
// redelivers the one failed delivery, narrates it, and the result shows up
// in mount.health, exactly the way `wb daemon status` reads it.
func TestMountHubWiresAndRunsTheRedeliverySweep(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	server := fakeAppDeliveriesServer(t, now.Add(-1*time.Hour))
	configPath := appHubConfigWithKey(t, webhookSecret, realTestAppPrivateKeyPEM)

	var console bytes.Buffer
	mount, err := mountHub(context.Background(), configPath, "127.0.0.1:8806", narrate.Writer{Out: &console}, &hubTuning{APIBaseURL: server.URL, Now: func() time.Time { return now }})
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	defer func() { _ = mount.Close() }()

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

// TestNoRedeliverySweepWithoutAnApp is the AC's own requirement: there is no
// activity when hub.github.app is not configured. Without an App, mountHub
// never builds a webhookMode at all, so there is no sweeper to start or to
// report on.
func TestNoRedeliverySweepWithoutAnApp(t *testing.T) {
	mount, err := mountHub(context.Background(), memoryHubConfig(t), "127.0.0.1:8807", narrate.Writer{}, nil)
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	defer func() { _ = mount.Close() }()

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

// TestDaemonStatusReportsTheRedeliverySweep is the status half: the live
// health endpoint's numbers reach `wb daemon status`, in JSON and in text.
func TestDaemonStatusReportsTheRedeliverySweep(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.hubConfigPath = func() string { return appHubConfig(t, webhookSecret) }
	at := time.Date(2026, 9, 18, 14, 0, 0, 0, time.UTC)
	failedAt := at.Add(-time.Hour)
	deps.hubHealth = func(context.Context, string) (daemonHubStatus, error) {
		return daemonHubStatus{WebhookRedelivery: &daemonHubRedeliverySweep{
			LastSweepAt: &at, Redelivered: 2, Abandoned: 1,
			LastFailureAt: &failedAt, LastFailureClass: "rate_limited",
		}}, nil
	}

	status := newDaemonController(deps, root).hubStatus(context.Background(), "127.0.0.1:8765")
	if status.WebhookRedelivery == nil || status.WebhookRedelivery.Redelivered != 2 || status.WebhookRedelivery.Abandoned != 1 ||
		status.WebhookRedelivery.LastSweepAt == nil || !status.WebhookRedelivery.LastSweepAt.Equal(at) ||
		status.WebhookRedelivery.LastFailureAt == nil || !status.WebhookRedelivery.LastFailureAt.Equal(failedAt) ||
		status.WebhookRedelivery.LastFailureClass != "rate_limited" {
		t.Fatalf("hub status = %+v", status.WebhookRedelivery)
	}

	var out bytes.Buffer
	if err := writeDaemonResult(&out, "text", daemonResult{Action: "status", Hub: status}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"hub_webhook_redelivered=2", "hub_webhook_abandoned=1", "hub_webhook_redelivery_last_sweep=" + at.Format(time.RFC3339),
		"hub_webhook_redelivery_last_failure=" + failedAt.Format(time.RFC3339), "hub_webhook_redelivery_last_failure_class=rate_limited",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status text %q does not contain %q", out.String(), want)
		}
	}

	// The daemon is not running, or has never swept: the field stays absent
	// rather than inventing a zero-value sweep.
	deps.hubHealth = func(context.Context, string) (daemonHubStatus, error) { return daemonHubStatus{}, nil }
	quiet := newDaemonController(deps, root).hubStatus(context.Background(), "127.0.0.1:8765")
	if quiet.WebhookRedelivery != nil {
		t.Fatalf("hub status without a live sweep = %+v", quiet.WebhookRedelivery)
	}
	out.Reset()
	if err := writeDaemonResult(&out, "text", daemonResult{Action: "status", Hub: quiet}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "hub_webhook_redelivered") {
		t.Fatalf("status text = %q, must omit the sweep columns without one", out.String())
	}
}
