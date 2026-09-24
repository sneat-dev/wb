package runqueue

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTicketPathLockedOnNilReceiverReturnsEmpty drives pathLocked's
// nil-receiver guard (visibility.go): admitHeavy's head-of-queue comparison
// calls it on a caller-supplied ticket that, in principle, could be nil.
func TestTicketPathLockedOnNilReceiverReturnsEmpty(t *testing.T) {
	t.Parallel()
	var ticket *Ticket
	if got := ticket.pathLocked(); got != "" {
		t.Fatalf("(*Ticket)(nil).pathLocked() = %q, want \"\"", got)
	}
}

// TestReadTicketsInReapsAnAgedTempFile drives readTicketsIn's dot-prefixed
// reap branch (visibility.go): a ".tmp-*" sibling atomicWriteFile would
// leave behind after a mid-write crash, once older than staleAfter, is
// removed the next time anything lists the directory.
func TestReadTicketsInReapsAnAgedTempFile(t *testing.T) {
	restoreStaleAfter := setStaleAfterForTest(t, time.Millisecond)
	defer restoreStaleAfter()

	dir := t.TempDir()
	tempPath := filepath.Join(dir, ".tmp-leftover")
	if err := os.WriteFile(tempPath, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tempPath, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	_ = readTicketsIn(dir)

	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("readTicketsIn did not reap an aged .tmp- file, stat err = %v", err)
	}
}

// TestReadTicketsInSkipsANonTempDotFileWithoutReaping drives
// reapStaleTempFile's own non-"tmp-" early return (visibility.go), reached
// via readTicketsIn's dot-prefix branch for a dotfile that is not one of
// atomicWriteFile's own temp siblings (e.g. a stray ".DS_Store").
func TestReadTicketsInSkipsANonTempDotFileWithoutReaping(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	strayPath := filepath.Join(dir, ".DS_Store")
	if err := os.WriteFile(strayPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = readTicketsIn(dir)
	if _, err := os.Stat(strayPath); err != nil {
		t.Fatalf("readTicketsIn removed a non-temp dotfile it must leave alone: %v", err)
	}
}

// TestReadTicketsInSkipsAnUnreadableTicketFile drives readTicketsIn's
// ReadFile error branch (visibility.go): a permission-denied ticket file is
// silently skipped rather than failing the whole listing.
func TestReadTicketsInSkipsAnUnreadableTicketFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permission checks")
	}
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "0000000000000000001-1-1.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if got := readTicketsIn(dir); len(got) != 0 {
		t.Fatalf("readTicketsIn with an unreadable ticket file = %#v, want none", got)
	}
}

// TestReadTicketsInSkipsATicketFileWithInvalidJSON drives readTicketsIn's
// json.Unmarshal error branch (visibility.go).
func TestReadTicketsInSkipsATicketFileWithInvalidJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "0000000000000000001-1-1.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readTicketsIn(dir); len(got) != 0 {
		t.Fatalf("readTicketsIn with invalid JSON = %#v, want none", got)
	}
}

