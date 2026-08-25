package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
)

func TestCampaignProgressRendersTypedPhase(t *testing.T) {
	var output bytes.Buffer
	campaign := newCampaignProgress(&output, true, "deps graph")
	reporter := campaign.reporter()
	if reporter == nil {
		t.Fatal("expected enabled reporter")
	}
	reporter(progress.Event{Phase: "discover_graph", Repository: "sneat-dev/wb", Completed: 2, Total: 5, State: progress.Running})
	campaign.finish("completed")

	got := output.String()
	for _, want := range []string{"deps graph", "discover graph", "sneat-dev/wb", "2/5", "completed", "\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress output %q does not contain %q", got, want)
		}
	}
}

func TestCampaignProgressRendersLayerZeroDetailAndState(t *testing.T) {
	var output bytes.Buffer
	campaign := newCampaignProgressWithHeartbeat(&output, true, "deps set", 0)
	campaign.report(progress.Event{
		Phase: "process_layer", Layer: progress.Index(0), Detail: "2 repositories", State: progress.Failed,
	})
	campaign.finish("failed")

	got := output.String()
	for _, want := range []string{"layer 0", "2 repositories", "failed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress output %q does not contain %q", got, want)
		}
	}
}

func TestCampaignProgressHeartbeatRefreshesAndStops(t *testing.T) {
	var output bytes.Buffer
	campaign := newCampaignProgressWithHeartbeat(&output, true, "deps bump", 5*time.Millisecond)
	campaign.report(progress.Event{Phase: "observe_releases", State: progress.Waiting})
	time.Sleep(18 * time.Millisecond)
	campaign.finish("completed")
	finishedLength := output.Len()
	if count := strings.Count(output.String(), "observe releases"); count < 2 {
		t.Fatalf("heartbeat rendered phase %d times, want at least 2: %q", count, output.String())
	}
	time.Sleep(12 * time.Millisecond)
	if output.Len() != finishedLength {
		t.Fatalf("heartbeat wrote after finish: before=%d after=%d", finishedLength, output.Len())
	}
}

func TestCampaignProgressDisabledHasNoReporterOrOutput(t *testing.T) {
	var output bytes.Buffer
	campaign := newCampaignProgress(&output, false, "deps drift")
	if reporter := campaign.reporter(); reporter != nil {
		t.Fatal("disabled progress must not expose a reporter")
	}
	campaign.finish("completed")
	if output.Len() != 0 {
		t.Fatalf("disabled progress wrote %q", output.String())
	}
}
