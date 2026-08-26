package sessionpark

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
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

func TestStoreRejectsUnsafeParkedOwnerEvidence(t *testing.T) {
	bundle := testBundle(t)
	bundle.Worktrees[0].OwnerEventID = "../newer-owner"
	if _, err := NewStore(t.TempDir()).Create(bundle); err == nil {
		t.Fatal("unsafe parked owner event identity accepted")
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

func TestSourceStoreRemoteEnvelopeAndReceiptCrashRetry(t *testing.T) {
	store := NewStore(t.TempDir())
	bundle := remoteTestBundle(t)
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	lock, err := store.Acquire(context.Background(), bundle.ParkedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	firstAt := time.Unix(100, 0).UTC()
	first, err := store.PrepareRemoteUnderLock(lock, "target", "", firstAt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PrepareRemoteUnderLock(lock, "target", "", firstAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replay || !first.Envelope.Request.CreatedAt.Equal(firstAt) || !first.Envelope.Request.CreatedAt.Equal(second.Envelope.Request.CreatedAt) ||
		!strings.EqualFold(string(first.Raw), string(second.Raw)) || first.Digest != second.Digest {
		t.Fatalf("remote admission retry changed exact bytes: first=%#v second=%#v", first, second)
	}
	receipt := validRemoteReceipt(t, first)
	if err := store.SaveRemoteReceiptUnderLock(lock, first, receipt); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	interrupted, err := store.Load(bundle.ParkedSessionID)
	if err != nil || interrupted.Status != StatusParked {
		t.Fatalf("receipt-before-event boundary = %#v, err=%v", interrupted, err)
	}
	lock, err = store.Acquire(context.Background(), bundle.ParkedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	replayed, err := store.PrepareRemoteUnderLock(lock, "target", "", firstAt.Add(2*time.Hour))
	if err != nil || !replayed.Replay || replayed.Digest != first.Digest {
		t.Fatalf("replayed admission = %#v, err=%v", replayed, err)
	}
	state, err := store.FinalizeRemoteUnderLock(lock, replayed, time.Unix(200, 0))
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != StatusResumed || state.Successor == nil || state.Successor.WBSessionID != receipt.SuccessorWBSessionID || state.RemoteReceipt == nil {
		t.Fatalf("final source state = %#v", state)
	}
}

func TestSourceStoreRejectsTraversalSymlinkAndNonPrivateBundle(t *testing.T) {
	t.Run("traversal", func(t *testing.T) {
		store := NewStore(t.TempDir())
		for _, id := range []string{".", "..", "park-../escape", "/park-absolute"} {
			if _, err := store.Load(id); err == nil {
				t.Fatalf("Load(%q) accepted", id)
			}
		}
	})
	t.Run("aggregate symlink", func(t *testing.T) {
		root := t.TempDir()
		store := NewStore(root)
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "park-symlink")); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load("park-symlink"); err == nil {
			t.Fatal("aggregate symlink followed")
		}
	})
	t.Run("store root symlink", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "parked-sessions")
		if err := os.Symlink(t.TempDir(), root); err != nil {
			t.Fatal(err)
		}
		if _, err := NewStore(root).Create(testBundle(t)); err == nil {
			t.Fatal("source store root symlink followed")
		}
	})
	t.Run("bundle symlink", func(t *testing.T) {
		store := NewStore(t.TempDir())
		bundle := testBundle(t)
		if _, err := store.Create(bundle); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(store.Root, bundle.ParkedSessionID)
		if err := os.Rename(filepath.Join(dir, sourceBundleFileName), filepath.Join(dir, "real-bundle.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("real-bundle.json", filepath.Join(dir, sourceBundleFileName)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(bundle.ParkedSessionID); err == nil {
			t.Fatal("bundle symlink followed")
		}
	})
	t.Run("bundle mode", func(t *testing.T) {
		store := NewStore(t.TempDir())
		bundle := testBundle(t)
		if _, err := store.Create(bundle); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(store.Root, bundle.ParkedSessionID, sourceBundleFileName), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(bundle.ParkedSessionID); err == nil {
			t.Fatal("non-private bundle accepted")
		}
	})
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "continuation symlink", mutate: func(t *testing.T, path string) {
			outside := filepath.Join(t.TempDir(), "outside-continuation")
			if err := os.WriteFile(outside, []byte(testBundle(t).Continuation), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "continuation mode", mutate: func(t *testing.T, path string) {
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(t.TempDir())
			bundle := testBundle(t)
			if _, err := store.Create(bundle); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, filepath.Join(store.Root, bundle.ParkedSessionID, sourceContinuationFileName))
			lock, err := store.Acquire(context.Background(), bundle.ParkedSessionID)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lock.Close() }()
			if _, err := store.ContinuationPathUnderLock(lock); err == nil {
				t.Fatal("unsafe continuation artifact accepted")
			}
		})
	}
	t.Run("unexpected event artifact", func(t *testing.T) {
		store := NewStore(t.TempDir())
		bundle := testBundle(t)
		if _, err := store.Create(bundle); err != nil {
			t.Fatal(err)
		}
		events := filepath.Join(store.Root, bundle.ParkedSessionID, sourceEventsDirName)
		if err := os.WriteFile(filepath.Join(events, "rogue.json"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(bundle.ParkedSessionID); err == nil {
			t.Fatal("unexpected source event artifact accepted")
		}
	})
}

func TestSourceLockRetainAndCloseRaceNeverReturnsUnvalidatedCapability(t *testing.T) {
	store := NewStore(t.TempDir())
	bundle := testBundle(t)
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	lock, err := store.Acquire(context.Background(), bundle.ParkedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := EncodeBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	digest := string(sessionmove.DigestBytes(raw))
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		for range 100 {
			retained, retainErr := lock.RetainSessionDir(store.Root, bundle.ParkedSessionID, digest)
			if retainErr == nil {
				if retained == nil {
					t.Error("successful retain returned nil capability")
				} else {
					_ = retained.Close()
				}
			}
		}
	}()
	go func() {
		defer group.Done()
		_ = lock.Close()
	}()
	group.Wait()
	if lock.HeldForSession(store.Root, bundle.ParkedSessionID, digest) {
		t.Fatal("closed source lock still authenticates")
	}
}

func TestSourceStoreRefusesSecondTargetAfterDurableResume(t *testing.T) {
	store := NewStore(t.TempDir())
	bundle := remoteTestBundle(t)
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	lock, err := store.Acquire(context.Background(), bundle.ParkedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.PrepareRemoteUnderLock(lock, "target-a", "", time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRemoteReceiptUnderLock(lock, first, validRemoteReceipt(t, first)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeRemoteUnderLock(lock, first, time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	lock, err = store.Acquire(context.Background(), bundle.ParkedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if _, err := store.PrepareRemoteUnderLock(lock, "target-b", "", time.Unix(300, 0)); err == nil {
		t.Fatal("resumed source admitted a competing target")
	}
	if _, err := os.Stat(filepath.Join(store.Root, bundle.ParkedSessionID, sourceEnvelopeName("target-b"))); !os.IsNotExist(err) {
		t.Fatalf("losing target mutated source aggregate: %v", err)
	}
}

func remoteTestBundle(t *testing.T) Bundle {
	t.Helper()
	bundle := testBundle(t)
	bundle.ParkedSessionID = "park-remote-source"
	bundle.Worktrees = []Worktree{{
		Repository: "acme/app", RepositoryRemote: "https://github.com/acme/app.git", WorktreeDir: "/tmp/app",
		Branch: "feature/app", Head: strings.Repeat("a", 40), RemoteHead: strings.Repeat("a", 40),
		WorkLogReference: "worklog:effort/run/" + strings.Repeat("b", 64), OwnerEventID: strings.Repeat("c", 64),
	}}
	return bundle
}

func validRemoteReceipt(t *testing.T, admission RemoteAdmission) Receipt {
	t.Helper()
	request := admission.Envelope.Request
	members := make([]ReceiptMember, len(request.Members))
	for index, member := range request.Members {
		reference, err := TargetWorkLogReference(request, admission.Digest, member)
		if err != nil {
			t.Fatal(err)
		}
		members[index] = ReceiptMember{MemberID: member.MemberID, Repository: member.Repository, TargetPath: "/tmp/target-" + member.MemberID,
			Pin: MemberPin(request.ResumeID, member.MemberID), Commit: member.Commit, TargetWorkLogReference: reference}
	}
	return Receipt{SchemaVersion: ReceiptSchemaVersion, ResumeID: request.ResumeID, RequestDigest: admission.Digest,
		ParkedSessionID: request.ParkedSessionID, SuccessorWBSessionID: request.SuccessorWBSessionID,
		PredecessorWBSessionID: request.PredecessorWBSessionID, TargetMachine: request.TargetMachine,
		TmuxName: "wb-session-" + request.SuccessorWBSessionID, Runtime: request.SourceRuntime, Model: request.SourceModel,
		AttemptID: "000001-11111111111111111111111111111111", AttemptIndex: 1, PID: os.Getpid(), StartedAt: time.Unix(150, 0).UTC(), Members: members}
}