// TestReadTicketsInReapsADeadTicket drives readTicketsIn's own isLive-false
// branch (visibility.go): a ticket recorded against a PID this OS reports
// as not running is reaped rather than reported as a live waiter.
func TestReadTicketsInReapsADeadTicket(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "0000000000000000001-1-1.json")
	if err := os.WriteFile(path, []byte(`{"pid":999999999,"updated_at":"2020-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readTicketsIn(dir); len(got) != 0 {
		t.Fatalf("readTicketsIn with a dead-PID ticket = %#v, want it reaped", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("readTicketsIn left the dead ticket file in place, stat err = %v", err)
	}
}

// TestReadHolderRecordsReapsAnAgedTempFile is readHolderRecords' own version
// of TestReadTicketsInReapsAnAgedTempFile (visibility.go): the same reap
// helper, reached from the holder-record listing path instead of the
// ticket-record one.
func TestReadHolderRecordsReapsAnAgedTempFile(t *testing.T) {
	restoreStaleAfter := setStaleAfterForTest(t, time.Millisecond)
	defer restoreStaleAfter()

	dir := t.TempDir()
	tempPath := filepath.Join(dir, ".tmp-leftover")
	if err := os.WriteFile(tempPath, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tempPath, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	_ = readHolderRecords(dir)
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("readHolderRecords did not reap an aged .tmp- file, stat err = %v", err)
	}
}

// TestReadHolderRecordsSkipsAnUnreadableFile is readHolderRecords' own
// version of the unreadable-file case (visibility.go).
func TestReadHolderRecordsSkipsAnUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permission checks")
	}
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "holder.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if got := readHolderRecords(dir); len(got) != 0 {
		t.Fatalf("readHolderRecords with an unreadable file = %#v, want none", got)
	}
}

// TestReadHolderRecordsSkipsInvalidJSON is readHolderRecords' own version of
// the bad-JSON case (visibility.go).
func TestReadHolderRecordsSkipsInvalidJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "holder.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readHolderRecords(dir); len(got) != 0 {
		t.Fatalf("readHolderRecords with invalid JSON = %#v, want none", got)
	}
}

// TestSnapshotOnTheHeavyNamespaceReadsTheHeavyDirectories drives snapshot's
// namespaceHeavy branch (visibility.go), distinct from the legacy-pool
// branch every other snapshot-driven test in this package exercises.
func TestSnapshotOnTheHeavyNamespaceReadsTheHeavyDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ticket := RegisterHeavy(root, Participant{PID: 1})
	t.Cleanup(func() { ticket.Forget() })
	state := ticket.Snapshot(8)
	if state.Total != 1 || state.Position != 1 {
		t.Fatalf("Snapshot(heavy namespace) = %#v, want Total=1 Position=1 for its own sole waiter", state)
	}
}

// TestListQueueReportsAHeavyWaiter drives ListQueue's heavy-waiting branch
// (visibility.go).
func TestListQueueReportsAHeavyWaiter(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ticket := RegisterHeavy(root, Participant{PID: 1, Summary: "go test ./..."})
	t.Cleanup(func() { ticket.Forget() })
	listing := ListQueue(root, 8)
	if len(listing.Waiting) != 1 || listing.Waiting[0].Summary != "go test ./..." {
		t.Fatalf("ListQueue with one heavy waiter = %#v, want it reported", listing.Waiting)
	}
	if listing.HeavyK != 1 {
		t.Fatalf("ListQueue.HeavyK = %d, want 1", listing.HeavyK)
	}
}

// TestGroupRunningHoldersCollapsesRepeatedSlotsForOneHolder drives
// groupRunningHolders' own collapse branch (visibility.go): two holder
// records sharing one PID+StartedAt key (readHolders' one-record-per-slot
// output for a multi-unit legacy holder) collapse into a single QueueEntry
// with summed Units, instead of being reported as two distinct holders.
func TestGroupRunningHoldersCollapsesRepeatedSlotsForOneHolder(t *testing.T) {
	t.Parallel()
	started := time.Now().UTC()
	holders := []Holder{
		{Participant: Participant{PID: 42}, StartedAt: started},
		{Participant: Participant{PID: 42}, StartedAt: started},
	}
	entries := groupRunningHolders(holders, time.Now().UTC())
	if len(entries) != 1 {
		t.Fatalf("groupRunningHolders(two slots, same holder) = %d entries, want 1", len(entries))
	}
	if entries[0].Units != 2 {
		t.Fatalf("groupRunningHolders(two slots, same holder).Units = %d, want 2", entries[0].Units)
	}
}

// setStaleAfterForTest overrides the package's staleAfter var for the
// duration of the calling test, restoring it on the returned func. Not
// parallel-safe: staleAfter is a shared package variable, exactly like
// existing staleAfter-shrinking tests elsewhere in this package.
func setStaleAfterForTest(t *testing.T, d time.Duration) func() {
	t.Helper()
	previous := staleAfter
	staleAfter = d
	return func() { staleAfter = previous }
}
