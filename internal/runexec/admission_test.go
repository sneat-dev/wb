package runexec

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/runqueue"
	"testing"
	"time"
)

func TestControlledAdmissionPreservesFastHandoffAndWaitingPaths(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"immediate", "snapshot-result", "self-handoff", "waited", "wait-error"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			ctx := context.WithValue(context.Background(), testContextKey{}, true)
			self := runqueue.Participant{PID: 7, Summary: "go test", Worktree: "/repo"}
			results := make(chan admissionResult, 1)
			ticks := make(chan time.Time, 1)
			grace := make(chan time.Time)
			close(grace)
			never := make(chan time.Time)
			forgotten, heartbeats, stops, snapshots := 0, 0, 0, 0
			sentinel := errors.New("admission cancelled")
			admission := runqueue.Admission{Units: 3, Waited: time.Second}
			clock := time.Unix(1, 0)
			var events []QueueEvent
			ops := admissionOperations{Register: func(root string, args []string, got runqueue.Participant) admissionTicket {
				if root != "/root" || len(args) != 3 || got != self {
					t.Fatal("registration mismatch")
				}
				return admissionTicket{Forget: func() { forgotten++ }, Heartbeat: func() { heartbeats++ }, Snapshot: func(budget int) runqueue.State {
					snapshots++
					if budget != 4 {
						t.Fatal(budget)
					}
					if path == "snapshot-result" {
						results <- admissionResult{admission: admission}
					}
					if path == "self-handoff" {
						results <- admissionResult{admission: admission}
						return runqueue.State{Holders: []runqueue.Holder{{Participant: self}}}
					}
					return runqueue.State{Position: 1, Total: 1}
				}}
			}, Start: func(got context.Context, _ admissionTicket) <-chan admissionResult {
				if got != ctx {
					t.Fatal("context changed")
				}
				if path == "immediate" {
					results <- admissionResult{admission: admission}
				}
				return results
			}, Budget: func() int { return 4 }, Now: func() time.Time { clock = clock.Add(time.Second); return clock }, After: func(duration time.Duration) <-chan time.Time {
				if duration != AdmissionGrace {
					t.Fatal(duration)
				}
				if path == "immediate" {
					return never
				}
				return grace
			}, Ticker: func(duration time.Duration) (<-chan time.Time, func()) {
				if duration != 5*time.Millisecond {
					t.Fatal(duration)
				}
				return ticks, func() { stops++ }
			}}
			got, err := ops.run(ctx, "/root", []string{"go", "test", "./..."}, self, func(e QueueEvent) {
				events = append(events, e)
				if e.Kind == Queued {
					ticks <- clock
				}
				if e.Kind == WaitingHeartbeat {
					result := admissionResult{admission: admission}
					if path == "wait-error" {
						result.err = sentinel
					}
					results <- result
				}
			}, 5*time.Millisecond)
			if got.Units != 3 || forgotten != 1 {
				t.Fatalf("admission=%+v forgotten=%d", got, forgotten)
			}
			if path == "waited" || path == "wait-error" {
				if heartbeats != 1 || stops != 1 || snapshots != 2 || len(events) < 2 || events[0].Kind != Queued || events[1].Kind != WaitingHeartbeat || events[1].Waited != time.Second {
					t.Fatalf("events=%+v heartbeats%d stops%d snapshots%d", events, heartbeats, stops, snapshots)
				}
				if path == "wait-error" {
					if err != sentinel || len(events) != 2 {
						t.Fatalf("error=%v events=%+v", err, events)
					}
				} else {
					if err != nil || len(events) != 3 || events[2].Kind != AdmittedAfterWait || events[2].Waited != 2*time.Second {
						t.Fatalf("events=%+v err=%v", events, err)
					}
				}
			} else {
				if err != nil || len(events) != 1 || events[0].Kind != ImmediatelyAdmitted || heartbeats != 0 || stops != 0 {
					t.Fatalf("events=%+v err=%v", events, err)
				}
			}
		})
	}
}
