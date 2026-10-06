package daemonview

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/daemonruntime"
)

func TestResultAllOptionalColumnsAndEveryWriterFailure(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	r := daemonruntime.Result{Action: "status", Managed: true, ReportedState: "ready", Identity: daemonruntime.Identity("ours"), RuntimeDir: "runtime", SocketPath: "socket", IdentityDetail: "detail", State: daemonruntime.PublicState{Status: daemon.StatusReady, StoppedReason: "stopped", Supervisor: daemon.SupervisorLaunchd}, LegacyRuntime: &daemonruntime.LegacyEndpoint{RuntimeDir: "old"}, SupervisorMismatch: "mismatch", Warning: "warning", ReachabilityTransport: "bridge", DirectTransportError: "direct", ReachabilityError: "api", Hub: daemonruntime.HubStatus{Mounted: true, Engine: "engine", Store: "store", Listen: "listen", Polling: true, PollInterval: "5s", RepositoriesPolled: 2, Webhook: true, WebhookPublicURL: "url", LastEventReceived: &daemonruntime.HubEventMarker{ID: "received"}, LastEventAcknowledged: &daemonruntime.HubEventMarker{ID: "acked"}, WebhookRedelivery: &daemonruntime.HubRedeliverySweep{LastSweepAt: &now, LastFailureAt: &now, LastFailureClass: "failure", Redelivered: 1, Abandoned: 2, Uncounted: 3}}}
	var out bytes.Buffer
	if err := Result(&out, "text", r); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"state=ready", "identity=ours", "runtime=runtime", "socket=socket", "stopped_reason=", "legacy_runtime=", "supervisor=launchd", "supervisor_mismatch=", "warning=", "api_transport=bridge", "direct_transport_error=", "api_probe_error=", "hub_mounted=true", "hub_engine=engine", "hub_polling=true", "hub_webhook_public_url=url", "hub_last_event_received=", "hub_last_event_acknowledged=", "hub_webhook_redelivery_last_sweep=2026-01-02T03:04:05Z", "hub_webhook_redelivery_last_failure_class=failure"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %s: %s", want, out.String())
		}
	}
	for allow := 0; allow < 22; allow++ {
		if err := Result(&originalFailWriter{Allow: allow}, "text", r); err == nil {
			t.Fatalf("writer %d accepted", allow)
		}
	}
	r.ReportedState = ""
	r.Hub.WebhookRedelivery.LastSweepAt = nil
	r.Hub.WebhookRedelivery.LastFailureAt = nil
	if err := Result(&out, "text", r); err != nil {
		t.Fatal(err)
	}
	zero := time.Time{}
	if formatOptionalTime(&zero) != "never" || formatOptionalTime(nil) != "never" {
		t.Fatal("zero time")
	}
	if err := Result(&out, "text", daemonruntime.Result{}); err != nil {
		t.Fatal(err)
	}
}
func TestResultTypedJSONDateAndWriterErrors(t *testing.T) {
	t.Parallel()
	r := daemonruntime.Result{State: daemonruntime.PublicState{UpdatedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}
	var out bytes.Buffer
	if err := Result(&out, "json", r); err == nil || out.Len() != 0 {
		t.Fatalf("date=%v bytes=%q", err, out.String())
	}
	sentinel := errors.New("writer")
	if err := Result(viewFailure{sentinel}, "json", daemonruntime.Result{}); err != sentinel {
		t.Fatal(err)
	}
	if err := Recovery(viewFailure{sentinel}, "json", daemonruntime.RecoveryResult{}); err != sentinel {
		t.Fatal(err)
	}
	if err := Recovery(viewFailure{sentinel}, "text", daemonruntime.RecoveryResult{}); err != sentinel {
		t.Fatal(err)
	}
}

type viewFailure struct{ err error }

func (w viewFailure) Write([]byte) (int, error) { return 0, w.err }
