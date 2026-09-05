package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestLiveProgressReplacesAndTerminatesLine(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newLiveProgress(&out, true)
	progress.start("work: starting")
	progress.update("work: one")
	progress.update("work: two")
	progress.finish("work: done")

	rendered := out.String()
	for _, want := range []string{"work: starting", "work: one (", "work: two (", "work: done ("} {
		if !strings.Contains(rendered, want) {
			t.Errorf("progress output missing %q: %q", want, rendered)
		}
	}
	if !strings.HasSuffix(rendered, "\n") {
		t.Errorf("finished progress did not end its live line: %q", rendered)
	}
}

func TestLiveProgressHeartbeatsWhileOperationIsBlocked(t *testing.T) {
	var out bytes.Buffer
	progress := newLiveProgressWithHeartbeat(&out, true, 5*time.Millisecond)
	progress.start("cleanup: alive")
	time.Sleep(18 * time.Millisecond)
	progress.finish("cleanup: complete")
	finishedLength := out.Len()

	if count := strings.Count(out.String(), "cleanup: alive"); count < 3 {
		t.Fatalf("blocked operation emitted %d liveness events, want at least 3: %q", count, out.String())
	}
	time.Sleep(12 * time.Millisecond)
	if out.Len() != finishedLength {
		t.Fatalf("heartbeat wrote after finish: before=%d after=%d", finishedLength, out.Len())
	}
}

func TestUniversalProgressHeartbeatIsTenSeconds(t *testing.T) {
	if universalProgressHeartbeat != 10*time.Second {
		t.Fatalf("universal progress heartbeat = %s, want 10s", universalProgressHeartbeat)
	}
}

func TestLiveProgressCanBeDisabled(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newLiveProgress(&out, false)
	progress.start("starting")
	progress.update("working")
	progress.finish("done")
	if out.Len() != 0 {
		t.Fatalf("disabled progress wrote %q", out.String())
	}
}
