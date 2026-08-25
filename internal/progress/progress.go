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
// or releases; Wave and Layer identify ordered campaigns.
type Event struct {
	Operation  string
	Phase      string
	Repository string
	Detail     string
	State      State
	Completed  int
	Total      int
	Wave       int
	Layer      int
}

// Reporter receives progress events. A nil reporter disables reporting.
type Reporter func(Event)

// Report invokes reporter when configured.
func Report(reporter Reporter, event Event) {
	if reporter != nil {
		reporter(event)
	}
}
