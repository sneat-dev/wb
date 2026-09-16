package runqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// tailCovWriteTicket writes a raw ticket record, letting a test control fields
// (created_at, updated_at, PID) that Register always fills with now/self.
func tailCovWriteTicket(t *testing.T, directory, name, summary, createdAt, updatedAt string) {
	t.Helper()
	payload := fmt.Sprintf(
		`{"pid":%d,"summary":%q,"created_at":%q,"updated_at":%q}`,
		os.Getpid(), summary, createdAt, updatedAt,
	)
	if err := os.WriteFile(filepath.Join(directory, name), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
}

// tailCovWriteHolder overwrites an announced holder record so a test can
// control StartedAt ordering without waiting on wall-clock time.
func tailCovWriteHolder(t *testing.T, path, summary string, started time.Time) {
	t.Helper()
	payload, err := json.Marshal(Holder{
		Participant: Participant{PID: os.Getpid(), Summary: summary},
		StartedAt:   started,
		UpdatedAt:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestTailCovIsLiveTrustsARecordWithNoUpdatedAt pins the one documented
// exemption from the heartbeat rule: a record written without an updated_at
// (zero time) is trusted for as long as its PID exists, so an older writer
// cannot be treated as stale purely for omitting the field.
func TestTailCovIsLiveTrustsARecordWithNoUpdatedAt(t *testing.T) {
	root := t.TempDir()
	directory := ticketDir(root)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, "no-updated-at.json"),
		[]byte(fmt.Sprintf(`{"pid":%d,"summary":"go test"}`, os.Getpid())),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if state := Peek(root, 1); state.Total != 1 {
		t.Fatalf("Peek = %+v, want the live ticket with no updated_at counted", state)
	}

	// Same shape, dead PID: the PID check must still win over the zero value.
	deadPath := filepath.Join(directory, "dead-no-updated-at.json")
	if err := os.WriteFile(deadPath, []byte(fmt.Sprintf(`{"pid":%d,"summary":"go test"}`, deadPID)), 0o600); err != nil {
		t.Fatal(err)
	}
	if state := Peek(root, 1); state.Total != 1 {
		t.Fatalf("Peek = %+v, want only the live ticket counted", state)
	}
	if _, err := os.Stat(deadPath); !os.IsNotExist(err) {
		t.Fatalf("dead PID ticket with no updated_at was not reaped: err=%v", err)
	}
}

// TestTailCovRegisterDegradesWhenTheTicketDirectoryCannotBeCreated covers the
// "registration is best-effort" contract at its source: an unusable projects
// root yields an invisible ticket, never an error or a panic.
func TestTailCovRegisterDegradesWhenTheTicketDirectoryCannotBeCreated(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ticket := Register(blocker, Participant{PID: os.Getpid(), Summary: "go test"})
	if ticket == nil {
		t.Fatal("Register returned nil")
	}
	if ticket.path != "" {
		t.Fatalf("Register recorded path %q, want an unregistered ticket", ticket.path)
	}
	if state := ticket.Snapshot(1); state.Position != 0 || state.Total != 0 {
		t.Fatalf("invisible ticket Snapshot = %+v, want the zero state", state)
	}
	ticket.Heartbeat() // must be a silent no-op
	ticket.Forget()    // must be a silent no-op
}

// TestTailCovRegisterDegradesWhenTheTicketFileCannotBeWritten covers the
// second best-effort failure: the directory exists but the ticket file cannot
// be created, so the caller still gets a usable (if invisible) Ticket.
func TestTailCovRegisterDegradesWhenTheTicketFileCannotBeWritten(t *testing.T) {
	tailCovRequireUnixFilesystem(t)
	root := t.TempDir()
	directory := ticketDir(root)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(directory, 0o700) }()

	ticket := Register(root, Participant{PID: os.Getpid(), Summary: "go test"})
	if ticket.path != "" {
		t.Fatalf("Register recorded path %q despite an unwritable directory", ticket.path)
	}
	if state := ticket.Snapshot(1); state.Total != 0 || state.Position != 0 {
		t.Fatalf("Snapshot = %+v, want the zero state for an invisible waiter", state)
	}
	ticket.Heartbeat()
	ticket.Forget()
}

// TestTailCovHeartbeatIsSafeOnNilAndInvisibleTickets pins the documented
// no-op behaviour of Heartbeat for callers that never registered.
func TestTailCovHeartbeatIsSafeOnNilAndInvisibleTickets(t *testing.T) {
	var nilTicket *Ticket
	nilTicket.Heartbeat()
	if state := nilTicket.Snapshot(1); state.Total != 0 {
		t.Fatalf("nil ticket Snapshot = %+v, want zero", state)
	}

	root := t.TempDir()
	blocker := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	Register(blocker, Participant{PID: os.Getpid(), Summary: "go test"}).Heartbeat()
}

// TestTailCovReadTicketsSkipsDirectoriesAndCorruptRecords proves neither a
// nested directory nor an unparsable file in the waiting directory can hide a
// real waiter, and that neither is deleted as if it were a stale ticket.
func TestTailCovReadTicketsSkipsDirectoriesAndCorruptRecords(t *testing.T) {
	root := t.TempDir()
	directory := ticketDir(root)
	nested := filepath.Join(directory, "nested")
	corrupt := filepath.Join(directory, "corrupt.json")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	live := Register(root, Participant{PID: os.Getpid(), Summary: "go test"})
	defer live.Forget()

	if state := Peek(root, 1); state.Total != 1 {
		t.Fatalf("Peek = %+v, want exactly the one live waiter counted", state)
	}
	if info, err := os.Stat(nested); err != nil || !info.IsDir() {
		t.Fatalf("nested directory was disturbed: info=%v err=%v", info, err)
	}
	if _, err := os.Stat(corrupt); err != nil {
		t.Fatalf("corrupt ticket file was removed, want it left in place: err=%v", err)
	}
}

// TestTailCovReadTicketsOrdersEqualCreatedAtByPath covers the tie-break in the
// waiter ordering: records written in the same instant are ordered by their
// file name so the queue position is stable rather than scheduler-dependent,
// while an older created_at still wins outright.
func TestTailCovReadTicketsOrdersEqualCreatedAtByPath(t *testing.T) {
	root := t.TempDir()
	directory := ticketDir(root)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tailCovWriteTicket(t, directory, "a.json", "tie-a", now, now)
	tailCovWriteTicket(t, directory, "b.json", "tie-b", now, now)
	tailCovWriteTicket(t, directory, "c.json", "older-c", "2020-01-01T00:00:00Z", now)

	listing := ListQueue(root, 1)
	got := make([]string, 0, len(listing.Waiting))
	for _, entry := range listing.Waiting {
		got = append(got, entry.Summary)
	}
	want := []string{"older-c", "tie-a", "tie-b"}
	if len(got) != len(want) {
		t.Fatalf("waiting entries = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("waiting entries = %v, want %v", got, want)
		}
	}
}

// TestTailCovReadTicketsTiesOnCreatedAtUsePathOrder exercises the tie-break
// directly through readTickets so the returned paths, not just the labels, are
// asserted.
func TestTailCovReadTicketsTiesOnCreatedAtUsePathOrder(t *testing.T) {
	root := t.TempDir()
	directory := ticketDir(root)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	same := "2026-09-08T14:59:50Z"
	fresh := time.Now().UTC().Format(time.RFC3339Nano)
	tailCovWriteTicket(t, directory, "second.json", "second", same, fresh)
	tailCovWriteTicket(t, directory, "first.json", "first", same, fresh)

	tickets := readTickets(root)
	if len(tickets) != 2 {
		t.Fatalf("readTickets returned %d records, want 2", len(tickets))
	}
	if filepath.Base(tickets[0].path) != "first.json" || filepath.Base(tickets[1].path) != "second.json" {
		t.Fatalf("readTickets order = %q, %q; want first.json then second.json",
			filepath.Base(tickets[0].path), filepath.Base(tickets[1].path))
	}
}

// TestTailCovReadHoldersTreatsNonPositiveBudgetAsOne proves a caller asking
// for zero slots still sees the holder of slot 0, matching Acquire's own
// floor of one.
func TestTailCovReadHoldersTreatsNonPositiveBudgetAsOne(t *testing.T) {
	root := t.TempDir()
	lease, _, err := Acquire(context.Background(), root, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	announcement := lease.Announce(Participant{PID: os.Getpid(), Summary: "go build"})
	defer announcement.Cleanup()

	for _, budget := range []int{0, -3} {
		if state := Peek(root, budget); len(state.Holders) != 1 {
			t.Fatalf("Peek(root, %d) = %+v, want the one announced holder", budget, state)
		}
	}
}

// TestTailCovReadHoldersSkipsCorruptRecords proves a corrupt holder file is
// ignored rather than reported, and that it is not mistaken for a stale record
// and deleted (only dead PIDs are reaped).
func TestTailCovReadHoldersSkipsCorruptRecords(t *testing.T) {
	root := t.TempDir()
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
	if err := os.WriteFile(announcement.paths[0], []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if state := Peek(root, 1); len(state.Holders) != 0 {
		t.Fatalf("Peek = %+v, want the corrupt holder excluded", state)
	}
	if _, err := os.Stat(announcement.paths[0]); err != nil {
		t.Fatalf("corrupt holder file was removed, want it left in place: err=%v", err)
	}
}

// TestTailCovReadHoldersSortsByStartedAt covers the holder ordering: slots are
// returned oldest first regardless of slot number.
func TestTailCovReadHoldersSortsByStartedAt(t *testing.T) {
	root := t.TempDir()
	lease, _, err := Acquire(context.Background(), root, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	announcement := lease.Announce(Participant{PID: os.Getpid(), Summary: "go build"})
	defer announcement.Cleanup()
	if len(announcement.paths) != 2 {
		t.Fatalf("Announce wrote %d holder files, want 2", len(announcement.paths))
	}
	// Round(0) drops the monotonic reading so the JSON round trip compares
	// equal by wall clock.
	base := time.Now().UTC().Add(-time.Minute).Round(0)
	tailCovWriteHolder(t, announcement.paths[0], "later-slot", base.Add(time.Minute))
	tailCovWriteHolder(t, announcement.paths[1], "earlier-slot", base)

	state := Peek(root, 2)
	if len(state.Holders) != 2 {
		t.Fatalf("Peek = %+v, want both announced holders", state)
	}
	if state.Holders[0].Summary != "earlier-slot" || state.Holders[1].Summary != "later-slot" {
		t.Fatalf("holder order = %q then %q, want earlier-slot then later-slot",
			state.Holders[0].Summary, state.Holders[1].Summary)
	}
	if !state.Holders[0].StartedAt.Before(state.Holders[1].StartedAt) {
		t.Fatalf("holder StartedAt values are not ascending: %v then %v",
			state.Holders[0].StartedAt, state.Holders[1].StartedAt)
	}
}

// TestTailCovAnnounceOnNilAndUnitlessLeases pins the documented no-op shape:
// both a nil Lease and a zero-unit Lease announce nothing and stay safe to
// heartbeat and clean up.
func TestTailCovAnnounceOnNilAndUnitlessLeases(t *testing.T) {
	var nilLease *Lease
	announcement := nilLease.Announce(Participant{PID: os.Getpid(), Summary: "go build"})
	if announcement == nil {
		t.Fatal("nil Lease.Announce returned nil, want an empty Announcement")
	}
	if len(announcement.paths) != 0 {
		t.Fatalf("nil Lease.Announce recorded %d holders, want 0", len(announcement.paths))
	}
	announcement.Heartbeat()
	announcement.Cleanup()

	var nilAnnouncement *Announcement
	nilAnnouncement.Heartbeat()
	nilAnnouncement.Cleanup()

	root := t.TempDir()
	lease, _, err := Acquire(context.Background(), root, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	zero := lease.Announce(Participant{PID: os.Getpid(), Summary: "go build"})
	if len(zero.paths) != 0 {
		t.Fatalf("zero-unit lease announced %d holders, want 0", len(zero.paths))
	}
	zero.Heartbeat()
	zero.Cleanup()
	if state := Peek(root, 2); len(state.Holders) != 0 {
		t.Fatalf("Peek = %+v, want no holders after a zero-unit announcement", state)
	}
}

// TestTailCovAnnounceSkipsSlotsWhoseHolderFileCannotBeWritten proves the
// best-effort contract on the holder side: an unwritable queue directory
// leaves the slot anonymous instead of failing the run.
func TestTailCovAnnounceSkipsSlotsWhoseHolderFileCannotBeWritten(t *testing.T) {
	tailCovRequireUnixFilesystem(t)
	root := t.TempDir()
	lease, _, err := Acquire(context.Background(), root, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	directory := queueRoot(root)
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(directory, 0o700) }()

	announcement := lease.Announce(Participant{PID: os.Getpid(), Summary: "go build"})
	if len(announcement.paths) != 0 {
		t.Fatalf("Announce recorded %d holders despite an unwritable directory, want 0", len(announcement.paths))
	}
	if state := Peek(root, 1); len(state.Holders) != 0 {
		t.Fatalf("Peek = %+v, want the unwritable slot to stay anonymous", state)
	}
	announcement.Heartbeat()
	announcement.Cleanup()
}

// TestTailCovCleanupIsIdempotent proves Cleanup removes the holder records and
// can be called again (and on a nil Announcement) without error.
func TestTailCovCleanupIsIdempotent(t *testing.T) {
	root := t.TempDir()
	lease, _, err := Acquire(context.Background(), root, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	announcement := lease.Announce(Participant{PID: os.Getpid(), Summary: "go build"})
	if len(announcement.paths) != 1 {
		t.Fatalf("Announce wrote %d holder files, want 1", len(announcement.paths))
	}
	holderPath := announcement.paths[0]
	announcement.Cleanup()
	if _, err := os.Stat(holderPath); !os.IsNotExist(err) {
		t.Fatalf("holder file survived Cleanup: err=%v", err)
	}
	announcement.Cleanup()
	if announcement.paths != nil {
		t.Fatalf("announcement.paths = %v after Cleanup, want nil", announcement.paths)
	}
}
