package progress

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestLiveProgressReplacesAndTerminatesLine(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := NewLive(&out, true)
	progress.Start("work: starting")
	progress.Update("work: one")
	progress.Update("work: two")
	progress.Finish("work: done")

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
	t.Parallel()
	var out bytes.Buffer
	progress := NewLiveWithHeartbeat(&out, true, 5*time.Millisecond)
	progress.Start("cleanup: alive")
	time.Sleep(18 * time.Millisecond)
	progress.Finish("cleanup: complete")
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
	t.Parallel()
	if Heartbeat != 10*time.Second {
		t.Fatalf("universal progress heartbeat = %s, want 10s", Heartbeat)
	}
}

func TestLiveProgressCanBeDisabled(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := NewLive(&out, false)
	progress.Start("starting")
	progress.Update("working")
	progress.Finish("done")
	if out.Len() != 0 {
		t.Fatalf("disabled progress wrote %q", out.String())
	}
}
