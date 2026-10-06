package progress

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
)

func TestCampaignProgressRendersTypedPhase(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	campaign := NewCampaign(&output, true, "deps graph")
	reporter := campaign.Reporter()
	if reporter == nil {
		t.Fatal("expected enabled reporter")
	}
	reporter(progress.Event{Phase: "discover_graph", Repository: "sneat-dev/wb", Completed: 2, Total: 5, State: progress.Running})
	campaign.Finish("completed")

	got := output.String()
	for _, want := range []string{"deps graph", "discover graph", "sneat-dev/wb", "2/5", "completed", "\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress output %q does not contain %q", got, want)
		}
	}
}

func TestCampaignProgressRendersLayerZeroDetailAndState(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	campaign := NewCampaignWithHeartbeat(&output, true, "deps set", 0)
	campaign.Report(progress.Event{
		Phase: "process_layer", Layer: progress.Index(0), Detail: "2 repositories", State: progress.Failed,
	})
	campaign.Finish("failed")

	got := output.String()
	for _, want := range []string{"layer 0", "2 repositories", "failed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress output %q does not contain %q", got, want)
		}
	}
}

type campaignWriter struct {
	mu    sync.Mutex
	body  bytes.Buffer
	wrote chan struct{}
}

func (w *campaignWriter) Write(raw []byte) (int, error) {
	w.mu.Lock()
	n, err := w.body.Write(raw)
	w.mu.Unlock()
	select {
	case w.wrote <- struct{}{}:
	default:
	}
	return n, err
}
func (w *campaignWriter) snapshot() string { w.mu.Lock(); defer w.mu.Unlock(); return w.body.String() }
func waitCampaignOutput(t *testing.T, w *campaignWriter, ready func(string) bool) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for !ready(w.snapshot()) {
		select {
		case <-w.wrote:
		case <-deadline.C:
			t.Fatalf("heartbeat did not render: %q", w.snapshot())
		}
	}
}
func TestCampaignProgressHeartbeatRefreshesAndStops(t *testing.T) {
	t.Parallel()
	output := &campaignWriter{wrote: make(chan struct{}, 16)}
	campaign := NewCampaignWithHeartbeat(output, true, "deps bump", time.Millisecond)
	campaign.Report(progress.Event{Phase: "observe_releases", State: progress.Waiting})
	waitCampaignOutput(t, output, func(s string) bool { return strings.Count(s, "observe releases") >= 2 })
	campaign.Finish("completed")
	finished := output.snapshot()
	select {
	case <-campaign.stopped:
	default:
		t.Fatal("Finish did not join heartbeat")
	}
	campaign.refresh() // A tick already queued when Finish starts cannot render.
	campaign.Finish("again")
	if output.snapshot() != finished {
		t.Fatal("heartbeat wrote after finish")
	}
	if strings.Count(finished, "observe releases") < 2 {
		t.Fatal(finished)
	}
}
func TestCampaignProgressHeartbeatDoesNotRepeatCompletedPhaseAsCurrent(t *testing.T) {
	t.Parallel()
	output := &campaignWriter{wrote: make(chan struct{}, 16)}
	campaign := NewCampaignWithHeartbeat(output, true, "worktree merge", time.Millisecond)
	campaign.Report(progress.Event{Phase: "sync_canonical", Detail: "fast_forwarded", State: progress.Completed})
	waitCampaignOutput(t, output, func(s string) bool { return strings.Contains(s, "alive; last completed sync canonical") })
	campaign.Finish("completed")
	got := output.snapshot()
	if !strings.Contains(got, "alive; last completed sync canonical") {
		t.Fatal(got)
	}
	if count := strings.Count(got, "sync canonical: fast_forwarded: completed"); count != 1 {
		t.Fatalf("completed phase rendered %d times: %q", count, got)
	}
}

func TestCampaignProgressDisabledHasNoReporterOrOutput(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	campaign := NewCampaign(&output, false, "deps drift")
	if reporter := campaign.Reporter(); reporter != nil {
		t.Fatal("disabled progress must not expose a reporter")
	}
	campaign.Finish("completed")
	if output.Len() != 0 {
		t.Fatalf("disabled progress wrote %q", output.String())
	}
}

// TestCampaignProgressReportsWaveNumber drives report()'s `event.Wave > 0`
// branch: the rendered line must name the wave.
func TestCampaignProgressReportsWaveNumber(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	p := NewCampaignWithHeartbeat(&out, true, "sync", 0)
	p.Report(progress.Event{Wave: 3, State: progress.Running})
	if !strings.Contains(out.String(), "wave 3") {
		t.Fatalf("output = %q; want it to name wave 3", out.String())
	}
}

// TestCampaignProgressIgnoresReportsAfterFinish drives report()'s
// `p.finished` guard: an event reported after finish() must not overwrite
// the finished line.
func TestCampaignProgressIgnoresReportsAfterFinish(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	p := NewCampaignWithHeartbeat(&out, true, "sync", 0)
	p.Report(progress.Event{Detail: "starting up"})
	p.Finish("done")
	afterFinish := out.String()
	p.Report(progress.Event{Detail: "late event, must be dropped"})
	if out.String() != afterFinish {
		t.Fatalf("output changed after finish: before=%q after=%q", afterFinish, out.String())
	}
	if strings.Contains(out.String(), "late event") {
		t.Fatalf("output = %q; a post-finish report must not render", out.String())
	}
}

func TestNilCampaignHasNoReporterOrFinish(t *testing.T) {
	t.Parallel()
	var campaign *Campaign
	if campaign.Reporter() != nil {
		t.Fatal("nil reporter")
	}
	campaign.Finish("done")
}
func TestDefaultCampaignCadenceAndFinishWithoutEvents(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	campaign := NewCampaign(&out, true, "migrate")
	if campaign.heartbeat != time.Second {
		t.Fatal(campaign.heartbeat)
	}
	campaign.Finish("done")
	if !strings.Contains(out.String(), "migrate: done") {
		t.Fatal(out.String())
	}
}
