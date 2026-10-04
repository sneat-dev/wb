package daemonview

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/sneat-dev/wb/internal/daemonruntime"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/operationreceipt"
)

// formatOptionalTime renders a nil or zero time as "never", the way an
// operator reading `wb daemon status` before the first sweep has run
// expects, rather than a misleading 0001-01-01 timestamp.
func formatOptionalTime(at *time.Time) string {
	if at == nil || at.IsZero() {
		return "never"
	}
	return at.Format(time.RFC3339)
}

func Result(out io.Writer, format string, result daemonruntime.Result) error {
	if format == "json" {
		return writeJSON(out, result)
	}
	status := "absent"
	if result.Managed {
		status = string(result.State.Status)
	}
	if result.ReportedState != "" {
		status = result.ReportedState
	}
	_, err := fmt.Fprintf(out, "daemon %s: state=%s, process_manager_running=%t, api_reachable=%t, direct_transport_reachable=%t, installed_provenance=%t", result.Action, status, result.ProcessManagerRunning, result.Reachable, result.DirectTransportReachable, result.ProvenanceMatches)
	if err == nil && result.Identity != "" {
		_, err = fmt.Fprintf(out, ", identity=%s, ready_verified=%t", result.Identity, result.ReadyVerified)
	}
	if err == nil && result.RuntimeDir != "" {
		_, err = fmt.Fprintf(out, ", runtime=%s", result.RuntimeDir)
	}
	if err == nil && result.SocketPath != "" {
		_, err = fmt.Fprintf(out, ", socket=%s", result.SocketPath)
	}
	if err == nil && result.IdentityDetail != "" {
		_, err = fmt.Fprintf(out, ", identity_detail=%q", result.IdentityDetail)
	}
	if err == nil && result.State.StoppedReason != "" {
		_, err = fmt.Fprintf(out, ", stopped_reason=%q", result.State.StoppedReason)
	}
	if err == nil && result.LegacyRuntime != nil {
		_, err = fmt.Fprintf(out, ", legacy_runtime=%q", result.LegacyRuntime.RuntimeDir)
	}
	if err == nil && result.Managed {
		_, err = fmt.Fprintf(out, ", supervisor=%s", result.State.Supervisor)
	}
	if err == nil && result.SupervisorMismatch != "" {
		_, err = fmt.Fprintf(out, ", supervisor_mismatch=%q", result.SupervisorMismatch)
	}
	if err == nil && result.Warning != "" {
		_, err = fmt.Fprintf(out, ", warning=%q", result.Warning)
	}
	if err == nil && result.ReachabilityTransport != "" {
		_, err = fmt.Fprintf(out, ", api_transport=%s", result.ReachabilityTransport)
	}
	if err == nil && result.DirectTransportError != "" {
		_, err = fmt.Fprintf(out, ", direct_transport_error=%q", result.DirectTransportError)
	}
	if err == nil && result.ReachabilityError != "" {
		_, err = fmt.Fprintf(out, ", api_probe_error=%q", result.ReachabilityError)
	}
	if err == nil {
		_, err = fmt.Fprintf(out, ", hub_mounted=%t", result.Hub.Mounted)
	}
	if err == nil && result.Hub.Mounted {
		_, err = fmt.Fprintf(out, ", hub_engine=%s, hub_store=%q, hub_listen=%s", result.Hub.Engine, result.Hub.Store, result.Hub.Listen)
	}
	if err == nil && result.Hub.Mounted {
		_, err = fmt.Fprintf(out, ", hub_polling=%t, hub_poll_interval=%s, hub_repositories_polled=%d, hub_webhook=%t", result.Hub.Polling, result.Hub.PollInterval, result.Hub.RepositoriesPolled, result.Hub.Webhook)
	}
	if err == nil && result.Hub.WebhookPublicURL != "" {
		_, err = fmt.Fprintf(out, ", hub_webhook_public_url=%s", result.Hub.WebhookPublicURL)
	}
	if err == nil && result.Hub.LastEventReceived != nil {
		_, err = fmt.Fprintf(out, ", hub_last_event_received=%q", result.Hub.LastEventReceived.ID)
	}
	if err == nil && result.Hub.LastEventAcknowledged != nil {
		_, err = fmt.Fprintf(out, ", hub_last_event_acknowledged=%q", result.Hub.LastEventAcknowledged.ID)
	}
	if err == nil && result.Hub.WebhookRedelivery != nil {
		_, err = fmt.Fprintf(out, ", hub_webhook_redelivery_last_sweep=%s, hub_webhook_redelivered=%d, hub_webhook_abandoned=%d, hub_webhook_redelivered_uncounted=%d",
			formatOptionalTime(result.Hub.WebhookRedelivery.LastSweepAt), result.Hub.WebhookRedelivery.Redelivered, result.Hub.WebhookRedelivery.Abandoned, result.Hub.WebhookRedelivery.Uncounted)
	}
	if err == nil && result.Hub.WebhookRedelivery != nil && result.Hub.WebhookRedelivery.LastFailureAt != nil {
		_, err = fmt.Fprintf(out, ", hub_webhook_redelivery_last_failure=%s, hub_webhook_redelivery_last_failure_class=%s",
			formatOptionalTime(result.Hub.WebhookRedelivery.LastFailureAt), result.Hub.WebhookRedelivery.LastFailureClass)
	}
	if err == nil {
		_, err = fmt.Fprintln(out)
	}
	return err
}
func Operation(out io.Writer, format string, operation *daemonv1.Operation) error {
	result := operationreceipt.FromOperation(operation)
	if format == "json" {
		return writeJSON(out, result)
	}
	if _, err := fmt.Fprintf(out, "operation %s: state=%s, cursor=%s, cpu_units=%d", result.OperationID, result.State, result.Cursor, result.CPUUnits); err != nil {
		return err
	}
	if result.TargetWorkerID != "" {
		if _, err := fmt.Fprintf(out, ", target_worker=%s", result.TargetWorkerID); err != nil {
			return err
		}
	}
	if result.FinishedUnixMilli != 0 {
		if _, err := fmt.Fprintf(out, ", exit_code=%d", result.ExitCode); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	if result.StdoutTail != "" {
		if _, err := io.WriteString(out, result.StdoutTail); err != nil {
			return err
		}
	}
	if result.StderrTail != "" {
		_, err := fmt.Fprintf(out, "\nstderr:\n%s", result.StderrTail)
		return err
	}
	return nil
}

func writeJSON(out io.Writer, payload any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(payload)
}
func Recovery(out io.Writer, format string, result daemonruntime.RecoveryResult) error {
	if format == "json" {
		return writeJSON(out, result)
	}
	_, err := fmt.Fprintf(out, "daemon recover: eligible=%t, applied=%t, lock_present=%t, owner_pid=%d, owner_alive=%t, state=%s, reason=%s, detail=%q\n", result.Eligible, result.Applied, result.LockPresent, result.OwnerPID, result.OwnerAlive, result.StateStatus, result.Reason, result.Detail)
	return err
}
