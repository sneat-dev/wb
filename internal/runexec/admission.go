package runexec

import (
	"context"
	"github.com/sneat-dev/wb/internal/runqueue"
	"time"
)

const AdmissionGrace = 200 * time.Millisecond
const Heartbeat = 10 * time.Second

type admissionTicket struct {
	Forget    func()
	Snapshot  func(int) runqueue.State
	Heartbeat func()
	Admit     func(context.Context) (runqueue.Admission, error)
}
type admissionResult struct {
	admission runqueue.Admission
	err       error
}
type admissionOperations struct {
	Start    func(context.Context, admissionTicket) <-chan admissionResult
	Register func(string, []string, runqueue.Participant) admissionTicket
	Budget   func() int
	Now      func() time.Time
	After    func(time.Duration) <-chan time.Time
	Ticker   func(time.Duration) (<-chan time.Time, func())
}

func defaultAdmissionOperations() admissionOperations {
	return admissionOperations{
		Start: startAdmission,
		Register: func(root string, argv []string, self runqueue.Participant) admissionTicket {
			ticket := runqueue.RegisterForAdmission(root, argv, self)
			return admissionTicket{Forget: ticket.Forget, Snapshot: ticket.Snapshot, Heartbeat: ticket.Heartbeat, Admit: func(ctx context.Context) (runqueue.Admission, error) {
				return runqueue.Admit(ctx, root, argv, self, ticket)
			}}
		}, Budget: runqueue.Budget, Now: time.Now, After: time.After, Ticker: realTicker,
	}
}
func realTicker(every time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(every)
	return ticker.C, ticker.Stop
}
func (ops admissionOperations) run(ctx context.Context, root string, argv []string, self runqueue.Participant, observe func(QueueEvent), heartbeat time.Duration) (runqueue.Admission, error) {
	ticket := ops.Register(root, argv, self)
	defer ticket.Forget()
	if runqueue.Classify(argv) == runqueue.KindNone {
		return ticket.Admit(ctx)
	}
	resultCh := ops.Start(ctx, ticket)
	immediate := func(r admissionResult) (runqueue.Admission, error) {
		observe(QueueEvent{Kind: ImmediatelyAdmitted})
		return r.admission, r.err
	}
	select {
	case r := <-resultCh:
		return immediate(r)
	case <-ops.After(AdmissionGrace):
	}
	state := ticket.Snapshot(ops.Budget())
	// Checking the exact self-holder first preserves the same immediate receipt
	// whether delivery is already buffered or still in flight, without a timing
	// dependent distinction between two equivalent immediate branches.
	if queueStateHasAdmittedSelf(state, self) {
		return immediate(<-resultCh)
	}
	select {
	case r := <-resultCh:
		return immediate(r)
	default:
	}

	queuedAt := ops.Now()
	observe(QueueEvent{Kind: Queued, Summary: self.Summary, State: state})
	ticks, stop := ops.Ticker(heartbeat)
	defer stop()
	for {
		select {
		case r := <-resultCh:
			if r.err == nil {
				observe(QueueEvent{Kind: AdmittedAfterWait, Waited: ops.Now().Sub(queuedAt)})
			}
			return r.admission, r.err
		case <-ticks:
			ticket.Heartbeat()
			observe(QueueEvent{Kind: WaitingHeartbeat, Waited: ops.Now().Sub(queuedAt), State: ticket.Snapshot(ops.Budget())})
		}
	}
}
func queueStateHasAdmittedSelf(state runqueue.State, self runqueue.Participant) bool {
	if state.Total != 0 {
		return false
	}
	for _, holder := range state.Holders {
		if holder.PID == self.PID && holder.Summary == self.Summary && holder.Worktree == self.Worktree {
			return true
		}
	}
	return false
}

func startAdmission(ctx context.Context, ticket admissionTicket) <-chan admissionResult {
	result := make(chan admissionResult, 1)
	go func() { admission, err := ticket.Admit(ctx); result <- admissionResult{admission, err} }()
	return result
}
