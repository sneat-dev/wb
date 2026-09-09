package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runqueue"
)

func TestRunQueueProgressFormatsQueuedHeartbeatAdmittedAndDoneLines(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newRunQueueProgress(&out, true, "")

	state := runqueue.State{Position: 2, Total: 3, Holders: []runqueue.Holder{
		{Participant: runqueue.Participant{PID: 4242, Summary: "go build"}, StartedAt: time.Now().Add(-3 * time.Minute)},
	}}
	progress.queued("go test", state)
	progress.heartbeat(80*time.Second, state)
	progress.admittedAfterWait(92 * time.Second)
	progress.done(4*time.Minute+2*time.Second, 0)

	rendered := out.String()
	for _, want := range []string{
		"wb run: queued go test (position 2 of 3, waiting on: 4242 go build)",
		"wb run: still queued 1m20s (position 2 of 3; running: 4242 go build",
		"wb run: admitted after 1m32s",
		"wb run: done in 4m2s (exit 0)",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("progress output missing %q: %q", want, rendered)
		}
	}
}

func TestRunQueueProgressAdmittedImmediatelyLine(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newRunQueueProgress(&out, true, "")
	progress.admittedImmediately()
	if got := out.String(); !strings.Contains(got, "wb run: admitted (queue empty)") {
		t.Fatalf("output = %q, want the admitted-immediately line", got)
	}
}

func TestRunQueueProgressFallsBackToHostLoadHintWithoutAHolder(t *testing.T) {
	t.Setenv("WB_ADMISSION_LOAD_FLOOR", "4")
	var out bytes.Buffer
	progress := newRunQueueProgress(&out, true, "")
	progress.queued("go test", runqueue.State{Position: 1, Total: 1})
	if got := out.String(); !strings.Contains(got, "waiting on: host load") {
		t.Fatalf("output = %q, want a host-load fallback hint", got)
	}
}

func TestRunQueueProgressCanBeSilenced(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newRunQueueProgress(&out, false, "")
	progress.queued("go test", runqueue.State{Position: 1, Total: 2})
	progress.heartbeat(20*time.Second, runqueue.State{Position: 1, Total: 2})
	progress.admittedAfterWait(20 * time.Second)
	progress.done(time.Minute, 0)
	if out.Len() != 0 {
		t.Fatalf("quiet progress wrote %q", out.String())
	}
}

func TestRunQueueProgressHeartbeatCadenceIsConfigurableForTests(t *testing.T) {
	var out bytes.Buffer
	progress := newRunQueueProgressWithHeartbeat(&out, true, "", 5*time.Millisecond)
	if progress.heartbeatEvery != 5*time.Millisecond {
		t.Fatalf("heartbeatEvery = %s, want 5ms", progress.heartbeatEvery)
	}
	if defaultHeartbeat := newRunQueueProgress(&out, true, "").heartbeatEvery; defaultHeartbeat != universalProgressHeartbeat {
		t.Fatalf("default heartbeatEvery = %s, want the universal 10s heartbeat", defaultHeartbeat)
	}
}
