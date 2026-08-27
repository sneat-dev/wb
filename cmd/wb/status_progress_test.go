package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestStatusProgressRendersCompletionCounter(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newStatusProgress(&out, true)
	progress.start(2)
	progress.complete(
		qualityTarget{repository: "acme/one"},
		repositoryStatusInfo{Repository: "acme/one", Status: "clean"},
	)
	progress.complete(
		qualityTarget{repository: "acme/two"},
		repositoryStatusInfo{Repository: "acme/two", Status: "attention"},
	)
	progress.finish()

	rendered := out.String()
	for _, want := range []string{
		"status: 0/2 repositories inspected",
		"status: 1/2 repositories inspected; acme/one: clean",
		"status: 2/2 repositories inspected; acme/two: attention",
		"status: inspected 2 repositories in",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("progress output missing %q: %q", want, rendered)
		}
	}
	if !strings.HasSuffix(rendered, "\n") {
		t.Errorf("finished progress did not end its live line: %q", rendered)
	}
}

func TestStatusProgressHeartbeatRefreshesAndStops(t *testing.T) {
	var out bytes.Buffer
	progress := newStatusProgressWithHeartbeat(&out, true, 5*time.Millisecond)
	progress.start(1)
	time.Sleep(18 * time.Millisecond)
	progress.finish()
	finishedLength := out.Len()
	if count := strings.Count(out.String(), "status: 0/1 repositories inspected"); count < 2 {
		t.Fatalf("heartbeat rendered status %d times, want at least 2: %q", count, out.String())
	}
	time.Sleep(12 * time.Millisecond)
	if out.Len() != finishedLength {
		t.Fatalf("heartbeat wrote after finish: before=%d after=%d", finishedLength, out.Len())
	}
}

func TestStatusProgressCanBeDisabled(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newStatusProgress(&out, false)
	progress.start(1)
	progress.complete(
		qualityTarget{repository: "acme/one"},
		repositoryStatusInfo{Repository: "acme/one", Status: "clean"},
	)
	progress.finish()
	if out.Len() != 0 {
		t.Fatalf("disabled progress wrote %q", out.String())
	}
}
