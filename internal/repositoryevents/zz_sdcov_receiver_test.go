package repositoryevents

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
)

func TestSdCovSleepContextHonorsTimerAndCancellation(t *testing.T) {
	t.Parallel()
	if err := sleepContext(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("sleepContext with live context = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepContext with cancelled context = %v", err)
	}
}

func TestSdCovSyncDirectoryReportsMissingDirectory(t *testing.T) {
	t.Parallel()
	if err := syncDirectory(t.TempDir()); err != nil {
		t.Fatalf("syncDirectory(existing) = %v", err)
	}
	if err := syncDirectory(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("syncDirectory accepted a missing directory")
	}
}

func TestSdCovReceiverRunUsesContractWaitDefaults(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		pollWait time.Duration
		want     time.Duration
	}{
		{name: "zero falls back to the contract default", want: repositoryevent.DefaultWaitSeconds * time.Second},
		{name: "above the maximum falls back", pollWait: time.Hour, want: repositoryevent.DefaultWaitSeconds * time.Second},
		{name: "a valid wait is honored", pollWait: 5 * time.Second, want: 5 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			source := &sdCovSource{}
			source.poll = func(cursor string, limit int, _ time.Duration) (repositoryevent.PollResponse, error) {
				if limit != repositoryevent.DefaultLimit {
					t.Errorf("poll limit = %d, want %d", limit, repositoryevent.DefaultLimit)
				}
				cancel()
				return repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: cursor, NextCursor: cursor}, nil
			}
			receiver := Receiver{Source: source, Queue: &enqueueFake{}, PollWait: test.pollWait, RetryDelay: time.Millisecond, ProgressEvery: time.Hour}
			receiver.Run(ctx)
			waits := source.recordedWaits()
			if len(waits) == 0 || waits[0] != test.want {
				t.Fatalf("poll waits = %v, want first %v", waits, test.want)
			}
		})
	}
}

func TestSdCovReceiverRunFallsBackForInvalidDurationsAndReturnsOnCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := &sdCovSource{}
	receiver := Receiver{Source: source, Queue: &enqueueFake{}, PollWait: -time.Second, RetryDelay: -time.Second, ProgressEvery: time.Hour}
	receiver.Run(ctx)
	if waits := source.recordedWaits(); len(waits) != 0 {
		t.Fatalf("cancelled receiver polled the provider: %v", waits)
	}
}

func TestSdCovReceiverRunRetriesFailedPollsOnTheTimer(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var polls int
	source := &sdCovSource{}
	source.poll = func(cursor string, _ int, _ time.Duration) (repositoryevent.PollResponse, error) {
		polls++
		switch {
		case polls <= 2:
			return repositoryevent.PollResponse{}, errors.New("provider unavailable")
		case polls == 3:
			return repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: cursor, NextCursor: cursor}, nil
		default:
			cancel()
			return repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: cursor, NextCursor: cursor}, nil
		}
	}
	var messages []string
	receiver := Receiver{
		Source: source, Queue: &enqueueFake{}, Cursor: CursorStore{Path: filepath.Join(t.TempDir(), "cursor.json")},
		PollWait: time.Millisecond, RetryDelay: time.Millisecond, ProgressEvery: time.Hour,
		Progress: func(message string) { messages = append(messages, message) },
	}
	done := make(chan struct{})
	go func() {
		receiver.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("receiver did not stop after cancellation")
	}
	if polls < 4 {
		t.Fatalf("polls = %d, want at least 4", polls)
	}
	failures := 0
	for _, message := range messages {
		if strings.Contains(message, "provider unavailable") {
			failures++
		}
	}
	if failures != 2 {
		t.Fatalf("retry progress messages = %v", messages)
	}
}

