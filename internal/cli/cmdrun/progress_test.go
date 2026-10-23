package cmdrun

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runexec"
	"github.com/sneat-dev/wb/internal/runqueue"
)

func TestRunQueueProgressFormatsQueuedHeartbeatAdmittedAndDoneLines(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newQueueProgress(&out, true, "", func(string) string { return "host load 5.0>4.0" })

	state := runqueue.State{Position: 2, Total: 3, Holders: []runqueue.Holder{
		{Participant: runqueue.Participant{PID: 4242, Summary: "go build"}, StartedAt: time.Now().Add(-3 * time.Minute)},
	}}
	progress.report(runexec.QueueEvent{Kind: runexec.Queued, Summary: "go test", State: state})
	progress.report(runexec.QueueEvent{Kind: runexec.WaitingHeartbeat, Waited: 80 * time.Second, State: state})
	progress.report(runexec.QueueEvent{Kind: runexec.AdmittedAfterWait, Waited: 92 * time.Second})
	progress.report(runexec.QueueEvent{Kind: runexec.Done, Elapsed: 4*time.Minute + 2*time.Second, ExitCode: 0})

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
	progress := newQueueProgress(&out, true, "", func(string) string { return "host load 5.0>4.0" })
	progress.report(runexec.QueueEvent{Kind: runexec.ImmediatelyAdmitted})
	if got := out.String(); !strings.Contains(got, "wb run: admitted (queue empty)") {
		t.Fatalf("output = %q, want the admitted-immediately line", got)
	}
}

func TestRunQueueProgressFallsBackToHostLoadHintWithoutAHolder(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newQueueProgress(&out, true, "", func(string) string { return "host load 5.0>4.0" })
	progress.report(runexec.QueueEvent{Kind: runexec.Queued, Summary: "go test", State: runqueue.State{Position: 1, Total: 1}})
	if got := out.String(); !strings.Contains(got, "waiting on: host load") {
		t.Fatalf("output = %q, want a host-load fallback hint", got)
	}
}

func TestRunQueueProgressCanBeSilenced(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	progress := newQueueProgress(&out, false, "", func(string) string { t.Fatal("quiet output must not resolve load"); return "" })
	progress.report(runexec.QueueEvent{Kind: runexec.Queued, Summary: "go test", State: runqueue.State{Position: 1, Total: 2}})
	progress.report(runexec.QueueEvent{Kind: runexec.WaitingHeartbeat, Waited: 20 * time.Second, State: runqueue.State{Position: 1, Total: 2}})
	progress.report(runexec.QueueEvent{Kind: runexec.AdmittedAfterWait, Waited: 20 * time.Second})
	progress.report(runexec.QueueEvent{Kind: runexec.Done, Elapsed: time.Minute, ExitCode: 0})
	if out.Len() != 0 {
		t.Fatalf("quiet progress wrote %q", out.String())
	}
}
func TestProgressResolvesFallbackAtEachWaitingEventAndCourtesyIgnoresQueueQuiet(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	calls := 0
	p := newQueueProgress(&out, true, "config", func(config string) string {
		if config != "config" {
			t.Fatal(config)
		}
		calls++
		return "capacity contention"
	})
	p.report(runexec.QueueEvent{Kind: runexec.WaitingHeartbeat, Waited: time.Second, State: runqueue.State{Position: 1, Total: 1}})
	p.report(runexec.QueueEvent{Kind: runexec.Queued, Summary: "go test"})
	if calls != 2 || !strings.Contains(out.String(), "running: capacity contention") {
		t.Fatalf("output=%q calls%d", out.String(), calls)
	}
	quiet := newQueueProgress(&out, false, "config", func(string) string { t.Fatal("quiet host lookup"); return "" })
	quiet.report(runexec.QueueEvent{Kind: runexec.CourtesyRunning, Summary: "child arg"})
	if !strings.HasSuffix(out.String(), "wb: command still running: child arg\n") {
		t.Fatal(out.String())
	}
}
