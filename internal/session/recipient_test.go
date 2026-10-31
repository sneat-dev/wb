package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecipientBarrierBlocksRegistrationPickupAndParkUntilSubmissionReturns(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"register", "pickup", "park"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			root, caller, peer := sharedScopeFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			guardDone := make(chan error, 1)
			go func() {
				guardDone <- WithActiveRecipient(context.Background(), root, caller, func() error {
					close(entered)
					<-release
					current, live, err := LookupActiveRecordExact(root, caller)
					if err != nil {
						return err
					}
					if !live || current != caller {
						return errors.New("recipient changed while submission held the barrier")
					}
					return nil
				})
			}()
			select {
			case <-entered:
			case err := <-guardDone:
				t.Fatalf("barrier did not enter: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("barrier acquisition timed out")
			}
			started, mutated := make(chan struct{}), make(chan error, 1)
			go func() {
				close(started)
				switch operation {
				case "register":
					next := caller
					next.Model = "new-metadata"
					_, err := Register(root, next)
					mutated <- err
				case "pickup":
					next := caller
					next.WBSessionID = "wbs-next-recipient"
					next.PredecessorWBSessionID = caller.WBSessionID
					next.HandoffID = "park-recipient"
					next.StartedAt = caller.StartedAt.Add(time.Second)
					mutated <- WithCurrentTransition(root, caller, next, func(commit func() error) error { return commit() })
				case "park":
					_, err := MarkRecordParked(root, caller, "park-recipient")
					mutated <- err
				}
			}()
			<-started
			select {
			case err := <-mutated:
				close(release)
				t.Fatalf("%s crossed the retained submission barrier: %v", operation, err)
			case <-time.After(30 * time.Millisecond):
			}
			close(release)
			select {
			case err := <-guardDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("submission did not return")
			}
			select {
			case err := <-mutated:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("registry writer remained blocked")
			}
			calls := 0
			if err := WithActiveRecipient(context.Background(), root, caller, func() error { calls++; return nil }); err == nil || calls != 0 {
				t.Fatalf("stale recipient admitted: %v calls=%d", err, calls)
			}
			if _, live, err := LookupActiveRecordExact(root, peer); err != nil || !live {
				t.Fatalf("foreign chat affected: %v", err)
			}
		})
	}
}

func TestRecipientBarrierReturnsFailuresWithoutSubmittingOrRetainingTheLock(t *testing.T) {
	t.Parallel()
	root, caller, _ := sharedScopeFixture(t)
	failure := errors.New("submission failed after intent")
	if err := WithActiveRecipient(context.Background(), root, caller, func() error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("callback failure lost: %v", err)
	}
	if _, err := Register(root, caller); err != nil {
		t.Fatalf("callback failure retained the barrier: %v", err)
	}
	if err := WithActiveRecipient(context.Background(), root, caller, nil); err == nil {
		t.Fatal("nil submission accepted")
	}
	if err := WithActiveRecipient(context.Background(), root, Record{}, func() error { t.Fatal("invalid PID submitted"); return nil }); err == nil {
		t.Fatal("invalid PID accepted")
	}
	bad := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(bad, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WithActiveRecipient(context.Background(), bad, caller, func() error { t.Fatal("bad directory submitted"); return nil }); err == nil {
		t.Fatal("bad directory accepted")
	}
	if err := writeRecordAtomic(root, caller, []byte("not-json")); err != nil {
		t.Fatal(err)
	}
	if err := WithActiveRecipient(context.Background(), root, caller, func() error { t.Fatal("corrupt record submitted"); return nil }); err == nil {
		t.Fatal("corrupt record accepted")
	}
}

func TestRegistryLockContextCancellationAndNativeFailureLeaveNoWaitingOwner(t *testing.T) {
	t.Parallel()
	file, err := os.CreateTemp(t.TempDir(), "closed-lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := lockRegistryFileContext(context.Background(), file); err == nil {
		t.Fatal("closed native lock descriptor accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lockRegistryFileContext(ctx, file); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation did not precede lock attempt: %v", err)
	}
}
