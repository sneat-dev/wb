package main

import (
	"bytes"
	"strings"
	"testing"

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
