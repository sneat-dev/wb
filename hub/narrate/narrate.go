// Package narrate renders the one console line the hub writes for every event
// it handles, whether the event arrived by webhook or was produced by the
// poller.
//
// The operator who runs `wb daemon serve` in a terminal wants to see the hub
// work without opening the dashboard, so the format is fixed-column and
// readable at a glance rather than structured for machines:
//
//	14:02:11 push            github.com/sneat-dev/wb          default branch main -> 3f1c2a9; queued for laptop
//	14:06:30 poll            github.com/sneat-dev/wb          no change
//
// Columns are local time, the event name as GitHub names it ("poll" for the
// poller), the organisation or repository the event is about, and the action
// taken in plain words.
//
// A Line carries only metadata the repository-event contract already treats as
// publishable. Callers must never put a token, a webhook secret, or a payload
// body into one: the daemon's console is echoed into the log file a detached
// daemon writes, and that file is routinely pasted into issues.
package narrate

import (
	"fmt"
	"io"
	"time"
)

// Column widths hold the four fields in step across consecutive lines. They
// are wide enough for "installation" and for a `github.com/owner/repository`
// of ordinary length; a longer value pushes the action right rather than
// being truncated, because a truncated repository name is worse than a ragged
// column.
const (
	eventWidth   = 15
	subjectWidth = 32
	timeLayout   = "15:04:05"
)

// Line is one narrated event.
type Line struct {
	// At is the moment the hub decided what to do with the event. It is
	// rendered in the location it carries, so a caller that wants local time
	// passes local time.
	At time.Time
	// Event is the event name as GitHub names it: "push", "repository",
	// "installation", or "poll" for the polling ingester.
	Event string
	// Subject is the organisation or repository the event is about.
	Subject string
	// Action is what the hub did, in plain words.
	Action string
}

// String renders the line without a trailing newline.
func (line Line) String() string {
	return fmt.Sprintf("%s %-*s %-*s %s", line.At.Format(timeLayout), eventWidth, line.Event, subjectWidth, line.Subject, line.Action)
}

// Writer sends narrated lines to the daemon's console. The zero value
// discards them, so a caller that has nowhere to write needs no branch.
//
// Quiet is what `wb daemon serve --quiet` sets. It silences the console only:
// a detached daemon started by `wb daemon start` never passes --quiet, so the
// log file it writes keeps every line.
type Writer struct {
	Out   io.Writer
	Quiet bool
}

// Write renders one line. It is deliberately best-effort: a console that
// cannot be written to must never take the hub down, and there is nowhere
// useful to report the failure to.
func (writer Writer) Write(line Line) {
	if writer.Quiet || writer.Out == nil {
		return
	}
	_, _ = fmt.Fprintln(writer.Out, line.String())
}
