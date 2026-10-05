package runexec

import (
	"context"
	"github.com/sneat-dev/wb/internal/runqueue"
	"os"
	"testing"
	"time"
)

const holderHoldDuration = 350 * time.Millisecond

type eventRecorder struct{ events []QueueEvent }

func (r *eventRecorder) report(event QueueEvent) { r.events = append(r.events, event) }
func nativeAdmission(ctx context.Context, root string, args []string, self runqueue.Participant, observe func(QueueEvent)) (*runqueue.Lease, int, time.Duration, error) {
	result, err := defaultAdmissionOperations().run(ctx, root, args, self, observe, 5*time.Millisecond)
	return result.Lease, result.Units, result.Waited, err
}

//nolint:paralleltest // This contract changes the process-wide runqueue CPU-count override; admission and restore must remain serial.
func TestAcquireWithQueueVisibilityEmitsQueuedHeartbeatsThenAdmitted(t *testing.T) {
	// Force the small-machine (N<8) legacy budget-sum pool so this test's
	// manually-held lease deterministically blocks the admission under
	// test, regardless of how many CPUs the machine running it actually
	// has (a large machine would otherwise route "go build ./..." through
	// the separate adaptive heavy-job pool this hold never touches).
	defer runqueue.SetNumCPUForTest(4)()
	root := t.TempDir()
	budget := runqueue.Budget()
	held, _, err := runqueue.Acquire(context.Background(), root, budget, budget)
	if err != nil {
		t.Fatal(err)
	}
	// Registered PIDs are now liveness-checked (a killed process's ticket or
	// holder record must not linger forever), so both self and the holder
	// must carry a real, live PID; this test process's own PID qualifies for
	// the whole test.
	holderPID := os.Getpid()
	holderAnnouncement := held.Announce(runqueue.Participant{PID: holderPID, Summary: "go build"})
	released := make(chan struct{})
	readyToRelease := make(chan struct{})
	go func() {
		// Keep the native budget occupied until the observer has actually
		// reported two waiting heartbeats; scheduler delays cannot shorten
		// the contract to a lucky fixed-duration sleep.
		<-readyToRelease
		holderAnnouncement.Cleanup()
		held.Release()
		close(released)
	}()
	t.Cleanup(func() {
		select {
		case <-readyToRelease:
		default:
			close(readyToRelease)
		}
		<-released
	})

	var out eventRecorder
	observedHeartbeats := 0
	progress := func(event QueueEvent) {
		out.report(event)
		if event.Kind == WaitingHeartbeat {
			observedHeartbeats++
			if observedHeartbeats == 2 {
				close(readyToRelease)
			}
		}
	}
	self := runqueue.Participant{PID: os.Getpid(), Summary: "go test", Worktree: "/w/waiter"}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	lease, _, waited, err := nativeAdmission(ctx, root, []string{"go", "test", "./..."}, self, progress)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if waited < AdmissionGrace {
		t.Fatalf("waited = %s, want at least the admission grace period", waited)
	}

	if len(out.events) < 4 || out.events[0].Kind != Queued {
		t.Fatalf("queued events=%+v", out.events)
	}
	queued := out.events[0]
	if queued.Summary != "go test" || queued.State.Position != 1 || queued.State.Total != 1 || len(queued.State.Holders) < 1 || queued.State.Holders[0].PID != holderPID || queued.State.Holders[0].Summary != "go build" {
		t.Fatalf("queued state=%+v", queued)
	}
	heartbeats := 0
	for _, event := range out.events {
		if event.Kind == WaitingHeartbeat {
			heartbeats++
		}
		if event.Kind == ImmediatelyAdmitted {
			t.Fatal("a waiting command must not claim an empty queue")
		}
	}
	if heartbeats < 2 {
		t.Fatalf("heartbeats=%d want>=2", heartbeats)
	}
	if out.events[len(out.events)-1].Kind != AdmittedAfterWait {
		t.Fatalf("missing final admitted event: %+v", out.events)
	}
}

