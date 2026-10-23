package cmdrun

import (
	"fmt"
	cliprogress "github.com/sneat-dev/wb/internal/cli/progress"
	"github.com/sneat-dev/wb/internal/runexec"
	"github.com/sneat-dev/wb/internal/runqueue"
	"io"
	"time"
)

type queueProgress struct {
	live       *cliprogress.Live
	out        io.Writer
	enabled    bool
	configPath string
	loadHint   func(string) string
	now        func() time.Time
}

func newQueueProgress(out io.Writer, enabled bool, config string, hint func(string) string) *queueProgress {
	return &queueProgress{live: cliprogress.NewLive(out, enabled), out: out, enabled: enabled, configPath: config, loadHint: hint, now: time.Now}
}
func (p *queueProgress) report(event runexec.QueueEvent) {
	// Interactive courtesy output predates --quiet and remains independent of
	// CPU queue visibility. The service joins its writer before returning.
	if event.Kind == runexec.CourtesyRunning {
		_, _ = fmt.Fprintf(p.out, "wb: command still running: %s\n", event.Summary)
		return
	}
	if !p.enabled {
		return
	}
	var text string
	switch event.Kind {
	case runexec.ImmediatelyAdmitted:
		text = "wb run: admitted (queue empty)"
	case runexec.Queued:
		text = fmt.Sprintf("wb run: queued %s (position %d of %d, waiting on: %s)", event.Summary, event.State.Position, event.State.Total, p.waitingOn(event.State))
	case runexec.WaitingHeartbeat:
		text = fmt.Sprintf("wb run: still queued %s (position %d of %d; running: %s)", event.Waited.Round(time.Second), event.State.Position, event.State.Total, p.runningOn(event.State))
	case runexec.AdmittedAfterWait:
		text = fmt.Sprintf("wb run: admitted after %s", event.Waited.Round(time.Second))
	case runexec.Done:
		text = fmt.Sprintf("wb run: done in %s (exit %d)", event.Elapsed.Round(time.Second), event.ExitCode)
	}
	p.live.PrintLine(text)
}
func (p *queueProgress) waitingOn(state runqueue.State) string {
	if len(state.Holders) > 0 {
		holder := state.Holders[0]
		return fmt.Sprintf("%d %s", holder.PID, holder.Summary)
	}
	return p.loadHint(p.configPath)
}
func (p *queueProgress) runningOn(state runqueue.State) string {
	if len(state.Holders) > 0 {
		holder := state.Holders[0]
		return fmt.Sprintf("%d %s %s", holder.PID, holder.Summary, p.now().Sub(holder.StartedAt).Round(time.Second))
	}
	return p.loadHint(p.configPath)
}
