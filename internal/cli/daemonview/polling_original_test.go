package daemonview

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemonruntime"
)

func TestDaemonStatusRendersThePollingColumns(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	err := Result(&out, "text", daemonruntime.Result{Action: "status", Hub: daemonruntime.HubStatus{
		Mounted: true, Engine: "memory", Store: "(in-memory; discarded on exit)", Listen: "127.0.0.1:8766",
		Polling: true, PollInterval: "20m0s", RepositoriesPolled: 7,
		LastEventReceived:     &daemonruntime.HubEventMarker{ID: "poll:acme_app:default_branch_updated:abc", OccurredAt: at},
		LastEventAcknowledged: &daemonruntime.HubEventMarker{ID: "poll:acme_app:default_branch_updated:abc", OccurredAt: at},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"hub_polling=true", "hub_poll_interval=20m0s", "hub_repositories_polled=7",
		`hub_last_event_received="poll:acme_app:default_branch_updated:abc"`,
		`hub_last_event_acknowledged="poll:acme_app:default_branch_updated:abc"`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status text %q does not contain %q", out.String(), want)
		}
	}
}