func TestSdCovReceiverRunStopsWhenCancelledDuringRetryDelay(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	retrying := make(chan struct{}, 1)
	source := &sdCovSource{}
	source.poll = func(string, int, time.Duration) (repositoryevent.PollResponse, error) {
		return repositoryevent.PollResponse{}, errors.New("provider unavailable")
	}
	receiver := Receiver{
		Source: source, Queue: &enqueueFake{}, Cursor: CursorStore{Path: filepath.Join(t.TempDir(), "cursor.json")},
		PollWait: time.Millisecond, RetryDelay: time.Minute, ProgressEvery: time.Hour,
		Progress: func(message string) {
			if strings.Contains(message, "provider unavailable") {
				select {
				case retrying <- struct{}{}:
				default:
				}
			}
		},
	}
	done := make(chan struct{})
	go func() {
		receiver.Run(ctx)
		close(done)
	}()
	select {
	case <-retrying:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("receiver never reported a failed poll")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("receiver ignored cancellation while waiting to retry")
	}
}

func TestSdCovReceiverRunReportsProgressWhileWaiting(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tickerSeen := make(chan struct{})
	var once sync.Once
	closingProgress := make(chan string, 8)
	source := &sdCovSource{}
	source.poll = func(cursor string, _ int, _ time.Duration) (repositoryevent.PollResponse, error) {
		select {
		case <-tickerSeen:
		case <-time.After(5 * time.Second):
		}
		cancel()
		return repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: cursor, NextCursor: cursor}, nil
	}
	receiver := Receiver{
		Source: source, Queue: &enqueueFake{}, Cursor: CursorStore{Path: filepath.Join(t.TempDir(), "cursor.json")},
		PollWait: time.Millisecond, RetryDelay: time.Millisecond, ProgressEvery: time.Millisecond,
		Progress: func(message string) {
			if strings.Contains(message, "waiting for provider events") {
				select {
				case closingProgress <- message:
				default:
				}
				once.Do(func() { close(tickerSeen) })
			}
		},
	}
	done := make(chan struct{})
	go func() {
		receiver.Run(ctx)
		close(done)
	}()
	select {
	case message := <-closingProgress:
		if !strings.Contains(message, "waiting for provider events") {
			t.Fatalf("ticker progress = %q", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("receiver never reported waiting for provider events")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("receiver did not stop")
	}
}

func TestSdCovReceiverRunStopsTickerOnRepeatedCancellation(t *testing.T) {
	t.Parallel()
	cursor := CursorStore{Path: filepath.Join(t.TempDir(), "cursor.json")}
	for attempt := 0; attempt < 64; attempt++ {
		ctx, cancel := context.WithCancel(context.Background())
		source := &sdCovSource{}
		source.poll = func(current string, _ int, _ time.Duration) (repositoryevent.PollResponse, error) {
			cancel()
			return repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: current, NextCursor: current}, nil
		}
		receiver := Receiver{Source: source, Queue: &enqueueFake{}, Cursor: cursor, PollWait: time.Millisecond, RetryDelay: time.Millisecond, ProgressEvery: time.Millisecond}
		receiver.Run(ctx)
		if waits := source.recordedWaits(); len(waits) != 1 {
			t.Fatalf("attempt %d polled %d times, want exactly 1", attempt, len(waits))
		}
	}
}

func TestSdCovReceiveOnceRequiresSourceAndQueue(t *testing.T) {
	t.Parallel()
	if err := (Receiver{}).ReceiveOnce(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unconfigured receiver error = %v", err)
	}
	partial := Receiver{Source: &sdCovSource{}}
	if err := partial.ReceiveOnce(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("receiver without queue error = %v", err)
	}
}