func TestAcquireWithQueueVisibilityAdmitsImmediatelyOnAnEmptyQueue(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var out eventRecorder
	progress := out.report
	self := runqueue.Participant{PID: os.Getpid(), Summary: "go test"}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	lease, units, _, err := nativeAdmission(ctx, root, []string{"go", "test", "./..."}, self, progress)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lease.Release)
	// Admission is a queue protocol outcome, not a wall-clock latency SLA.
	// Race/coverage instrumentation and filesystem scheduling can exceed the
	// grace period without any competing holder or queue wait.
	if lease == nil || units <= 0 {
		t.Fatalf("native admission returned no governed lease: lease=%v units=%d", lease, units)
	}
	if len(out.events) != 1 || out.events[0].Kind != ImmediatelyAdmitted {
		t.Fatalf("immediate events=%+v", out.events)
	}
}

//nolint:paralleltest // This contract changes the process-wide runqueue CPU-count override; admission and restore must remain serial.
func TestAdmitWithQueueVisibilityOnALargeMachineUsesTheAdaptiveHeavyPool(t *testing.T) {
	defer runqueue.SetNumCPUForTest(18)()
	root := t.TempDir()
	broadArgv := []string{"go", "test", "./..."}

	var firstOut eventRecorder
	firstProgress := firstOut.report
	firstSelf := runqueue.Participant{PID: os.Getpid(), Summary: "first"}
	firstLease, firstUnits, _, err := nativeAdmission(context.Background(), root, broadArgv, firstSelf, firstProgress)
	if err != nil {
		t.Fatalf("first admitWithQueueVisibility = %v", err)
	}
	defer firstLease.Release()
	if firstUnits != 18 {
		t.Fatalf("first alone = %d units, want 18 (the whole machine)", firstUnits)
	}
	if len(firstOut.events) != 1 || firstOut.events[0].Kind != ImmediatelyAdmitted {
		t.Fatalf("first events=%+v: must not queue against own holder", firstOut.events)
	}

	var secondOut eventRecorder
	secondProgress := secondOut.report
	secondSelf := runqueue.Participant{PID: os.Getpid(), Summary: "second"}
	secondLease, secondUnits, _, err := nativeAdmission(context.Background(), root, broadArgv, secondSelf, secondProgress)
	if err != nil {
		t.Fatalf("second admitWithQueueVisibility = %v", err)
	}
	defer secondLease.Release()
	if secondUnits != 9 {
		t.Fatalf("second while the first holds 18 = %d units, want min(12, 27-18) = 9", secondUnits)
	}
}

func TestQueueStateHasAdmittedSelfRequiresTheExactSelfHolderAfterTicketRemoval(t *testing.T) {
	t.Parallel()
	self := runqueue.Participant{PID: 7, Summary: "go test", Worktree: "/worktree"}
	if !queueStateHasAdmittedSelf(runqueue.State{Holders: []runqueue.Holder{{Participant: self}}}, self) {
		t.Fatal("own holder after ticket removal must prove admission")
	}
	for name, state := range map[string]runqueue.State{
		"still waiting":  {Total: 1, Holders: []runqueue.Holder{{Participant: self}}},
		"other pid":      {Holders: []runqueue.Holder{{Participant: runqueue.Participant{PID: 8, Summary: self.Summary, Worktree: self.Worktree}}}},
		"other summary":  {Holders: []runqueue.Holder{{Participant: runqueue.Participant{PID: self.PID, Summary: "go build", Worktree: self.Worktree}}}},
		"other worktree": {Holders: []runqueue.Holder{{Participant: runqueue.Participant{PID: self.PID, Summary: self.Summary, Worktree: "/other"}}}},
		"no visibility":  {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if queueStateHasAdmittedSelf(state, self) {
				t.Fatalf("queueStateHasAdmittedSelf(%+v) = true", state)
			}
		})
	}
}

func TestAcquireWithQueueVisibilitySkipsEverythingForKindNone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var out eventRecorder
	progress := out.report
	lease, units, waited, err := nativeAdmission(context.Background(), root, []string{"git", "status"}, runqueue.Participant{PID: 1, Summary: "git status"}, progress)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lease.Release)
	if units != 0 {
		t.Fatalf("units = %d, want 0 for an ungoverned command", units)
	}
	if waited != 0 {
		t.Fatalf("waited = %s, want 0 for an ungoverned command", waited)
	}
	if len(out.events) != 0 {
		t.Fatalf("an ungoverned command must not publish any queue event: %+v", out.events)
	}
	if state := runqueue.Peek(root, 1); state.Total != 0 {
		t.Fatalf("an ungoverned command must not register a waiting ticket: %+v", state)
	}
}
