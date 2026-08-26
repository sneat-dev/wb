package sessionpark

import (
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
)

func testBundle(t *testing.T) Bundle {
	t.Helper()
	return Bundle{SchemaVersion: SchemaVersion, ParkedSessionID: "park-test", Source: session.Record{PID: 123, WBSessionID: "wbs-source", Machine: "laptop", Runtime: "codex", StartedAt: time.Unix(10, 0).UTC()}, Continuation: "continue from checkpoint", ParkedAt: time.Unix(20, 0).UTC(), Worktrees: []Worktree{{Repository: "acme/app", WorktreeDir: "/tmp/app-a", Branch: "feature/a", Head: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Dirty: true}, {Repository: "acme/app", WorktreeDir: "/tmp/app-b", Branch: "feature/b", Head: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", RemoteHead: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}}
}

func TestStorePreservesMultipleWorktreesAndDirtyEvidence(t *testing.T) {
	store := NewStore(t.TempDir())
	bundle := testBundle(t)
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(bundle.ParkedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != StatusParked || len(state.Bundle.Worktrees) != 2 || !state.Bundle.Worktrees[0].Dirty {
		t.Fatalf("state = %#v", state)
	}
}

func TestStoreResumeLineageAndIdenticalRetry(t *testing.T) {
	store := NewStore(t.TempDir())
	bundle := testBundle(t)
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	successor := session.Record{PID: 456, WBSessionID: "wbs-successor", PredecessorWBSessionID: bundle.Source.WBSessionID, StartedAt: time.Unix(30, 0).UTC()}
	first, err := store.Resume(bundle.ParkedSessionID, successor, time.Unix(40, 0))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Resume(bundle.ParkedSessionID, session.Record{PID: 999, WBSessionID: "wbs-other"}, time.Unix(50, 0))
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != StatusResumed || second.Status != StatusResumed || second.Successor == nil || second.Successor.WBSessionID != successor.WBSessionID {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if len(second.Events) != 1 {
		t.Fatalf("retry appended duplicate event: %#v", second.Events)
	}
}

func TestStoreRejectsOversizeContinuation(t *testing.T) {
	bundle := testBundle(t)
	bundle.Continuation = string(make([]byte, MaxContinuationBytes+1))
	if _, err := NewStore(t.TempDir()).Create(bundle); err == nil {
		t.Fatal("oversize continuation accepted")
	}
}

func TestStoreResumeConcurrentSuccessorsHasOneWinner(t *testing.T) {
	store := NewStore(t.TempDir())
	bundle := testBundle(t)
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan State, 2)
	errs := make(chan error, 2)
	for i, id := range []string{"wbs-one", "wbs-two"} {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			result, err := store.Resume(bundle.ParkedSessionID, session.Record{PID: 500 + i, WBSessionID: id}, time.Unix(int64(50+i), 0))
			results <- result
			errs <- err
		}(i, id)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err := store.Load(bundle.ParkedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Events) != 1 || state.Successor == nil {
		t.Fatalf("state = %#v", state)
	}
}

func TestStoreFindBySourceRepairsLifecycleProjectionWithoutNewID(t *testing.T) {
	store := NewStore(t.TempDir())
	bundle := testBundle(t)
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	found, ok, err := store.FindBySource(bundle.Source.WBSessionID)
	if err != nil || !ok || found.ParkedSessionID != bundle.ParkedSessionID {
		t.Fatalf("found=%#v ok=%t err=%v", found, ok, err)
	}
}