func TestSdCovReceiveOnceSurfacesCursorAndPollFailures(t *testing.T) {
	t.Parallel()
	corrupt := CursorStore{Path: filepath.Join(t.TempDir(), "cursor.json")}
	if err := os.WriteFile(corrupt.Path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	receiver := Receiver{Source: &sdCovSource{}, Queue: &enqueueFake{}, Cursor: corrupt}
	if err := receiver.ReceiveOnce(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "load repository event cursor") {
		t.Fatalf("corrupt cursor error = %v", err)
	}

	source := &sdCovSource{}
	source.poll = func(string, int, time.Duration) (repositoryevent.PollResponse, error) {
		return repositoryevent.PollResponse{}, errors.New("poll exploded")
	}
	receiver = Receiver{Source: source, Queue: &enqueueFake{}, Cursor: CursorStore{Path: filepath.Join(t.TempDir(), "cursor.json")}}
	if err := receiver.ReceiveOnce(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "poll exploded") {
		t.Fatalf("poll failure error = %v", err)
	}
}

func TestSdCovReceiveOnceResumesPendingAcknowledgement(t *testing.T) {
	t.Parallel()
	cursor := CursorStore{Path: filepath.Join(t.TempDir(), "cursor.json")}
	state := cursorState{
		Version:    repositoryevent.ContractVersion,
		Cursor:     "cursor-1",
		PendingAck: &repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: "cursor-2", EventIDs: []string{"event-1"}},
	}
	if err := cursor.saveState(state); err != nil {
		t.Fatal(err)
	}
	source := &sdCovSource{}
	source.ack = func(repositoryevent.AckRequest) (repositoryevent.AckResponse, error) {
		return repositoryevent.AckResponse{}, errors.New("provider down")
	}
	receiver := Receiver{Source: source, Queue: &enqueueFake{}, Cursor: cursor}
	if err := receiver.ReceiveOnce(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "resume repository event acknowledgement") {
		t.Fatalf("resume ack error = %v", err)
	}

	source.ack = nil
	source.poll = sdCovEmptyPoll
	var progress []string
	receiver.Progress = func(message string) { progress = append(progress, message) }
	if err := receiver.ReceiveOnce(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if got, err := cursor.Load(); err != nil || got != "cursor-2" {
		t.Fatalf("cursor after resume = %q, %v", got, err)
	}
	acks := source.recordedAcks()
	if len(acks) != 2 || acks[1].Cursor != "cursor-2" || len(acks[1].EventIDs) != 1 {
		t.Fatalf("resumed acks = %+v", acks)
	}

	// A full batch acknowledges every durable enqueue and reports progress.
	event := receiverEvent("event-acked")
	source.poll = func(string, int, time.Duration) (repositoryevent.PollResponse, error) {
		return repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: "cursor-2", NextCursor: "cursor-3", Events: []repositoryevent.Event{event}}, nil
	}
	queue := &enqueueFake{}
	receiver.Queue = queue
	if err := receiver.ReceiveOnce(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if len(queue.ids) != 1 || queue.ids[0] != event.ID {
		t.Fatalf("enqueued = %v", queue.ids)
	}
	acks = source.recordedAcks()
	if len(acks) != 3 || acks[2].Cursor != "cursor-3" || len(acks[2].EventIDs) != 1 {
		t.Fatalf("acks = %+v", acks)
	}
	if len(progress) != 1 || !strings.Contains(progress[0], "acknowledged 1 event(s)") {
		t.Fatalf("progress = %v", progress)
	}
}

func TestSdCovCursorStoreLoadStateRejectsInvalidFiles(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	write := func(t *testing.T, name, body string) CursorStore {
		t.Helper()
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return CursorStore{Path: path}
	}

	if _, err := (CursorStore{Path: directory}).loadState(); err == nil {
		t.Fatal("loadState accepted a directory path")
	}
	if _, err := write(t, "invalid-json.json", "{").loadState(); err == nil {
		t.Fatal("loadState accepted invalid JSON")
	}
	if _, err := write(t, "bad-version.json", `{"version":2,"cursor":"cursor-1"}`).loadState(); err == nil || !strings.Contains(err.Error(), "unsupported cursor state version") {
		t.Fatalf("version error = %v", err)
	}
	if _, err := write(t, "bad-cursor.json", `{"version":1,"cursor":"bad\ncursor"}`).loadState(); err == nil {
		t.Fatal("loadState accepted an invalid cursor")
	}
	if _, err := write(t, "bad-pending.json", `{"version":1,"cursor":"cursor-1","pending_ack":{"version":99,"cursor":"cursor-2","event_ids":["event-1"]}}`).loadState(); err == nil || !strings.Contains(err.Error(), "invalid pending repository event acknowledgement") {
		t.Fatalf("pending ack error = %v", err)
	}

	valid := write(t, "valid.json", `{"version":1,"cursor":"cursor-1"}`)
	state, err := valid.loadState()
	if err != nil || state.Cursor != "cursor-1" || state.PendingAck != nil {
		t.Fatalf("valid state = %+v, %v", state, err)
	}
	if got, err := valid.Load(); err != nil || got != "cursor-1" {
		t.Fatalf("Load = %q, %v", got, err)
	}
}

func TestSdCovCursorStoreSaveValidatesAndRoundTrips(t *testing.T) {
	t.Parallel()
	store := CursorStore{Path: filepath.Join(t.TempDir(), "nested", "cursor.json")}
	if got, err := store.Load(); err != nil || got != "" {
		t.Fatalf("missing cursor = %q, %v", got, err)
	}
	if err := store.Save("cursor-42"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(); err != nil || got != "cursor-42" {
		t.Fatalf("loaded cursor = %q, %v", got, err)
	}
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("cursor file mode = %o, want 600", perm)
	}
	if err := store.Save(""); err == nil {
		t.Fatal("Save accepted an empty cursor")
	}
	if err := store.Save(strings.Repeat("c", 2048)); err == nil {
		t.Fatal("Save accepted an oversized cursor")
	}
}

func TestSdCovCursorStoreSaveStateRejectsInvalidStatesAndFilesystemFailures(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	store := CursorStore{Path: filepath.Join(directory, "cursor.json")}

	invalidPending := cursorState{
		Version:    repositoryevent.ContractVersion,
		Cursor:     "cursor-1",
		PendingAck: &repositoryevent.AckRequest{Version: 99, Cursor: "cursor-2", EventIDs: []string{"event-1"}},
	}
	if err := store.saveState(invalidPending); err == nil {
		t.Fatal("saveState accepted an invalid pending acknowledgement")
	}

	blocker := filepath.Join(directory, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	blocked := CursorStore{Path: filepath.Join(blocker, "cursor.json")}
	if err := blocked.saveState(cursorState{Version: repositoryevent.ContractVersion, Cursor: "cursor-1"}); err == nil {
		t.Fatal("saveState accepted a file in place of its directory")
	}

	target := filepath.Join(directory, "occupied.json")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "child"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	occupied := CursorStore{Path: target}
	if err := occupied.saveState(cursorState{Version: repositoryevent.ContractVersion, Cursor: "cursor-1"}); err == nil {
		t.Fatal("saveState renamed over a populated directory")
	}

	// A pending acknowledgement carrying a valid cursor but no event IDs is
	// rejected before any file is touched.
	emptyIDs := cursorState{
		Version:    repositoryevent.ContractVersion,
		Cursor:     "cursor-1",
		PendingAck: &repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: "cursor-2"},
	}
	if err := store.saveState(emptyIDs); err == nil {
		t.Fatal("saveState accepted an acknowledgement without event IDs")
	}
}

// TestSdCovReceiveOnceReportsCursorPersistenceFailures forces each cursor
// write to fail by removing write permission from its directory (and, for the
// final acknowledged write, by doing so from inside the acknowledgement
// callback, which runs between the two writes).
func TestSdCovReceiveOnceReportsCursorPersistenceFailures(t *testing.T) {
	t.Parallel()
	sdCovSkipIfPrivileged(t)

	t.Run("empty poll cursor", func(t *testing.T) {
		t.Parallel()
		directory := t.TempDir()
		if err := os.Chmod(directory, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
		source := &sdCovSource{}
		source.poll = func(string, int, time.Duration) (repositoryevent.PollResponse, error) {
			return repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: "cursor-1", NextCursor: "cursor-2"}, nil
		}
		receiver := Receiver{Source: source, Queue: &enqueueFake{}, Cursor: CursorStore{Path: filepath.Join(directory, "cursor.json")}}
		if err := receiver.ReceiveOnce(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "persist empty repository event cursor") {
			t.Fatalf("empty poll persist error = %v", err)
		}
	})

	t.Run("pending acknowledgement", func(t *testing.T) {
		t.Parallel()
		directory := t.TempDir()
		if err := os.Chmod(directory, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
		source := &sdCovSource{}
		source.poll = func(string, int, time.Duration) (repositoryevent.PollResponse, error) {
			return repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: "cursor-1", NextCursor: "cursor-2", Events: []repositoryevent.Event{receiverEvent("event-pending")}}, nil
		}
		receiver := Receiver{Source: source, Queue: &enqueueFake{}, Cursor: CursorStore{Path: filepath.Join(directory, "cursor.json")}}
		if err := receiver.ReceiveOnce(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "persist pending repository event acknowledgement") {
			t.Fatalf("pending ack persist error = %v", err)
		}
	})

	t.Run("resumed acknowledgement", func(t *testing.T) {
		t.Parallel()
		directory := t.TempDir()
		cursor := CursorStore{Path: filepath.Join(directory, "cursor.json")}
		if err := cursor.saveState(cursorState{
			Version:    repositoryevent.ContractVersion,
			Cursor:     "cursor-1",
			PendingAck: &repositoryevent.AckRequest{Version: repositoryevent.ContractVersion, Cursor: "cursor-2", EventIDs: []string{"event-1"}},
		}); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
		source := &sdCovSource{poll: sdCovEmptyPoll}
		receiver := Receiver{Source: source, Queue: &enqueueFake{}, Cursor: cursor}
		if err := receiver.ReceiveOnce(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "persist resumed repository event acknowledgement") {
			t.Fatalf("resumed ack persist error = %v", err)
		}
	})

	t.Run("acknowledged cursor", func(t *testing.T) {
		t.Parallel()
		directory := t.TempDir()
		cursor := CursorStore{Path: filepath.Join(directory, "cursor.json")}
		if err := cursor.Save("cursor-1"); err != nil {
			t.Fatal(err)
		}
		source := &sdCovSource{}
		source.poll = func(string, int, time.Duration) (repositoryevent.PollResponse, error) {
			return repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: "cursor-1", NextCursor: "cursor-2", Events: []repositoryevent.Event{receiverEvent("event-acked-persist")}}, nil
		}
		source.ack = func(request repositoryevent.AckRequest) (repositoryevent.AckResponse, error) {
			if err := os.Chmod(directory, 0o500); err != nil {
				return repositoryevent.AckResponse{}, err
			}
			return repositoryevent.AckResponse{Version: repositoryevent.ContractVersion, Cursor: request.Cursor}, nil
		}
		t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
		receiver := Receiver{Source: source, Queue: &enqueueFake{}, Cursor: cursor}
		if err := receiver.ReceiveOnce(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "persist acknowledged repository event cursor") {
			t.Fatalf("acknowledged persist error = %v", err)
		}
		// The pending acknowledgement must have been durably recorded before
		// the acknowledgement was sent, even though the final write failed.
		raw, err := os.ReadFile(cursor.Path)
		if err != nil {
			t.Fatal(err)
		}
		var state cursorState
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		if state.PendingAck == nil || state.PendingAck.Cursor != "cursor-2" {
			t.Fatalf("durable pending ack = %+v", state)
		}
	})
}
