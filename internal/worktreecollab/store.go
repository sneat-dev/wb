package worktreecollab

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/worktreelayout"
	"github.com/sneat-dev/wb/internal/worktreesecure"
)

const directoryName = "worktree-collaboration"

type lockFile interface {
	TryLockContext(context.Context, time.Duration) (bool, error)
	Unlock() error
}

// StorePorts are one store invocation's durable boundaries. Callers normally
// use NewStore; tests can fail a specific boundary without global hooks.
type StorePorts struct {
	OpenHome  func(string, bool) (*os.File, error)
	OpenChild func(*os.File, string, bool, worktreesecure.ValidSegment) (*os.File, error)
	Read      func(*os.File, string, any) error
	Write     func(*os.File, string, any, os.FileMode) error
	Lock      func(string) lockFile
}

// Store keeps one checkout's snapshot under WB's private state directory. A
// stable per-checkout lock serializes all changes; the lock file is retained so
// another process cannot acquire a different inode for the same identity.
type Store struct {
	Home  string
	Ports StorePorts
}

func NewStore(home string) Store {
	return Store{Home: home, Ports: StorePorts{
		OpenHome:  worktreesecure.OpenAbsoluteDirectoryNoFollow,
		OpenChild: worktreesecure.OpenPrivateChild,
		Read:      filewrite.ReadJSONAt,
		Write:     filewrite.WriteJSONAtomicAt,
		Lock:      func(path string) lockFile { return flock.New(path) },
	}}
}

// WithLocked loads, validates, and optionally publishes a snapshot while one
// cross-process lock is held. The callback must not mutate external authority
// before it returns: a failed callback never writes coordination state. A
// missing snapshot is distinct from an initialized but unowned state.
func (store Store) WithLocked(ctx context.Context, checkout Checkout, change func(*State, bool) error) (result State, returnErr error) {
	state, err := New(checkout)
	if err != nil {
		return State{}, err
	}
	if change == nil {
		return State{}, fmt.Errorf("coordination operation is required")
	}
	if store.Ports.OpenHome == nil || store.Ports.OpenChild == nil || store.Ports.Read == nil || store.Ports.Write == nil || store.Ports.Lock == nil {
		return State{}, fmt.Errorf("coordination store boundaries are incomplete")
	}
	home, err := store.Ports.OpenHome(store.Home, true)
	if err != nil {
		return State{}, fmt.Errorf("open private coordination home: %w", err)
	}
	defer func() { _ = home.Close() }()
	directory, err := store.Ports.OpenChild(home, directoryName, true, worktreelayout.ValidSafeSegment)
	if err != nil {
		return State{}, fmt.Errorf("open private coordination directory: %w", err)
	}
	defer func() { _ = directory.Close() }()
	if err := store.directoryMatches(home, directory); err != nil {
		return State{}, err
	}
	lock := store.Ports.Lock(filepath.Join(store.Home, directoryName, checkout.ID+".lock"))
	owned, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return State{}, fmt.Errorf("acquire coordination lock: %w", err)
	}
	if !owned {
		return State{}, fmt.Errorf("coordination lock was not acquired")
	}
	defer func() {
		if err := lock.Unlock(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("release coordination lock: %w", err)
		}
	}()
	if err := store.directoryMatches(home, directory); err != nil {
		return State{}, err
	}
	filename := checkout.ID + ".json"
	found := true
	if err := store.Ports.Read(directory, filename, &state); errors.Is(err, os.ErrNotExist) {
		found = false
	} else if err != nil {
		return State{}, fmt.Errorf("read coordination state: %w", err)
	} else if err := state.Validate(checkout); err != nil {
		return State{}, err
	}
	revision := state.Revision
	if err := change(&state, found); err != nil {
		return State{}, err
	}
	if state.Revision != revision {
		if err := state.Validate(checkout); err != nil {
			return State{}, err
		}
		if err := store.directoryMatches(home, directory); err != nil {
			return State{}, err
		}
		if err := store.Ports.Write(directory, filename, state, 0o600); err != nil {
			return State{}, fmt.Errorf("publish coordination state: %w", err)
		}
	}
	if err := store.directoryMatches(home, directory); err != nil {
		return State{}, err
	}
	return state, nil
}

func (store Store) directoryMatches(home, directory *os.File) error {
	for _, check := range []struct {
		path string
		held *os.File
	}{
		{store.Home, home},
		{filepath.Join(store.Home, directoryName), directory},
	} {
		current, err := os.Lstat(check.path)
		if err != nil {
			return fmt.Errorf("recheck coordination directory: %w", err)
		}
		heldInfo, err := check.held.Stat()
		if err != nil {
			return fmt.Errorf("inspect held coordination directory: %w", err)
		}
		if !current.IsDir() || !os.SameFile(current, heldInfo) {
			return fmt.Errorf("coordination directory changed while locked")
		}
	}
	return nil
}
