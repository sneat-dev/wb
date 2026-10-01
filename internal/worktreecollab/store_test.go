package worktreecollab

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreesecure"
)

func TestStorePersistsPrivateStateAndRefusesRebinding(t *testing.T) {
	t.Parallel()
	home := privateTestHome(t)
	store := NewStore(home)
	checkout := testCheckout()
	_, err := store.WithLocked(context.Background(), checkout, func(state *State, found bool) error {
		if found {
			t.Fatal("fresh state already existed")
		}
		return state.Take(TakeRequest{Caller: "owner", ExpectedOwner: NoOwner, At: testNow})
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, directoryName, checkout.ID+".json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private snapshot mode = %v, %v", info, err)
	}
	state, err := store.WithLocked(context.Background(), checkout, func(state *State, found bool) error {
		if !found || state.Owner != "owner" {
			t.Fatalf("reload = %+v, found %t", state, found)
		}
		return state.Join("peer", testNow)
	})
	if err != nil || len(state.Members) != 2 {
		t.Fatalf("joined snapshot = %+v, %v", state, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.WithLocked(context.Background(), checkout, func(*State, bool) error { return errors.New("refusal") })
	if err == nil {
		t.Fatal("operation failure accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("failed operation changed snapshot: %v", err)
	}
	_, err = store.WithLocked(context.Background(), checkout, func(*State, bool) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	after, err = os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("read-only operation changed snapshot: %v", err)
	}
	rebound := checkout
	rebound.GitDir = "/another/gitdir"
	_, err = store.WithLocked(context.Background(), rebound, func(*State, bool) error { return nil })
	if err == nil {
		t.Fatal("different checkout accepted existing snapshot")
	}
}

type testLock struct {
	owned     bool
	lockErr   error
	unlockErr error
}

func (lock *testLock) TryLockContext(context.Context, time.Duration) (bool, error) {
	return lock.owned, lock.lockErr
}
func (lock *testLock) Unlock() error { return lock.unlockErr }

func privateTestHome(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, ".wb")
}

func TestStoreBoundaryFailures(t *testing.T) {
	t.Parallel()
	boom := errors.New("boundary failure")
	checkout := testCheckout()
	base := NewStore(privateTestHome(t))
	for _, tc := range []struct {
		name   string
		change func(*Store)
		apply  func(*State, bool) error
		want   string
	}{
		{"missing ports", func(s *Store) { s.Ports.Read = nil }, func(*State, bool) error { return nil }, "boundaries"},
		{"open home", func(s *Store) { s.Ports.OpenHome = func(string, bool) (*os.File, error) { return nil, boom } }, func(*State, bool) error { return nil }, "home"},
		{"open child", func(s *Store) {
			s.Ports.OpenChild = func(*os.File, string, bool, worktreesecure.ValidSegment) (*os.File, error) { return nil, boom }
		}, func(*State, bool) error { return nil }, "directory"},
		{"lock failure", func(s *Store) { s.Ports.Lock = func(string) lockFile { return &testLock{lockErr: boom} } }, func(*State, bool) error { return nil }, "lock"},
		{"lock not acquired", func(s *Store) { s.Ports.Lock = func(string) lockFile { return &testLock{} } }, func(*State, bool) error { return nil }, "not acquired"},
		{"read failure", func(s *Store) { s.Ports.Read = func(*os.File, string, any) error { return boom } }, func(*State, bool) error { return nil }, "read"},
		{"corrupt snapshot", func(s *Store) {
			s.Ports.Read = func(_ *os.File, _ string, value any) error { value.(*State).Owner = "missing"; return nil }
		}, func(*State, bool) error { return nil }, "owner"},
		{"invalid transition", nil, func(s *State, _ bool) error { s.Revision++; return nil }, "owner"},
		{"write failure", func(s *Store) { s.Ports.Write = func(*os.File, string, any, os.FileMode) error { return boom } }, func(s *State, _ bool) error {
			return s.Take(TakeRequest{Caller: "owner", ExpectedOwner: NoOwner, At: testNow})
		}, "publish"},
		{"unlock failure", func(s *Store) {
			s.Ports.Lock = func(string) lockFile { return &testLock{owned: true, unlockErr: boom} }
		}, func(*State, bool) error { return nil }, "release"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := base
			store.Home = privateTestHome(t)
			if tc.change != nil {
				tc.change(&store)
			}
			if _, err := store.WithLocked(context.Background(), checkout, tc.apply); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("boundary error = %v, want %q", err, tc.want)
			}
		})
	}
	if _, err := base.WithLocked(context.Background(), Checkout{}, func(*State, bool) error { return nil }); err == nil {
		t.Fatal("invalid checkout accepted")
	}
	if _, err := base.WithLocked(context.Background(), checkout, nil); err == nil {
		t.Fatal("nil transaction accepted")
	}
}

func TestStoreRefusesDirectoryReplacementAroundLockAndPublication(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"before-lock", "after-lock", "before-write", "after-write"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			home := privateTestHome(t)
			store := NewStore(home)
			directory := filepath.Join(home, directoryName)
			replace := func() {
				t.Helper()
				if err := os.Rename(directory, directory+".detached"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			switch phase {
			case "before-lock":
				original := store.Ports.OpenChild
				store.Ports.OpenChild = func(parent *os.File, name string, create bool, valid worktreesecure.ValidSegment) (*os.File, error) {
					opened, err := original(parent, name, create, valid)
					if err == nil {
						replace()
					}
					return opened, err
				}
			case "after-lock":
				store.Ports.Lock = func(string) lockFile { replace(); return &testLock{owned: true} }
			case "after-write":
				original := store.Ports.Write
				store.Ports.Write = func(dir *os.File, name string, value any, mode os.FileMode) error {
					if err := original(dir, name, value, mode); err != nil {
						return err
					}
					replace()
					return nil
				}
			}
			_, err := store.WithLocked(context.Background(), testCheckout(), func(state *State, _ bool) error {
				if phase == "before-write" {
					replace()
				}
				return state.Take(TakeRequest{Caller: "owner", ExpectedOwner: NoOwner, At: testNow})
			})
			if err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatalf("replaced directory accepted: %v", err)
			}
			if _, err := os.Stat(filepath.Join(directory, testCheckout().ID+".json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("replacement received snapshot: %v", err)
			}
		})
	}
}

func TestStoreDirectoryRecheckReportsMissingPathAndClosedHandle(t *testing.T) {
	t.Parallel()
	home := privateTestHome(t)
	store := NewStore(home)
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	if err := store.directoryMatches(held, held); err == nil || !strings.Contains(err.Error(), "recheck") {
		t.Fatalf("missing child path = %v", err)
	}
	if err := os.Mkdir(filepath.Join(home, directoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	closed, err := os.Open(filepath.Join(home, directoryName))
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.directoryMatches(held, closed); err == nil || !strings.Contains(err.Error(), "inspect held") {
		t.Fatalf("closed held handle = %v", err)
	}
}
