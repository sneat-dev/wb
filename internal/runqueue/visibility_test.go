package runqueue

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

	staleUpdatedAt := time.Now().UTC().Add(-2 * staleAfter)
	staleTicket := ticketRecord{
		Participant: ticket.self,
		CreatedAt:   ticket.createdAt,
		UpdatedAt:   staleUpdatedAt,
	}
	payload, err := json.Marshal(staleTicket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ticket.path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if state := Peek(root, 1); state.Total != 0 {
		t.Fatalf("Peek = %+v, want the un-refreshed ticket reaped once stale", state)
	}

	// Re-register after the stale record was reaped, age the replacement
	// deterministically, then prove Heartbeat refreshes it without relying on
	// scheduler-sensitive sleeps around a millisecond TTL.
	ticket2 := Register(root, Participant{PID: os.Getpid(), Summary: "go test"})
	defer ticket2.Forget()
	staleTicket.Participant = ticket2.self
	staleTicket.CreatedAt = ticket2.createdAt
	payload, err = json.Marshal(staleTicket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ticket2.path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	ticket2.Heartbeat()
	payload, err = os.ReadFile(ticket2.path)
	if err != nil {
		t.Fatal(err)
	}
	var refreshedTicket ticketRecord
	if err := json.Unmarshal(payload, &refreshedTicket); err != nil {
		t.Fatal(err)
	}
	if !refreshedTicket.UpdatedAt.After(staleUpdatedAt) {
		t.Fatalf("Heartbeat UpdatedAt = %v, want after stale value %v", refreshedTicket.UpdatedAt, staleUpdatedAt)
	}
	staleAfter = time.Minute
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

	if len(announcement.paths) != 1 {
		t.Fatalf("Announce wrote %d holder files, want 1", len(announcement.paths))
	}
	staleUpdatedAt := time.Now().UTC().Add(-2 * staleAfter)
	staleHolder := Holder{
		Participant: announcement.self,
		StartedAt:   announcement.started,
		UpdatedAt:   staleUpdatedAt,
	}
	payload, err := json.Marshal(staleHolder)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(announcement.paths[0], payload, 0o600); err != nil {
		t.Fatal(err)
	}
	announcement.Heartbeat()
	payload, err = os.ReadFile(announcement.paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var refreshedHolder Holder
	if err := json.Unmarshal(payload, &refreshedHolder); err != nil {
		t.Fatal(err)
	}
	if !refreshedHolder.UpdatedAt.After(staleUpdatedAt) {
		t.Fatalf("Heartbeat UpdatedAt = %v, want after stale value %v", refreshedHolder.UpdatedAt, staleUpdatedAt)
	}
	staleAfter = time.Minute
	if state := Peek(root, 1); len(state.Holders) != 1 {
		t.Fatalf("Peek = %+v, want the heartbeat-refreshed holder to survive", state)
	}
}

// TestReadTicketsReapsAnOldOrphanedTempFile pins the re-review's Minor 4
// finding: atomicWriteFile's hidden ".tmp-*" sibling can be left behind if
// the writing process is killed between CreateTemp and Rename. A leftover
// older than staleAfter — one no live writer could still be producing —
// is removed the next time anything lists this directory; a fresh one
// (still possibly a write genuinely in flight) is left alone.
//
// #753: the previous version used a deliberately tiny staleAfter (20ms) and
// registered the live ticket FIRST, before two os.WriteFile calls plus an
// os.Chtimes. That made the live ticket's own liveness check
// (isLive: time.Since(UpdatedAt) < staleAfter) depend on real elapsed time,
// and 20ms was not a safe margin against ordinary CI scheduling jitter: the
// CI failure that opened #753 recorded Total:0, meaning the live ticket
// itself was reaped as stale by the time Peek finally ran, exactly like the
// .tmp-old file next to it. A first attempt widened staleAfter to 2s and
// reordered Register/Heartbeat to run last, but round-2 review proved that
// still only shrinks the window rather than removing it (a 2.1s injected
// stall reproduced the identical Total:0 failure).
//
// The fix instead pins every timestamp readTicketsIn/reapStaleTempFile
// compares, so no comparison depends on real elapsed time at all, at the
// production-default staleAfter: .tmp-old is backdated to time.Unix(1, 0)
// (stale under any staleAfter, ever), .tmp-fresh is dated 24h in the future
// (time.Since of a future mtime is negative, so it can never register as
// stale), and the live ticket's own record is rewritten with UpdatedAt 24h
// in the future using the same os.WriteFile(ticket.path, json) idiom
// TestHeartbeatKeepsALiveWaiterFromAging already uses below. A 2.1s stall
// injected between the setup and Peek (matching round-2 review's repro)
// passes with this version; it was removed once confirmed.
func TestReadTicketsReapsAnOldOrphanedTempFile(t *testing.T) {
	root := t.TempDir()

	dir := ticketDir(root)
	oldTemp := filepath.Join(dir, ".tmp-old")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldTemp, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	longAgo := time.Unix(1, 0)
	if err := os.Chtimes(oldTemp, longAgo, longAgo); err != nil {
		t.Fatal(err)
	}
	freshTemp := filepath.Join(dir, ".tmp-fresh")
	if err := os.WriteFile(freshTemp, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	farFuture := time.Now().Add(24 * time.Hour)
	if err := os.Chtimes(freshTemp, farFuture, farFuture); err != nil {
		t.Fatal(err)
	}

	live := Register(root, Participant{PID: os.Getpid(), Summary: "go test"})
	defer live.Forget()
	liveTicket := ticketRecord{
		Participant: live.self,
		CreatedAt:   live.createdAt,
		UpdatedAt:   farFuture,
	}
	payload, err := json.Marshal(liveTicket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live.path, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	if state := Peek(root, 1); state.Total != 1 {
		t.Fatalf("Peek = %+v, want only the live ticket counted (temp files must never count)", state)
	}

	if _, err := os.Stat(oldTemp); !os.IsNotExist(err) {
		t.Fatalf("old .tmp-* file still present after a directory listing, stat error=%v", err)
	}
	if _, err := os.Stat(freshTemp); err != nil {
		t.Fatalf("fresh .tmp-* file was removed too early: %v", err)
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
