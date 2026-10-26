package worktreecollab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestStoreLoadIsReadOnlyAndCorroboratesSnapshot(t *testing.T) {
	t.Parallel()
	store := NewStore(privateTestHome(t))
	checkout := testCheckout()
	store.Ports.Write, store.Ports.Lock = nil, nil // inspection needs neither capability
	if state, found, err := store.Load(checkout); err != nil || found || state.Checkout != checkout {
		t.Fatalf("missing home = %+v, %t, %v", state, found, err)
	}
	if _, err := os.Stat(store.Home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection created home: %v", err)
	}
	if err := os.Mkdir(store.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Load(checkout); err != nil || found {
		t.Fatalf("missing coordination directory = %t, %v", found, err)
	}
	path := filepath.Join(store.Home, directoryName)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection created coordination directory: %v", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Load(checkout); err != nil || found {
		t.Fatalf("missing snapshot = %t, %v", found, err)
	}
	if _, err := os.Stat(filepath.Join(path, checkout.ID+".lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection created lock: %v", err)
	}
	writer := NewStore(store.Home)
	if _, err := writer.WithLocked(context.Background(), checkout, func(state *State, _ bool) error {
		return state.Take(TakeRequest{Caller: "owner", ExpectedOwner: NoOwner, At: testNow})
	}); err != nil {
		t.Fatal(err)
	}
	if state, found, err := store.Load(checkout); err != nil || !found || state.Owner != "owner" {
		t.Fatalf("published snapshot = %+v, %t, %v", state, found, err)
	}
	rebound := checkout
	rebound.GitDir = "/another/gitdir"
	if _, _, err := store.Load(rebound); err == nil {
		t.Fatal("rebound checkout accepted")
	}
}

