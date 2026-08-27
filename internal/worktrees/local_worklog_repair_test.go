package worktrees

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAppendLocalEventRepairsTornFinalJournalRecord(t *testing.T) {
	clearIdentity(t)
	worktree := custodyWorktree(t)
	first := LocalWorkLogEvent{ID: "first-stable-event", Type: LocalEventHandoff,
		At: time.Date(2026, time.August, 25, 17, 0, 0, 0, time.UTC), Message: "received", Result: "received"}
	if _, _, err := appendLocalEvent(worktree, first); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, localWorkLogEventsName)
	appendTestBytes(t, eventsPath, []byte(`{"version":1,"seq":1,"id":"torn`))
	second := LocalWorkLogEvent{ID: "second-stable-event", Type: LocalEventHandoff,
		At: time.Date(2026, time.August, 25, 17, 1, 0, 0, time.UTC), Message: "completed", Result: "completed"}
	if _, _, err := appendLocalEvent(worktree, second); err != nil {
		t.Fatalf("append after torn final journal record: %v", err)
	}
	events, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if eventByID(events, first.ID) == nil || eventByID(events, second.ID) == nil {
		t.Fatalf("events after repair = %#v", events)
	}
	if occurrences := outboxEventOccurrences(t, mustReadFile(t, filepath.Join(filepath.Dir(eventsPath), localWorkLogOutboxName)), second.ID); occurrences != 1 {
		t.Fatalf("second outbox occurrences=%d, want 1", occurrences)
	}
}

func TestAppendLocalEventRefusesCompleteInvalidUnterminatedJournalRecord(t *testing.T) {
	clearIdentity(t)
	worktree := custodyWorktree(t)
	first := LocalWorkLogEvent{ID: "first-stable-event", Type: LocalEventHandoff,
		At: time.Date(2026, time.August, 25, 17, 0, 0, 0, time.UTC), Message: "received", Result: "received"}
	if _, _, err := appendLocalEvent(worktree, first); err != nil {
		t.Fatal(err)
	}
	eventsPath := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, localWorkLogEventsName)
	invalid := LocalWorkLogEvent{Version: 1, Seq: 9, ID: "complete-but-conflicting", Type: LocalEventHandoff,
		At: first.At.Add(time.Minute), Message: "wrong sequence", Result: "completed"}
	raw, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	appendTestBytes(t, eventsPath, raw)
	second := LocalWorkLogEvent{ID: "second-stable-event", Type: LocalEventHandoff,
		At: first.At.Add(2 * time.Minute), Message: "completed", Result: "completed"}
	if _, _, err := appendLocalEvent(worktree, second); err == nil || !strings.Contains(err.Error(), "sequence gap") {
		t.Fatalf("error=%v, want complete invalid evidence conflict", err)
	}
}

func TestAppendLocalEventRepairsTornFinalOutboxRecord(t *testing.T) {
	clearIdentity(t)
	worktree := custodyWorktree(t)
	event := LocalWorkLogEvent{ID: "stable-session-event", Type: LocalEventHandoff,
		At: time.Date(2026, time.August, 25, 17, 0, 0, 0, time.UTC), Message: "received", Result: "received"}
	if _, _, err := appendLocalEvent(worktree, event); err != nil {
		t.Fatal(err)
	}
	outboxPath := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, localWorkLogOutboxName)
	appendTestBytes(t, outboxPath, []byte(`{"version":1,"seq":1`))
	if _, _, err := appendLocalEvent(worktree, event); err != nil {
		t.Fatalf("exact replay did not repair torn outbox: %v", err)
	}
	if occurrences := outboxEventOccurrences(t, mustReadFile(t, outboxPath), event.ID); occurrences != 1 {
		t.Fatalf("outbox occurrences=%d, want 1", occurrences)
	}
}

func TestAppendLocalEventConcurrentAtomicJournalUpdates(t *testing.T) {
	clearIdentity(t)
	worktree := custodyWorktree(t)
	const count = 24
	start := make(chan struct{})
	errorsByWorker := make(chan error, count)
	var workers sync.WaitGroup
	for worker := range count {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, _, err := appendLocalEvent(worktree, LocalWorkLogEvent{
				ID: fmt.Sprintf("concurrent-event-%02d", worker), Type: LocalEventSteer,
				At:      time.Date(2026, time.August, 25, 18, worker, 0, 0, time.UTC),
				Message: fmt.Sprintf("worker %d", worker),
			})
			errorsByWorker <- err
		}()
	}
	close(start)
	workers.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Fatal(err)
		}
	}
	events, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool, count)
	for seq, event := range events {
		if event.Seq != seq || seen[event.ID] {
			t.Fatalf("invalid concurrent event at %d: %#v", seq, event)
		}
		seen[event.ID] = true
	}
	for worker := range count {
		if !seen[fmt.Sprintf("concurrent-event-%02d", worker)] {
			t.Fatalf("concurrent worker %d event was lost", worker)
		}
	}
	if outboxCount, err := countLocalOutbox(worktree); err != nil || outboxCount != len(events) {
		t.Fatalf("outbox count=%d err=%v, want all %d journal events", outboxCount, err, len(events))
	}
}

