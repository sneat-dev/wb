package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/daemonview"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/daemonruntime"
)

// fakeAppDeliveriesServer serves just enough of GET /app/hook/deliveries and
// POST /app/hook/deliveries/{id}/attempts for a wiring test: one failed
// delivery, redelivered once. A second, already-successful delivery is
// included as the reachability evidence the sweep now requires (S1) before
// it will spend a counted attempt on the failing one.

// TestMountHubWiresAndRunsTheRedeliverySweep proves the sweeper mountHub
// builds is the real thing: pointed at a fake GitHub App API, one Sweep call
// redelivers the one failed delivery, narrates it, and the result shows up
// in mount.health, exactly the way `wb daemon status` reads it.

// TestNoRedeliverySweepWithoutAnApp is the AC's own requirement: there is no
// activity when hub.github.app is not configured. Without an App, mountHub
// never builds a webhookMode at all, so there is no sweeper to start or to
// report on.

// TestDaemonStatusReportsTheRedeliverySweep is the status half: the live
// health endpoint's numbers reach `wb daemon status`, in JSON and in text.
func TestDaemonStatusReportsTheRedeliverySweep(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.HubConfigPath = func() string { return appHubConfig(t, webhookSecret) }
	at := time.Date(2026, 9, 18, 14, 0, 0, 0, time.UTC)
	failedAt := at.Add(-time.Hour)
	deps.HubHealth = func(context.Context, string) (daemonHubStatus, error) {
		return daemonHubStatus{WebhookRedelivery: &daemonruntime.HubRedeliverySweep{
			LastSweepAt: &at, Redelivered: 2, Abandoned: 1, Uncounted: 4,
			LastFailureAt: &failedAt, LastFailureClass: "rate_limited",
		}}, nil
	}

	saved := daemon.State{SchemaVersion: daemon.StateSchemaVersion, Status: daemon.StatusStopped, Listen: "127.0.0.1:8765"}
	saved.Queue.SchemaVersion = daemon.QueueSchemaVersion
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}).Save(saved); err != nil {
		t.Fatal(err)
	}
	statusResult, statusErr := newDaemonController(deps, root).Status(context.Background())
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	status := statusResult.Hub
	if status.WebhookRedelivery == nil || status.WebhookRedelivery.Redelivered != 2 || status.WebhookRedelivery.Abandoned != 1 ||
		status.WebhookRedelivery.Uncounted != 4 ||
		status.WebhookRedelivery.LastSweepAt == nil || !status.WebhookRedelivery.LastSweepAt.Equal(at) ||
		status.WebhookRedelivery.LastFailureAt == nil || !status.WebhookRedelivery.LastFailureAt.Equal(failedAt) ||
		status.WebhookRedelivery.LastFailureClass != "rate_limited" {
		t.Fatalf("hub status = %+v", status.WebhookRedelivery)
	}

	var out bytes.Buffer
	if err := daemonview.Result(&out, "text", daemonResult{Action: "status", Hub: status}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"hub_webhook_redelivered=2", "hub_webhook_abandoned=1", "hub_webhook_redelivered_uncounted=4", "hub_webhook_redelivery_last_sweep=" + at.Format(time.RFC3339),
		"hub_webhook_redelivery_last_failure=" + failedAt.Format(time.RFC3339), "hub_webhook_redelivery_last_failure_class=rate_limited",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status text %q does not contain %q", out.String(), want)
		}
	}

	// The daemon is not running, or has never swept: the field stays absent
	// rather than inventing a zero-value sweep.
	deps.HubHealth = func(context.Context, string) (daemonHubStatus, error) { return daemonHubStatus{}, nil }
	quietResult, quietErr := newDaemonController(deps, root).Status(context.Background())
	if quietErr != nil {
		t.Fatal(quietErr)
	}
	quiet := quietResult.Hub
	if quiet.WebhookRedelivery != nil {
		t.Fatalf("hub status without a live sweep = %+v", quiet.WebhookRedelivery)
	}
	out.Reset()
	if err := daemonview.Result(&out, "text", daemonResult{Action: "status", Hub: quiet}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "hub_webhook_redelivered") {
		t.Fatalf("status text = %q, must omit the sweep columns without one", out.String())
	}
}