func TestStoreLoadRefusesFaultsAndDirectoryReplacement(t *testing.T) {
	t.Parallel()
	checkout := testCheckout()
	base := NewStore(privateTestHome(t))
	if err := os.Mkdir(base.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base.Home, directoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("inspection boundary")
	for _, tc := range []struct {
		name   string
		change func(*Store)
	}{
		{"missing boundary", func(s *Store) { s.Ports.Read = nil }},
		{"open home", func(s *Store) { s.Ports.OpenHome = func(string, bool) (*os.File, error) { return nil, boom } }},
		{"open child", func(s *Store) {
			s.Ports.OpenChild = func(*os.File, string, bool, worktreesecure.ValidSegment) (*os.File, error) { return nil, boom }
		}},
		{"read", func(s *Store) { s.Ports.Read = func(*os.File, string, any) error { return boom } }},
		{"replaced before read", func(s *Store) {
			original := s.Ports.OpenChild
			s.Ports.OpenChild = func(parent *os.File, name string, create bool, valid worktreesecure.ValidSegment) (*os.File, error) {
				opened, err := original(parent, name, create, valid)
				if err == nil {
					path := filepath.Join(s.Home, directoryName)
					if renameErr := os.Rename(path, path+".detached"); renameErr != nil {
						return nil, renameErr
					}
					if mkdirErr := os.Mkdir(path, 0o700); mkdirErr != nil {
						return nil, mkdirErr
					}
				}
				return opened, err
			}
		}},
		{"corrupt", func(s *Store) {
			s.Ports.Read = func(_ *os.File, _ string, value any) error { value.(*State).Owner = "wrong"; return nil }
		}},
		{"replaced after read", func(s *Store) {
			s.Ports.Read = func(_ *os.File, _ string, _ any) error {
				path := filepath.Join(s.Home, directoryName)
				if err := os.Rename(path, path+".detached"); err != nil {
					return err
				}
				return os.Mkdir(path, 0o700)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := base
			store.Home = privateTestHome(t)
			if err := os.Mkdir(store.Home, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(store.Home, directoryName), 0o700); err != nil {
				t.Fatal(err)
			}
			tc.change(&store)
			if _, _, err := store.Load(checkout); err == nil {
				t.Fatal("faulty inspection accepted")
			}
		})
	}
	if _, _, err := base.Load(Checkout{}); err == nil {
		t.Fatal("invalid checkout accepted")
	}
	for _, phase := range []string{"replace home", "create child"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			store := NewStore(privateTestHome(t))
			if err := os.Mkdir(store.Home, 0o700); err != nil {
				t.Fatal(err)
			}
			store.Ports.OpenChild = func(_ *os.File, _ string, _ bool, _ worktreesecure.ValidSegment) (*os.File, error) {
				if phase == "replace home" {
					if err := os.Rename(store.Home, store.Home+".detached"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(store.Home, 0o700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Mkdir(filepath.Join(store.Home, directoryName), 0o700); err != nil {
					t.Fatal(err)
				}
				return nil, os.ErrNotExist
			}
			if _, _, err := store.Load(checkout); err == nil {
				t.Fatal("changed missing directory accepted")
			}
		})
	}
}

func TestStoreRebindRequiresExactRetiredCheckoutAndAdmittedActor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		oldExists     bool
		joinPeer      bool
		changeCurrent func(Checkout) Checkout
		expectedRoot  string
		actor         string
		wantError     string
	}{
		{name: "retired root and matching Git identity", expectedRoot: "old", actor: "owner"},
		{name: "old root still exists", oldExists: true, expectedRoot: "old", actor: "owner", wantError: "still exists"},
		{name: "Git identity changed", expectedRoot: "old", actor: "owner", changeCurrent: func(checkout Checkout) Checkout { checkout.GitDir += "/other"; return checkout }, wantError: "prior and current checkout identities"},
		{name: "expected root compare mismatch", expectedRoot: "somewhere-else", actor: "owner", wantError: "prior and current checkout identities"},
		{name: "actor is not admitted", expectedRoot: "old", actor: "stranger", wantError: "current owner"},
		{name: "admitted non-owner cannot rebind", expectedRoot: "old", actor: "peer", joinPeer: true, wantError: "current owner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			oldRoot := filepath.Join(dir, "old")
			currentRoot := filepath.Join(dir, "current")
			if err := os.Mkdir(currentRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.oldExists {
				if err := os.Mkdir(oldRoot, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			oldCheckout := Checkout{ID: "checkout-1", Root: oldRoot, GitDir: filepath.Join(dir, "repo", ".git", "worktrees", "one"), CommonDir: filepath.Join(dir, "repo", ".git")}
			store := NewStore(filepath.Join(dir, ".wb"))
			if _, err := store.WithLocked(context.Background(), oldCheckout, func(state *State, found bool) error {
				if found {
					t.Fatal("unexpected fixture state")
				}
				if err := state.Take(TakeRequest{Caller: "owner", ExpectedOwner: NoOwner, At: testNow}); err != nil {
					return err
				}
				if tc.joinPeer {
					return state.Join("peer", testNow.Add(time.Second))
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			currentCheckout := oldCheckout
			currentCheckout.Root = currentRoot
			if tc.changeCurrent != nil {
				currentCheckout = tc.changeCurrent(currentCheckout)
			}
			expected := oldRoot
			if tc.expectedRoot == "somewhere-else" {
				expected = filepath.Join(dir, tc.expectedRoot)
			}
			state, err := store.Rebind(context.Background(), currentCheckout, expected, tc.actor, testNow.Add(time.Minute))
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("Rebind error = %v, want containing %q", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if state.Checkout != currentCheckout || state.Revision != 2 || len(state.CheckoutRebinds) != 1 {
				t.Fatalf("rebound snapshot = %+v", state)
			}
			change := state.CheckoutRebinds[0]
			if change.PreviousRoot != oldRoot || change.CurrentRoot != currentRoot || change.Actor != "owner" || !change.At.Equal(testNow.Add(time.Minute)) {
				t.Fatalf("audit record = %+v", change)
			}
			loaded, found, err := store.Load(currentCheckout)
			if err != nil || !found || loaded.Checkout.Root != currentRoot {
				t.Fatalf("Load after rebind = %+v, %t, %v", loaded, found, err)
			}
		})
	}
}

func TestStoreRebindRejectsInvalidInputsWithoutChangingSnapshot(t *testing.T) {
	t.Parallel()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldRoot := filepath.Join(base, "retired")
	currentRoot := filepath.Join(base, "current")
	if err := os.Mkdir(currentRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	oldCheckout := testCheckout()
	oldCheckout.Root = oldRoot
	currentCheckout := oldCheckout
	currentCheckout.Root = currentRoot
	store := NewStore(filepath.Join(base, "private"))
	if _, err := store.WithLocked(context.Background(), oldCheckout, func(state *State, found bool) error {
		if found {
			t.Fatal("unexpected existing coordination state")
		}
		return state.Take(TakeRequest{Caller: "owner", ExpectedOwner: NoOwner, At: testNow})
	}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, expectedRoot, actor, wantError string
		checkout                             Checkout
		at                                   time.Time
	}{
		{name: "invalid actor", expectedRoot: oldRoot, actor: "bad actor", checkout: currentCheckout, at: testNow, wantError: "admitted actor"},
		{name: "no owner actor", expectedRoot: oldRoot, actor: NoOwner, checkout: currentCheckout, at: testNow, wantError: "admitted actor"},
		{name: "zero timestamp", expectedRoot: oldRoot, actor: "owner", checkout: currentCheckout, wantError: "admitted actor"},
		{name: "invalid checkout", expectedRoot: oldRoot, actor: "owner", checkout: Checkout{Root: currentRoot}, at: testNow, wantError: "checkout identity is incomplete"},
		{name: "expected root mismatch", expectedRoot: filepath.Join(base, "other"), actor: "owner", checkout: currentCheckout, at: testNow, wantError: "prior and current checkout identities"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := store.Rebind(context.Background(), tc.checkout, tc.expectedRoot, tc.actor, tc.at)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("Rebind error = %v, want containing %q", err, tc.wantError)
			}
			state, found, loadErr := store.Load(oldCheckout)
			if loadErr != nil || !found || state.Checkout != oldCheckout || state.Revision != 1 || len(state.CheckoutRebinds) != 0 {
				t.Fatalf("failed rebind changed stored state: state=%+v found=%t err=%v", state, found, loadErr)
			}
		})
	}
}

func TestStoreRebindFilesystemInspectionErrorPreservesSnapshot(t *testing.T) {
	t.Parallel()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldRoot := base + string(os.PathSeparator) + strings.Repeat("x", 256)
	currentRoot := filepath.Join(base, "current")
	if err := os.Mkdir(currentRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	oldCheckout := testCheckout()
	oldCheckout.Root = oldRoot
	currentCheckout := oldCheckout
	currentCheckout.Root = currentRoot
	store := NewStore(filepath.Join(base, "private"))
	if _, err := store.WithLocked(context.Background(), oldCheckout, func(state *State, found bool) error {
		if found {
			t.Fatal("unexpected existing coordination state")
		}
		return state.Take(TakeRequest{Caller: "owner", ExpectedOwner: NoOwner, At: testNow})
	}); err != nil {
		t.Fatal(err)
	}
	_, err = store.Rebind(context.Background(), currentCheckout, oldRoot, "owner", testNow.Add(time.Minute))
	if err == nil || !strings.Contains(err.Error(), "inspect expected previous checkout root") {
		t.Fatalf("filesystem inspection error = %v", err)
	}
	state, found, loadErr := store.Load(oldCheckout)
	if loadErr != nil || !found || state.Checkout != oldCheckout || state.Revision != 1 || len(state.CheckoutRebinds) != 0 {
		t.Fatalf("filesystem refusal changed stored state: state=%+v found=%t err=%v", state, found, loadErr)
	}
}

func TestStoreRebindRejectsAlreadyCurrentRootAndKeepsState(t *testing.T) {
	t.Parallel()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	currentRoot := filepath.Join(base, "current")
	if err := os.Mkdir(currentRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	currentCheckout := testCheckout()
	currentCheckout.Root = currentRoot
	store := NewStore(filepath.Join(base, "private"))
	if _, err := store.WithLocked(context.Background(), currentCheckout, func(state *State, found bool) error {
		if found {
			t.Fatal("unexpected existing coordination state")
		}
		return state.Take(TakeRequest{Caller: "owner", ExpectedOwner: NoOwner, At: testNow})
	}); err != nil {
		t.Fatal(err)
	}
	_, err = store.Rebind(context.Background(), currentCheckout, filepath.Join(base, "retired"), "owner", testNow.Add(time.Minute))
	if err == nil || !strings.Contains(err.Error(), "prior and current checkout identities") {
		t.Fatalf("already-current Rebind error = %v", err)
	}
	state, found, loadErr := store.Load(currentCheckout)
	if loadErr != nil || !found || state.Checkout != currentCheckout || state.Revision != 1 || len(state.CheckoutRebinds) != 0 {
		t.Fatalf("already-current refusal changed stored state: state=%+v found=%t err=%v", state, found, loadErr)
	}
}

func TestStoreRebindRequiresInitializedStateAndValidEnvelope(t *testing.T) {
	t.Parallel()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	currentRoot := filepath.Join(base, "current")
	if err := os.Mkdir(currentRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	currentCheckout := testCheckout()
	currentCheckout.Root = currentRoot
	oldRoot := filepath.Join(base, "retired")
	emptyStore := NewStore(filepath.Join(base, "empty-private"))
	if _, err := emptyStore.Rebind(context.Background(), currentCheckout, oldRoot, "owner", testNow); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("uninitialized Rebind error = %v", err)
	}
	if _, found, err := emptyStore.Load(currentCheckout); err != nil || found {
		t.Fatalf("refused uninitialized rebind published state: found=%t err=%v", found, err)
	}

	invalidEnvelope := testState(t)
	invalidEnvelope.Owner = "absent-member"
	if err := invalidEnvelope.ValidateRebind(currentCheckout, oldRoot); err == nil || !strings.Contains(err.Error(), "owner is not joined") {
		t.Fatalf("invalid envelope rebind error = %v", err)
	}
	validState := testState(t)
	validState.Checkout.Root = filepath.Join(base, "retired")
	for _, expectedRoot := range []string{"", "retired", base + string(os.PathSeparator) + "sub" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "retired"} {
		if err := validState.ValidateRebind(currentCheckout, expectedRoot); err == nil {
			t.Fatalf("ValidateRebind accepted unsafe expected root %q", expectedRoot)
		}
	}
}

// recoveryFixture publishes one stale snapshot with real private directories,
// one member, and a retryable message, so store-level recovery proofs run
// against the same locked filesystem layout production uses. retiredRoot
// overrides the absent prior root when a case needs an uninspectable path.
func recoveryFixture(t *testing.T, retiredRoot func(base string) string) (Store, Checkout, Checkout, InactiveOwnerRecovery) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldRoot := filepath.Join(base, "retired")
	if retiredRoot != nil {
		oldRoot = retiredRoot(base)
	}
	currentRoot := filepath.Join(base, "current")
	if err := os.Mkdir(currentRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	oldCheckout := Checkout{ID: "checkout-1", Root: oldRoot,
		GitDir: filepath.Join(base, "repo", ".git", "worktrees", "checkout1"), CommonDir: filepath.Join(base, "repo", ".git")}
	currentCheckout := oldCheckout
	currentCheckout.Root = currentRoot
	store := NewStore(filepath.Join(base, "private"))
	if _, err := store.WithLocked(context.Background(), oldCheckout, func(state *State, found bool) error {
		if found {
			t.Fatal("unexpected existing coordination state")
		}
		if err := state.Take(TakeRequest{Caller: "owner", ExpectedOwner: NoOwner, At: testNow}); err != nil {
			return err
		}
		if err := state.Join("peer", testNow); err != nil {
			return err
		}
		_, _, err := state.Send(SendRequest{Sender: "owner", Recipients: []string{"peer"}, IdempotencyKey: "key",
			MessageID: "message", Kind: "text", Body: "before recovery", At: testNow})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return store, oldCheckout, currentCheckout, InactiveOwnerRecovery{
		ExpectedRoot: oldRoot, ExpectedOwner: "owner", ExpectedEpoch: 1, ExpectedRevision: 3, OwnerStatus: "inactive",
		Actor: "successor", Reason: "retired checkout reused by Git; recorded owner exited", At: testNow.Add(time.Hour)}
}

func TestStoreRecoverInactiveOwnerPublishesOneCoherentSnapshot(t *testing.T) {
	t.Parallel()
	store, oldCheckout, currentCheckout, recovery := recoveryFixture(t, nil)
	path := filepath.Join(store.Home, directoryName, oldCheckout.ID+".json")
	revalidations := 0
	state, err := store.RecoverInactiveOwner(context.Background(), currentCheckout, recovery, func() error {
		revalidations++
		// The atomic write has not happened yet: the durable bytes still describe
		// the stale root and inactive owner while the final checks run.
		held, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var observed State
		if err := json.Unmarshal(held, &observed); err != nil {
			return err
		}
		if observed.Checkout != oldCheckout || observed.Owner != "owner" || observed.OwnerEpoch != 1 || observed.Revision != 3 {
			return fmt.Errorf("snapshot changed before final revalidation: %+v", observed)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if revalidations != 1 {
		t.Fatalf("final revalidation ran %d times", revalidations)
	}
	if state.Checkout != currentCheckout || state.Owner != "successor" || state.OwnerEpoch != 2 || state.Revision != 4 {
		t.Fatalf("recovered snapshot = checkout %+v owner %q epoch %d revision %d",
			state.Checkout, state.Owner, state.OwnerEpoch, state.Revision)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var published State
	if err := json.Unmarshal(raw, &published); err != nil {
		t.Fatalf("published snapshot is not one coherent document: %v", err)
	}
	if published.Checkout.Root != currentCheckout.Root || published.Checkout.GitDir != oldCheckout.GitDir ||
		published.Owner != "successor" || published.OwnerEpoch != 2 || published.Revision != 4 {
		t.Fatalf("published root and ownership are not one snapshot: %+v", published)
	}
	if len(published.CheckoutRebinds) != 1 || published.CheckoutRebinds[0].PreviousRoot != oldCheckout.Root {
		t.Fatalf("published rebind audit = %+v", published.CheckoutRebinds)
	}
	if len(published.OwnerChanges) != 2 || published.OwnerChanges[1].Previous != "owner" || published.OwnerChanges[1].Current != "successor" {
		t.Fatalf("published ownership audit = %+v", published.OwnerChanges)
	}
	if len(published.Inboxes["peer"]) != 1 || len(published.Requests) != 1 {
		t.Fatalf("published recovery dropped message history: %+v", published)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("published snapshot mode = %v, %v", info, err)
	}
	if _, found, err := store.Load(currentCheckout); err != nil || !found {
		t.Fatalf("Load of recovered checkout = %t, %v", found, err)
	}
}

func TestStoreRecoverInactiveOwnerRefusalsPreserveExactBytes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		retired    func(string) string
		revalidate func() error
		wantError  string
	}{
		{name: "final revalidation refuses", revalidate: func() error { return errors.New("owner became live") }, wantError: "owner became live"},
		{name: "missing final revalidation", revalidate: nil, wantError: "final revalidation"},
		{name: "retired root uninspectable", retired: func(base string) string {
			return base + string(os.PathSeparator) + strings.Repeat("x", 256)
		}, revalidate: func() error { return nil }, wantError: "inspect expected previous checkout root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, oldCheckout, currentCheckout, recovery := recoveryFixture(t, tc.retired)
			path := filepath.Join(store.Home, directoryName, oldCheckout.ID+".json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.RecoverInactiveOwner(context.Background(), currentCheckout, recovery, tc.revalidate)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("recovery error = %v, want %q", err, tc.wantError)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatalf("refused recovery rewrote the snapshot:\nbefore %s\nafter  %s", before, after)
			}
		})
	}

	store, oldCheckout, currentCheckout, recovery := recoveryFixture(t, nil)
	path := filepath.Join(store.Home, directoryName, oldCheckout.ID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(oldCheckout.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecoverInactiveOwner(context.Background(), currentCheckout, recovery, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "still exists") {
		t.Fatalf("recreated retired root = %v", err)
	}
	if err := os.Remove(oldCheckout.Root); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(oldCheckout.Root), oldCheckout.Root); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecoverInactiveOwner(context.Background(), currentCheckout, recovery, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "still exists") {
		t.Fatalf("symlinked retired root = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("recreated or symlinked retired root changed the snapshot: %v", err)
	}
}

func TestStoreRecoverInactiveOwnerRejectsFailedPersistenceAndMissingState(t *testing.T) {
	t.Parallel()
	store, oldCheckout, currentCheckout, recovery := recoveryFixture(t, nil)
	path := filepath.Join(store.Home, directoryName, oldCheckout.ID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	failingWrite := store
	failingWrite.Ports.Write = func(*os.File, string, any, os.FileMode) error { return errors.New("disk full") }
	if _, err := failingWrite.RecoverInactiveOwner(context.Background(), currentCheckout, recovery, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "publish") {
		t.Fatalf("write failure = %v", err)
	}
	unavailableLock := store
	unavailableLock.Ports.Lock = func(string) lockFile { return &testLock{} }
	if _, err := unavailableLock.RecoverInactiveOwner(context.Background(), currentCheckout, recovery, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "not acquired") {
		t.Fatalf("unavailable lock = %v", err)
	}
	empty := NewStore(privateTestHome(t))
	_, emptyErr := empty.RecoverInactiveOwner(context.Background(), currentCheckout, recovery, func() error { return nil })
	if emptyErr == nil || !strings.Contains(emptyErr.Error(), "not initialized") {
		t.Fatalf("uninitialized recovery = %v", emptyErr)
	}
	if _, found, err := empty.Load(currentCheckout); err != nil || found {
		t.Fatalf("uninitialized recovery published state: found=%t err=%v", found, err)
	}
	changedIdentity := currentCheckout
	changedIdentity.GitDir = filepath.Join(filepath.Dir(currentCheckout.GitDir), "checkout2")
	if _, err := store.RecoverInactiveOwner(context.Background(), changedIdentity, recovery, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "prior and current checkout identities") {
		t.Fatalf("different Git identity = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("refused recovery changed the snapshot: %v", err)
	}
}

func TestStoreRecoverInactiveOwnerSerializesConcurrentRequests(t *testing.T) {
	t.Parallel()
	store, _, currentCheckout, recovery := recoveryFixture(t, nil)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for contender := 0; contender < 2; contender++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := store.RecoverInactiveOwner(context.Background(), currentCheckout, recovery, func() error { return nil })
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("contender error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent recoveries: success=%d conflict=%d", successes, conflicts)
	}
	state, found, err := store.Load(currentCheckout)
	if err != nil || !found {
		t.Fatalf("recovered state = %t, %v", found, err)
	}
	if state.OwnerEpoch != 2 || state.Revision != 4 || len(state.OwnerChanges) != 2 || len(state.CheckoutRebinds) != 1 {
		t.Fatalf("concurrent recovery advanced counters twice: epoch %d revision %d changes %d rebinds %d",
			state.OwnerEpoch, state.Revision, len(state.OwnerChanges), len(state.CheckoutRebinds))
	}
}

// TestStoreRecoverInactiveOwnerAuditKeepsComparedBindingAfterLaterRevision
// proves the existing immutable audit keeps the exact CAS binding and the
// operator's supplied reason after another revision-changing operation. The
// reason deliberately contains JSON punctuation and a newline so a lossy
// concatenation could not reconstruct it.
func TestStoreRecoverInactiveOwnerAuditKeepsComparedBindingAfterLaterRevision(t *testing.T) {
	t.Parallel()
	store, oldCheckout, currentCheckout, recovery := recoveryFixture(t, nil)
	recovery.Reason = "retired root reused: \"quoted\", with, commas\nand a second line"
	if _, err := store.RecoverInactiveOwner(context.Background(), currentCheckout, recovery, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WithLocked(context.Background(), currentCheckout, func(state *State, found bool) error {
		if !found {
			t.Fatalf("recovered state missing before the later transition")
		}
		return state.Join("later-peer", testNow.Add(2*time.Hour))
	}); err != nil {
		t.Fatal(err)
	}
	published, found, err := store.Load(currentCheckout)
	if err != nil || !found {
		t.Fatalf("reloaded state = %t, %v", found, err)
	}
	if published.Revision != recovery.ExpectedRevision+2 || len(published.OwnerChanges) != 2 {
		t.Fatalf("later transition did not advance the revision exactly once: revision %d changes %d",
			published.Revision, len(published.OwnerChanges))
	}
	var audited inactiveOwnerRecoveryAudit
	if err := json.Unmarshal([]byte(published.OwnerChanges[1].Reason), &audited); err != nil {
		t.Fatalf("audited reason is not reconstructible: %v (%q)", err, published.OwnerChanges[1].Reason)
	}
	wantExpected := coordinationBinding{Root: oldCheckout.Root, Owner: "owner", Epoch: 1, Revision: 3}
	wantResulting := coordinationBinding{Root: currentCheckout.Root, Owner: "successor", Epoch: 2, Revision: 4}
	if audited.Reason != recovery.Reason || audited.Expected != wantExpected || audited.Resulting != wantResulting {
		t.Fatalf("audit after a later revision = reason %q expected %+v resulting %+v, want reason %q expected %+v resulting %+v",
			audited.Reason, audited.Expected, audited.Resulting, recovery.Reason, wantExpected, wantResulting)
	}
}
