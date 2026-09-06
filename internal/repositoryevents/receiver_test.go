package repositoryevents

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
)

type sourceFake struct {
	response repositoryevent.PollResponse
	acks     []repositoryevent.AckRequest
	ackErr   error
}

func TestQueueCoalescesQueuedDefaultBranchEventsDurably(t *testing.T) {
	root := t.TempDir()
	queue, err := NewQueue(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	queue.now = func() time.Time { return now }
	first := receiverEvent("event-1")
	first.TargetSHA = "1111111111111111111111111111111111111111"
	second := receiverEvent("event-2")
	second.TargetSHA = "2222222222222222222222222222222222222222"
	if _, err := queue.Enqueue(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Enqueue(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if queue.jobs[first.ID].State != "superseded" || queue.jobs[first.ID].Progress != "superseded by event-2" || queue.jobs[second.ID].State != "queued" {
		t.Fatalf("coalesced jobs = %+v / %+v", queue.jobs[first.ID], queue.jobs[second.ID])
	}
	restarted, err := NewQueue(root)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.jobs[first.ID].State != "superseded" || restarted.jobs[second.ID].State != "queued" {
		t.Fatalf("restarted jobs = %+v / %+v", restarted.jobs[first.ID], restarted.jobs[second.ID])
	}
}

type concurrencyProcessor struct {
	mu             sync.Mutex
	started        chan string
	release        <-chan struct{}
	activeByRepo   map[string]int
	maxByRepo      map[string]int
	activeTotal    int
	maxActiveTotal int
}

func (processor *concurrencyProcessor) Process(_ context.Context, event repositoryevent.Event) (string, error) {
	processor.mu.Lock()
	processor.activeByRepo[event.Repository]++
	processor.activeTotal++
	if processor.activeByRepo[event.Repository] > processor.maxByRepo[event.Repository] {
		processor.maxByRepo[event.Repository] = processor.activeByRepo[event.Repository]
	}
	if processor.activeTotal > processor.maxActiveTotal {
		processor.maxActiveTotal = processor.activeTotal
	}
	processor.mu.Unlock()
	processor.started <- event.ID
	<-processor.release
	processor.mu.Lock()
	processor.activeByRepo[event.Repository]--
	processor.activeTotal--
	processor.mu.Unlock()
	return "pulled", nil
}

func TestQueueRunsDifferentRepositoriesInParallelButExcludesSameRepository(t *testing.T) {
	queue, err := NewQueue(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	queue.workers = 3
	queue.acquire = func(context.Context) (func(), error) { return func() {}, nil }
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	queue.now = func() time.Time { return now }
	first := receiverEvent("event-a1")
	second := receiverEvent("event-a2")
	second.Ref = "refs/heads/release"
	other := receiverEvent("event-b1")
	other.Repository = "github.com/acme/other"
	for _, event := range []repositoryevent.Event{first, second, other} {
		if _, err := queue.Enqueue(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	release := make(chan struct{})
	processor := &concurrencyProcessor{started: make(chan string, 3), release: release, activeByRepo: map[string]int{}, maxByRepo: map[string]int{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go queue.Run(ctx, processor, nil)
	started := map[string]bool{}
	for len(started) < 2 {
		select {
		case id := <-processor.started:
			started[id] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("parallel starts = %+v", started)
		}
	}
	if !started["event-a1"] || !started["event-b1"] || started["event-a2"] {
		t.Fatalf("initial starts = %+v", started)
	}
	close(release)
	select {
	case id := <-processor.started:
		if id != "event-a2" {
			t.Fatalf("third start = %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("same-repository follow-up did not start")
	}
	processor.mu.Lock()
	defer processor.mu.Unlock()
	if processor.maxByRepo[first.Repository] != 1 || processor.maxActiveTotal < 2 {
		t.Fatalf("max same repo=%d total=%d", processor.maxByRepo[first.Repository], processor.maxActiveTotal)
	}
}

func TestQueueOrdersOldPushRenameAndNewPushAcrossCaseInsensitiveAliases(t *testing.T) {
	queue, err := NewQueue(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldPush := receiverEvent("event-old-push")
	oldPush.Repository = "github.com/Acme/Old"
	rename := repositoryevent.Event{Version: repositoryevent.ContractVersion, ID: "event-rename", Repository: "github.com/Acme/New", PreviousRepository: "github.com/acme/old", Ref: "refs/heads/main", Reason: repositoryevent.ReasonRepositoryRenamed}
	newPush := receiverEvent("event-new-push")
	newPush.Repository = "github.com/acme/new"
	for _, event := range []repositoryevent.Event{oldPush, rename, newPush} {
		if _, err := queue.Enqueue(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	first := queue.claimNext()
	if first == nil || first.Event.ID != oldPush.ID {
		t.Fatalf("first claim = %+v", first)
	}
	if blocked := queue.claimNext(); blocked != nil {
		t.Fatalf("claimed across old-name alias: %+v", blocked)
	}
	queue.finish(oldPush.ID, "pulled", nil)
	second := queue.claimNext()
	if second == nil || second.Event.ID != rename.ID {
		t.Fatalf("second claim = %+v", second)
	}
	if blocked := queue.claimNext(); blocked != nil {
		t.Fatalf("claimed across rename aliases: %+v", blocked)
	}
	queue.finish(rename.ID, "relocated", nil)
	third := queue.claimNext()
	if third == nil || third.Event.ID != newPush.ID {
		t.Fatalf("third claim = %+v", third)
	}
}

func (source *sourceFake) PollRepositoryEvents(context.Context, string, int, time.Duration) (repositoryevent.PollResponse, error) {
	return source.response, nil
}
func (source *sourceFake) AckRepositoryEvents(_ context.Context, request repositoryevent.AckRequest) (repositoryevent.AckResponse, error) {
	source.acks = append(source.acks, request)
	if source.ackErr != nil {
		return repositoryevent.AckResponse{}, source.ackErr
	}
	return repositoryevent.AckResponse{Version: repositoryevent.ContractVersion, Cursor: request.Cursor}, nil
}

func TestReceiveOnceResumesDurablePendingAcknowledgementBeforePolling(t *testing.T) {
	source := &sourceFake{
		response: repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: "", NextCursor: "cursor-2", Events: []repositoryevent.Event{receiverEvent("event-1")}},
		ackErr:   errors.New("provider unavailable"),
	}
	cursor := CursorStore{Path: filepath.Join(t.TempDir(), "cursor.json")}
	receiver := Receiver{Source: source, Queue: &enqueueFake{}, Cursor: cursor}
	if err := receiver.ReceiveOnce(context.Background(), 0); err == nil {
		t.Fatal("ack failure was accepted")
	}
	state, err := cursor.loadState()
	if err != nil || state.PendingAck == nil || state.PendingAck.Cursor != "cursor-2" {
		t.Fatalf("durable pending ack = %+v, %v", state, err)
	}
	source.ackErr = nil
	source.response = repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: "cursor-2", NextCursor: "cursor-2", Events: nil}
	if err := receiver.ReceiveOnce(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	state, err = cursor.loadState()
	if err != nil || state.PendingAck != nil || state.Cursor != "cursor-2" || len(source.acks) != 2 {
		t.Fatalf("resumed state = %+v, acks=%d, err=%v", state, len(source.acks), err)
	}
}

type enqueueFake struct {
	ids    []string
	failOn string
}

func (queue *enqueueFake) Enqueue(_ context.Context, event repositoryevent.Event) (string, error) {
	if event.ID == queue.failOn {
		return "", errors.New("disk full")
	}
	queue.ids = append(queue.ids, event.ID)
	return event.ID, nil
}

func receiverEvent(id string) repositoryevent.Event {
	return repositoryevent.Event{Version: repositoryevent.ContractVersion, ID: id, Repository: "github.com/acme/app", Ref: "refs/heads/main", Reason: repositoryevent.ReasonDefaultBranchUpdated}
}

func TestReceiveOnceAcknowledgesOnlyAfterEveryDurableEnqueue(t *testing.T) {
	source := &sourceFake{response: repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: "", NextCursor: "cursor-2", Events: []repositoryevent.Event{receiverEvent("event-1"), receiverEvent("event-2")}}}
	cursor := CursorStore{Path: filepath.Join(t.TempDir(), "cursor.json")}
	failing := &enqueueFake{failOn: "event-2"}
	receiver := Receiver{Source: source, Queue: failing, Cursor: cursor}
	if err := receiver.ReceiveOnce(context.Background(), 0); err == nil {
		t.Fatal("enqueue failure was accepted")
	}
	if len(source.acks) != 0 {
		t.Fatalf("acknowledged partial batch: %+v", source.acks)
	}
	if got, err := cursor.Load(); err != nil || got != "" {
		t.Fatalf("cursor after failed enqueue = %q, %v", got, err)
	}

	complete := &enqueueFake{}
	receiver.Queue = complete
	if err := receiver.ReceiveOnce(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if len(source.acks) != 1 || len(source.acks[0].EventIDs) != 2 || source.acks[0].Cursor != "cursor-2" {
		t.Fatalf("acks = %+v", source.acks)
	}
	if got, err := cursor.Load(); err != nil || got != "cursor-2" {
		t.Fatalf("cursor = %q, %v", got, err)
	}
}

func TestQueueDeduplicatesDurablyAndRecoversRunningJob(t *testing.T) {
	root := t.TempDir()
	queue, err := NewQueue(root)
	if err != nil {
		t.Fatal(err)
	}
	event := receiverEvent("event-durable")
	if _, err := queue.Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	queue.mu.Lock()
	queue.jobs[event.ID].State = "running"
	if err := queue.persist(queue.jobs[event.ID]); err != nil {
		queue.mu.Unlock()
		t.Fatal(err)
	}
	queue.mu.Unlock()

	restarted, err := NewQueue(root)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.jobs[event.ID].State != "queued" {
		t.Fatalf("recovered state = %q", restarted.jobs[event.ID].State)
	}
	if _, err := restarted.Enqueue(context.Background(), event); err != nil || len(restarted.jobs) != 1 {
		t.Fatalf("duplicate enqueue = %v, jobs=%d", err, len(restarted.jobs))
	}
	changed := event
	changed.TargetSHA = "0123456789abcdef0123456789abcdef01234567"
	if _, err := restarted.Enqueue(context.Background(), changed); err == nil {
		t.Fatal("conflicting duplicate event was accepted")
	}
}
