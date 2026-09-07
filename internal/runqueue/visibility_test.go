package runqueue

import (
	"context"
	"testing"
	"time"
)

func TestRegisterReportsPositionAndTotalAmongWaiters(t *testing.T) {
	root := t.TempDir()
	first := Register(root, Participant{PID: 111, Summary: "go test", Worktree: "/w/a"})
	defer first.Forget()
	second := Register(root, Participant{PID: 222, Summary: "go vet", Worktree: "/w/b"})
	defer second.Forget()

	if state := first.Snapshot(1); state.Position != 1 || state.Total != 2 {
		t.Fatalf("first ticket state = %+v, want position 1 of 2", state)
	}
	if state := second.Snapshot(1); state.Position != 2 || state.Total != 2 {
		t.Fatalf("second ticket state = %+v, want position 2 of 2", state)
	}

	first.Forget()
	if state := second.Snapshot(1); state.Position != 1 || state.Total != 1 {
		t.Fatalf("second ticket state after first forgot = %+v, want position 1 of 1", state)
	}
}

func TestSnapshotReportsAnnouncedHolders(t *testing.T) {
	root := t.TempDir()
	lease, _, err := Acquire(context.Background(), root, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	cleanup := lease.Announce(Participant{PID: 4242, Summary: "go build", Worktree: "/w/holder"})
	defer cleanup()

	waiter := Register(root, Participant{PID: 555, Summary: "go test"})
	defer waiter.Forget()

	state := waiter.Snapshot(2)
	if len(state.Holders) != 1 {
		t.Fatalf("state.Holders = %+v, want exactly one announced holder", state.Holders)
	}
	holder := state.Holders[0]
	if holder.PID != 4242 || holder.Summary != "go build" || holder.Worktree != "/w/holder" {
		t.Fatalf("holder = %+v, want the announced participant", holder)
	}
	if holder.StartedAt.IsZero() {
		t.Fatal("holder.StartedAt is zero")
	}
}

func TestAnnounceCleanupRemovesHolderRecords(t *testing.T) {
	root := t.TempDir()
	lease, _, err := Acquire(context.Background(), root, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := lease.Announce(Participant{PID: 1, Summary: "go test"})
	if state := Peek(root, 1); len(state.Holders) != 1 {
		t.Fatalf("Peek before cleanup = %+v, want one holder", state)
	}
	cleanup()
	lease.Release()
	if state := Peek(root, 1); len(state.Holders) != 0 {
		t.Fatalf("Peek after cleanup+release = %+v, want no holders", state)
	}
}

func TestPeekDoesNotRegisterAWaiter(t *testing.T) {
	root := t.TempDir()
	if state := Peek(root, 2); state.Total != 0 || state.Position != 0 {
		t.Fatalf("Peek on empty queue = %+v, want zero state", state)
	}
	ticket := Register(root, Participant{PID: 1, Summary: "go test"})
	defer ticket.Forget()
	if state := Peek(root, 2); state.Total != 1 {
		t.Fatalf("Peek total = %d, want 1 (Peek itself must not register)", state.Total)
	}
	if state := Peek(root, 2); state.Position != 0 {
		t.Fatalf("Peek position = %d, want 0 (Peek has no ticket of its own)", state.Position)
	}
}

func TestListQueueReportsRunningAndWaitingEntries(t *testing.T) {
	root := t.TempDir()
	lease, _, err := Acquire(context.Background(), root, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	cleanup := lease.Announce(Participant{PID: 10, Summary: "go build", Worktree: "/w/one"})
	defer cleanup()
	waiter := Register(root, Participant{PID: 20, Summary: "go test", Worktree: "/w/two"})
	defer waiter.Forget()
	time.Sleep(2 * time.Millisecond)

	listing := ListQueue(root, 2)
	if listing.Budget != 2 {
		t.Fatalf("listing.Budget = %d, want 2", listing.Budget)
	}
	if len(listing.Running) != 1 || listing.Running[0].PID != 10 || listing.Running[0].Summary != "go build" || listing.Running[0].Worktree != "/w/one" {
		t.Fatalf("listing.Running = %+v", listing.Running)
	}
	if listing.Running[0].Age <= 0 {
		t.Fatalf("listing.Running[0].Age = %s, want positive", listing.Running[0].Age)
	}
	if len(listing.Waiting) != 1 || listing.Waiting[0].PID != 20 || listing.Waiting[0].Summary != "go test" || listing.Waiting[0].Worktree != "/w/two" {
		t.Fatalf("listing.Waiting = %+v", listing.Waiting)
	}
}

func TestForgetIsSafeToCallTwiceAndOnUnregisteredTicket(t *testing.T) {
	root := t.TempDir()
	ticket := Register(root, Participant{PID: 1, Summary: "go test"})
	ticket.Forget()
	ticket.Forget()

	var nilTicket *Ticket
	nilTicket.Forget()
	if state := nilTicket.Snapshot(1); state.Position != 0 || state.Total != 0 {
		t.Fatalf("nil ticket snapshot = %+v, want zero value", state)
	}
}
