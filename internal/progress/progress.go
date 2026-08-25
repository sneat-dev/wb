// Package progress defines the transport-neutral event contract used by
// long-running WB operations. Engines never write terminal output directly.
package progress

// State describes the lifecycle of one operation or repository phase.
type State string

const (
	Started   State = "started"
	Running   State = "running"
	Completed State = "completed"
	Failed    State = "failed"
	Waiting   State = "waiting"
)

// Event is one progress observation. Completed and Total refer to repositories
// or releases; Wave and Layer identify ordered campaigns. Layer is a pointer
// because layer zero is meaningful and must remain distinguishable from an
// operation that has no layer.
type Event struct {
	Operation  string
	Phase      string
	Repository string
	Detail     string
	State      State
	Completed  int
	Total      int
	Wave       int
	Layer      *int
}

// Reporter receives progress events. Engines may call a reporter concurrently,
// so implementations must be concurrency-safe and return promptly. A nil
// reporter disables reporting.
type Reporter func(Event)

// Index returns a stable pointer for an optional zero-based event index.
func Index(value int) *int { return &value }

// Report invokes reporter when configured.
func Report(reporter Reporter, event Event) {
	if reporter != nil {
		reporter(event)
	}
}
