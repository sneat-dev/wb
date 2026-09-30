package worktrees

import (
	"testing"
	"time"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// Each compatibility name remains callable while the journal implementation lives in its leaf.
func TestJournalFacadeAdapters(t *testing.T) {
	t.Parallel()
	worktree := newJournalWorktree(t)
	journal, err := openJournalDirectory(worktree, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Error(err)
		}
	})
	fd, err := openJournalComponent(int(journal.Fd()), worklogDirectory, true)
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(fd)
	directory, err := openJournalSubdirectory(worktree, worklogDirectory, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := directory.Close(); err != nil {
			t.Error(err)
		}
	})
	event := LocalWorkLogEvent{Version: 1, ID: "facade", Type: LocalEventSteer, At: time.Unix(10, 0).UTC()}
	if err := validateLocalEventForSequence(event, nil); err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeLocalEvents([]LocalWorkLogEvent{event})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseLocalEvents(encoded)
	if err != nil || len(parsed) != 1 {
		t.Fatalf("parse=%v/%v", parsed, err)
	}
	parsed, repair, err := parseLocalEventsForRepair(encoded)
	if err != nil || repair || len(parsed) != 1 {
		t.Fatalf("repair parse=%v/%v/%v", parsed, repair, err)
	}
	if !sameLocalEvent(event, parsed[0]) || localEventID(nil, event) == "" {
		t.Fatal("identity adapters")
	}
	if err := rewriteLocalEventJournal(directory, parsed); err != nil {
		t.Fatal(err)
	}
	if got, repair, err := readLocalEventsForAppend(directory); err != nil || repair || len(got) != 1 {
		t.Fatalf("append read=%v/%v/%v", got, repair, err)
	}
	unlock, err := lockLocalWorkLog(directory)
	if err != nil {
		t.Fatal(err)
	}
	next := LocalWorkLogEvent{Version: 1, ID: "next", Type: LocalEventSteer, At: time.Unix(11, 0).UTC()}
	if _, _, err := appendLocalEventUnderLock(worktree, directory, next); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	events, err := readLocalEvents(worktree)
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%v/%v", events, err)
	}
	if content, err := readLocalWorkLogBytes(worktree, localWorkLogEventsName); err != nil || len(content) == 0 {
		t.Fatalf("bytes=%q/%v", content, err)
	}
	if err := repairLocalOutbox(directory, events); err != nil {
		t.Fatal(err)
	}
	if _, err := repairLocalEventDerivatives(worktree, directory, events); err != nil {
		t.Fatal(err)
	}
	if _, err := repairCurrentLocalProjection(worktree); err != nil {
		t.Fatal(err)
	}
	if projection, err := readLocalProjection(worktree); err != nil || projection.LastEventID != "next" {
		t.Fatalf("projection=%#v/%v", projection, err)
	}
	if projection, err := rebuildLocalProjection(events); err != nil || projection.LastEventID != "next" {
		t.Fatalf("rebuild=%#v/%v", projection, err)
	}
	if count, err := countLocalOutbox(worktree); err != nil || count != 2 {
		t.Fatalf("outbox=%d/%v", count, err)
	}
}