func TestAppendLocalEventExactReplayRepairsOutboxAndProjection(t *testing.T) {
	clearIdentity(t)
	worktree := custodyWorktree(t)
	wanted := LocalWorkLogEvent{
		ID: "session-handoff-completed-1", Type: LocalEventHandoff,
		At:      time.Date(2026, time.August, 25, 17, 0, 0, 0, time.UTC),
		Message: "target successor completed", Result: "completed",
		Extra: map[string]any{"handoff_id": "handoff-123", "endpoint": "target"},
	}
	created, _, err := appendLocalEvent(worktree, wanted)
	if err != nil {
		t.Fatal(err)
	}
	eventsBefore, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory)
	outboxPath := filepath.Join(directory, localWorkLogOutboxName)
	outbox, err := os.ReadFile(outboxPath)
	if err != nil {
		t.Fatal(err)
	}
	filtered := filterOutboxEvent(t, outbox, wanted.ID)
	if err := os.WriteFile(outboxPath, filtered, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(directory, localWorkLogProjectionName)); err != nil {
		t.Fatal(err)
	}

	// A stable caller may omit At on replay. The existing immutable event time
	// is authoritative, while every other byte must still match.
	replay := wanted
	replay.At = time.Time{}
	got, projection, err := appendLocalEvent(worktree, replay)
	if err != nil {
		t.Fatal(err)
	}
	if !sameLocalEvent(got, created) || projection.LastEventID != created.ID {
		t.Fatalf("replay event=%#v projection=%#v", got, projection)
	}
	eventsAfter, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("events grew from %d to %d on exact replay", len(eventsBefore), len(eventsAfter))
	}
	if occurrences := outboxEventOccurrences(t, mustReadFile(t, outboxPath), wanted.ID); occurrences != 1 {
		t.Fatalf("outbox event occurrences=%d, want 1", occurrences)
	}
	if _, err := readLocalProjection(worktree); err != nil {
		t.Fatalf("projection was not repaired: %v", err)
	}
}

func TestAppendLocalEventSameIDDifferentEvidenceConflicts(t *testing.T) {
	clearIdentity(t)
	worktree := custodyWorktree(t)
	event := LocalWorkLogEvent{ID: "stable-session-event", Type: LocalEventHandoff,
		At: time.Date(2026, time.August, 25, 17, 0, 0, 0, time.UTC), Message: "received", Result: "received"}
	if _, _, err := appendLocalEvent(worktree, event); err != nil {
		t.Fatal(err)
	}
	conflict := event
	conflict.Message = "different"
	if _, _, err := appendLocalEvent(worktree, conflict); err == nil || !strings.Contains(err.Error(), "different immutable evidence") {
		t.Fatalf("error=%v, want immutable ID conflict", err)
	}
}

func TestAppendLocalEventConflictingOutboxEvidenceRefusesRepair(t *testing.T) {
	clearIdentity(t)
	worktree := custodyWorktree(t)
	event := LocalWorkLogEvent{ID: "stable-session-event", Type: LocalEventHandoff,
		At: time.Date(2026, time.August, 25, 17, 0, 0, 0, time.UTC), Message: "received", Result: "received"}
	created, _, err := appendLocalEvent(worktree, event)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory)
	outboxPath := filepath.Join(directory, localWorkLogOutboxName)
	outbox := filterOutboxEvent(t, mustReadFile(t, outboxPath), event.ID)
	forged := created
	forged.Message = "forged"
	line, err := json.Marshal(forged)
	if err != nil {
		t.Fatal(err)
	}
	outbox = append(outbox, append(line, '\n')...)
	if err := os.WriteFile(outboxPath, outbox, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := appendLocalEvent(worktree, event); err == nil || !strings.Contains(err.Error(), "outbox event ID") {
		t.Fatalf("error=%v, want conflicting outbox refusal", err)
	}
}

func filterOutboxEvent(t *testing.T, content []byte, eventID string) []byte {
	t.Helper()
	var filtered bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var event LocalWorkLogEvent
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if event.ID != eventID {
			filtered.Write(line)
			filtered.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return filtered.Bytes()
}

func outboxEventOccurrences(t *testing.T, content []byte, eventID string) int {
	t.Helper()
	count := 0
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		var event LocalWorkLogEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		if event.ID == eventID {
			count++
		}
	}
	return count
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func appendTestBytes(t *testing.T, path string, content []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func eventByID(events []LocalWorkLogEvent, id string) *LocalWorkLogEvent {
	for index := range events {
		if events[index].ID == id {
			return &events[index]
		}
	}
	return nil
}
