package runqueue

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// deadPID is a PID that must not resolve to a live process on any machine
// running this test suite, matching the convention internal/session's own
// tests already use for the same purpose (see session_test.go).
const deadPID = 424242

func TestRegisterReportsPositionAndTotalAmongWaiters(t *testing.T) {
	root := t.TempDir()
	self := os.Getpid()
	first := Register(root, Participant{PID: self, Summary: "go test", Worktree: "/w/a"})
	defer first.Forget()
	second := Register(root, Participant{PID: self, Summary: "go vet", Worktree: "/w/b"})
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
	announcement := lease.Announce(Participant{PID: os.Getpid(), Summary: "go build", Worktree: "/w/holder"})
	defer announcement.Cleanup()

	waiter := Register(root, Participant{PID: os.Getpid(), Summary: "go test"})
	defer waiter.Forget()

	state := waiter.Snapshot(2)
	if len(state.Holders) != 1 {
		t.Fatalf("state.Holders = %+v, want exactly one announced holder", state.Holders)
	}
	holder := state.Holders[0]
	if holder.PID != os.Getpid() || holder.Summary != "go build" || holder.Worktree != "/w/holder" {
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
	announcement := lease.Announce(Participant{PID: os.Getpid(), Summary: "go test"})
	if state := Peek(root, 1); len(state.Holders) != 1 {
		t.Fatalf("Peek before cleanup = %+v, want one holder", state)
	}
	announcement.Cleanup()
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
	ticket := Register(root, Participant{PID: os.Getpid(), Summary: "go test"})
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
	announcement := lease.Announce(Participant{PID: os.Getpid(), Summary: "go build", Worktree: "/w/one"})
	defer announcement.Cleanup()
	waiter := Register(root, Participant{PID: os.Getpid(), Summary: "go test", Worktree: "/w/two"})
	defer waiter.Forget()
	time.Sleep(2 * time.Millisecond)

	listing := ListQueue(root, 2)
	if listing.Budget != 2 {
		t.Fatalf("listing.Budget = %d, want 2", listing.Budget)
	}
	if len(listing.Running) != 1 || listing.Running[0].PID != os.Getpid() || listing.Running[0].Summary != "go build" || listing.Running[0].Worktree != "/w/one" {
		t.Fatalf("listing.Running = %+v", listing.Running)
	}
	if listing.Running[0].Age <= 0 {
		t.Fatalf("listing.Running[0].Age = %s, want positive", listing.Running[0].Age)
	}
	if len(listing.Waiting) != 1 || listing.Waiting[0].PID != os.Getpid() || listing.Waiting[0].Summary != "go test" || listing.Waiting[0].Worktree != "/w/two" {
		t.Fatalf("listing.Waiting = %+v", listing.Waiting)
	}
}

// TestListQueueOnEmptyQueueHasEmptySlicesNotNil pins the JSON shape of an
// empty queue: `--format json` must emit "running":[] / "waiting":[], never
// "running":null, so a consumer can always range over the fields without a
// nil check.
func TestListQueueOnEmptyQueueHasEmptySlicesNotNil(t *testing.T) {
	root := t.TempDir()
	listing := ListQueue(root, 2)
	if listing.Running == nil {
		t.Fatal("listing.Running is nil, want an empty slice")
	}
	if listing.Waiting == nil {
		t.Fatal("listing.Waiting is nil, want an empty slice")
	}
	payload, err := json.Marshal(listing)
	if err != nil {
		t.Fatal(err)
	}
	rendered := string(payload)
	if want := `"running":[]`; !strings.Contains(rendered, want) {
		t.Fatalf("json = %s, want %q", rendered, want)
	}
	if want := `"waiting":[]`; !strings.Contains(rendered, want) {
		t.Fatalf("json = %s, want %q", rendered, want)
	}
}

func TestForgetIsSafeToCallTwiceAndOnUnregisteredTicket(t *testing.T) {
	root := t.TempDir()
	ticket := Register(root, Participant{PID: os.Getpid(), Summary: "go test"})
	ticket.Forget()
	ticket.Forget()

	var nilTicket *Ticket
	nilTicket.Forget()
	if state := nilTicket.Snapshot(1); state.Position != 0 || state.Total != 0 {
		t.Fatalf("nil ticket snapshot = %+v, want zero value", state)
	}
}

// --- Staleness / reaping (review finding: a killed process must not
// permanently corrupt queue visibility) ---

// TestReadTicketsReapsAndIgnoresADeadPIDsTicket covers a SIGKILLed waiter:
// nothing else ever claims its uniquely named ticket file, so without a
// liveness check it would report a phantom waiter forever. Snapshot must
// both exclude it from Total/Position and best-effort remove the file.
func TestReadTicketsReapsAndIgnoresADeadPIDsTicket(t *testing.T) {
	root := t.TempDir()
	dead := Register(root, Participant{PID: deadPID, Summary: "go test"})
	if dead.path == "" {
		t.Fatal("Register did not create a ticket file")
	}
	deadPath := dead.path

	if state := Peek(root, 1); state.Total != 0 {
		t.Fatalf("Peek = %+v, want the dead PID's ticket excluded from Total", state)
	}
	if _, err := os.Stat(deadPath); !os.IsNotExist(err) {
		t.Fatalf("dead ticket file still exists after Peek reaped it: err=%v", err)
	}
}

// TestReadHoldersIgnoresADeadPIDsHolder covers a killed holder: its slot file
// otherwise self-heals only when that slot is reused. readHolders must
// exclude it (and best-effort remove it) instead of reporting a phantom
// holder.
func TestReadHoldersIgnoresADeadPIDsHolder(t *testing.T) {
	root := t.TempDir()
	lease, _, err := Acquire(context.Background(), root, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	announcement := lease.Announce(Participant{PID: deadPID, Summary: "go build"})
	defer announcement.Cleanup()
	if len(announcement.paths) != 1 {
		t.Fatal("Announce did not write a holder file")
	}
	holderPath := announcement.paths[0]

	if state := Peek(root, 1); len(state.Holders) != 0 {
		t.Fatalf("Peek = %+v, want the dead PID's holder excluded", state)
	}
	if _, err := os.Stat(holderPath); !os.IsNotExist(err) {
		t.Fatalf("dead holder file still exists after Peek reaped it: err=%v", err)
	}
}

// TestLiveFreshWaiterIsNotReaped is the negative case for the two tests
// above: a real, live PID with a just-written (fresh) record must keep
// counting even though nothing has heartbeat it again yet ("idle" between
// heartbeats is normal, not stale).
func TestLiveFreshWaiterIsNotReaped(t *testing.T) {
	root := t.TempDir()
	ticket := Register(root, Participant{PID: os.Getpid(), Summary: "go test"})
	defer ticket.Forget()

	if state := Peek(root, 1); state.Total != 1 {
		t.Fatalf("Peek = %+v, want the live, fresh ticket counted", state)
	}
}

// TestHeartbeatKeepsALiveWaiterFromAging shows Ticket.Heartbeat doing the job
// it exists for: without it, a record older than staleAfter is reaped even
// though its PID is alive; with it, the record stays live.
func TestHeartbeatKeepsALiveWaiterFromAging(t *testing.T) {
	root := t.TempDir()
	previous := staleAfter
	staleAfter = 20 * time.Millisecond
	defer func() { staleAfter = previous }()

	ticket := Register(root, Participant{PID: os.Getpid(), Summary: "go test"})
	defer ticket.Forget()

	time.Sleep(30 * time.Millisecond)
	if state := Peek(root, 1); state.Total != 0 {
		t.Fatalf("Peek = %+v, want the un-refreshed ticket reaped once stale", state)
	}

	// Re-register (the previous ticket's file was reaped) and prove a
	// refreshed record survives the same TTL.
	ticket2 := Register(root, Participant{PID: os.Getpid(), Summary: "go test"})
	defer ticket2.Forget()
	time.Sleep(15 * time.Millisecond)
	ticket2.Heartbeat()
	time.Sleep(15 * time.Millisecond)
	if state := Peek(root, 1); state.Total != 1 {
		t.Fatalf("Peek = %+v, want the heartbeat-refreshed ticket to survive", state)
	}
}

// TestAnnouncementHeartbeatKeepsALiveHolderFromAging is the holder-side
// counterpart: Announce writes an initial UpdatedAt, and Heartbeat refreshes
// it on the same cadence `wb run` already uses while a command runs.
func TestAnnouncementHeartbeatKeepsALiveHolderFromAging(t *testing.T) {
	root := t.TempDir()
	previous := staleAfter
	staleAfter = 20 * time.Millisecond
	defer func() { staleAfter = previous }()

	lease, _, err := Acquire(context.Background(), root, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	announcement := lease.Announce(Participant{PID: os.Getpid(), Summary: "go build"})
	defer announcement.Cleanup()

	time.Sleep(15 * time.Millisecond)
	announcement.Heartbeat()
	time.Sleep(15 * time.Millisecond)
	if state := Peek(root, 1); len(state.Holders) != 1 {
		t.Fatalf("Peek = %+v, want the heartbeat-refreshed holder to survive", state)
	}
}

// TestConcurrentReadersDoNotErrorWhileStaleEntriesAreReaped exercises the
// race the review asked for directly: many goroutines calling Peek at once
// while a dead-PID ticket is present must never panic or otherwise fail —
// os.Remove losing the race to another reaper is expected and silent.
func TestConcurrentReadersDoNotErrorWhileStaleEntriesAreReaped(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		Register(root, Participant{PID: deadPID, Summary: "go test"})
	}
	live := Register(root, Participant{PID: os.Getpid(), Summary: "go test"})
	defer live.Forget()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_ = Peek(root, 1)
			}
		}()
	}
	wg.Wait()

	entries, err := os.ReadDir(ticketDir(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("ticket dir has %d entries after concurrent reaping, want the 1 live ticket left", len(entries))
	}
}
