package streams

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
)

// ErrNotFound is returned when no stream with that name exists.
var ErrNotFound = errors.New("stream not found")

// Store reads and writes stream state under WB's home directory.
//
// Every mutation goes through Update, which takes an exclusive lock, re-reads
// from disk, applies the caller's change and writes atomically. That is what
// `stream-verbs-re-read-state-before-mutating` requires: a value a verb read
// when it started is a snapshot, and two verbs running at once must not each
// write back their own stale copy of the whole stream.
type Store struct {
	// Root is <wb-home>/streams. Callers normally build a Store with Open.
	Root string
	// Now is the clock stream timestamps come from. Tests substitute it.
	Now func() time.Time
}

// Open resolves WB's home for one projects root and returns the store below
// it. It does not create anything; the first Save does.
func Open(projectsRoot string) (*Store, error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return nil, err
	}
	return OpenAt(filepath.Join(home, "streams")), nil
}

// OpenAt returns a store rooted at an exact directory. It is the seam tests
// and embedders use instead of an environment variable.
func OpenAt(root string) *Store {
	return &Store{Root: root, Now: time.Now}
}

func (store *Store) now() time.Time {
	if store.Now == nil {
		return time.Now().UTC()
	}
	return store.Now().UTC()
}

// Dir is the per-stream directory. Its layout is part of the contract: the
// event log sits beside the state file so a stream is one removable unit.
func (store *Store) Dir(name string) string { return filepath.Join(store.Root, name) }

func (store *Store) statePath(name string) string {
	return filepath.Join(store.Dir(name), "stream.json")
}

func (store *Store) lockPath(name string) string {
	return filepath.Join(store.Dir(name), "stream.lock")
}

// Load reads one stream. It returns ErrNotFound when the stream does not
// exist, so a caller can tell "no such stream" from "the state is unreadable"
// without matching on prose.
func (store *Store) Load(name string) (Stream, error) {
	if err := ValidateName(name); err != nil {
		return Stream{}, err
	}
	contents, err := os.ReadFile(store.statePath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return Stream{}, fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return Stream{}, fmt.Errorf("read stream state %s: %w", store.statePath(name), err)
	}
	return decodeStream(store.statePath(name), contents)
}

func decodeStream(path string, contents []byte) (Stream, error) {
	var stream Stream
	if err := json.Unmarshal(contents, &stream); err != nil {
		return Stream{}, fmt.Errorf("parse stream state %s: %w", path, err)
	}
	if stream.SchemaVersion > SchemaVersion {
		return Stream{}, fmt.Errorf("stream state %s uses schema version %d; this wb supports %d — run `wb self-update`", path, stream.SchemaVersion, SchemaVersion)
	}
	return stream, nil
}

// List returns every stream in the store, newest first. An unreadable stream
// is reported as an error rather than skipped: silently omitting a stream that
// may hold live links would hide exactly the state `status` exists to show.
func (store *Store) List() ([]Stream, error) {
	entries, err := os.ReadDir(store.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read stream store %s: %w", store.Root, err)
	}
	streams := make([]Stream, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		stream, err := store.Load(entry.Name())
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		streams = append(streams, stream)
	}
	sort.SliceStable(streams, func(i, j int) bool {
		return streams[i].CreatedAt.After(streams[j].CreatedAt)
	})
	return streams, nil
}

// Create writes a stream that must not already exist. It is how `stream start`
// reserves the name: two concurrent starts on one name cannot both win,
// because the exclusive create is the arbiter rather than a prior existence
// check.
func (store *Store) Create(stream Stream) (Stream, error) {
	if err := ValidateName(stream.Name); err != nil {
		return Stream{}, err
	}
	if err := os.MkdirAll(store.Dir(stream.Name), 0o700); err != nil {
		return Stream{}, fmt.Errorf("create stream directory: %w", err)
	}
	stream.SchemaVersion = SchemaVersion
	now := store.now()
	if stream.CreatedAt.IsZero() {
		stream.CreatedAt = now
	}
	stream.UpdatedAt = now
	contents, err := json.MarshalIndent(stream, "", "  ")
	if err != nil {
		return Stream{}, err
	}
	file, err := os.OpenFile(store.statePath(stream.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return Stream{}, fmt.Errorf("stream %q already exists at %s", stream.Name, store.statePath(stream.Name))
		}
		return Stream{}, fmt.Errorf("create stream state: %w", err)
	}
	if _, err := file.Write(append(contents, '\n')); err != nil {
		_ = file.Close()
		return Stream{}, fmt.Errorf("write stream state: %w", err)
	}
	if err := file.Close(); err != nil {
		return Stream{}, fmt.Errorf("close stream state: %w", err)
	}
	return stream, nil
}

// Update applies mutate to the freshly re-read stream under an exclusive lock
// and writes the result atomically. mutate may be called only once; returning
// an error leaves the stored stream untouched.
func (store *Store) Update(name string, mutate func(*Stream) error) (Stream, error) {
	if err := ValidateName(name); err != nil {
		return Stream{}, err
	}
	release, err := store.lock(name)
	if err != nil {
		return Stream{}, err
	}
	defer release()
	stream, err := store.Load(name)
	if err != nil {
		return Stream{}, err
	}
	if err := mutate(&stream); err != nil {
		return Stream{}, err
	}
	stream.SchemaVersion = SchemaVersion
	stream.Name = name
	stream.UpdatedAt = store.now()
	if err := store.writeAtomically(name, stream); err != nil {
		return Stream{}, err
	}
	return stream, nil
}

func (store *Store) writeAtomically(name string, stream Stream) error {
	contents, err := json.MarshalIndent(stream, "", "  ")
	if err != nil {
		return err
	}
	directory := store.Dir(name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create stream directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, "stream-*.json")
	if err != nil {
		return fmt.Errorf("stage stream state: %w", err)
	}
	staged := temporary.Name()
	defer func() { _ = os.Remove(staged) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect stream state: %w", err)
	}
	if _, err := temporary.Write(append(contents, '\n')); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write stream state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("flush stream state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close stream state: %w", err)
	}
	if err := os.Rename(staged, store.statePath(name)); err != nil {
		return fmt.Errorf("publish stream state: %w", err)
	}
	return nil
}

// lock takes the per-stream exclusive lock. It blocks: a stream verb that
// found the lock held would otherwise have to tell the caller to retry, and
// `waits-are-verbs-not-instructions` puts that wait inside the verb.
func (store *Store) lock(name string) (func(), error) {
	if err := os.MkdirAll(store.Dir(name), 0o700); err != nil {
		return nil, fmt.Errorf("create stream directory: %w", err)
	}
	file, err := os.OpenFile(store.lockPath(name), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open stream lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock stream %s: %w", name, err)
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}

// RepositoryStream answers the one-open-stream-per-repository question:
// which open stream, if any, already holds this repository. It is what
// `stream start` refuses on, and what names the holder in that refusal.
func (store *Store) RepositoryStream(repository string) (Stream, bool, error) {
	all, err := store.List()
	if err != nil {
		return Stream{}, false, err
	}
	for _, stream := range all {
		if !stream.Open() {
			continue
		}
		if _, ok := stream.Member(repository); ok {
			return stream, true, nil
		}
	}
	return Stream{}, false, nil
}

// LiveLinksForWorktree returns every live link recorded against one consumer
// worktree path, across every open stream.
//
// This is the state half of `merge-refuses-a-linked-worktree`. It is exported
// because the refusal belongs to `wb worktree merge` and `wb pr land`, which
// must be able to ask the question without importing a stream verb.
func (store *Store) LiveLinksForWorktree(worktree string) ([]StreamLink, error) {
	resolved := normalizePath(worktree)
	all, err := store.List()
	if err != nil {
		return nil, err
	}
	var links []StreamLink
	for _, stream := range all {
		if !stream.Open() {
			continue
		}
		for _, member := range stream.Members {
			if normalizePath(member.Worktree) != resolved {
				continue
			}
			for _, link := range member.Links {
				links = append(links, StreamLink{Stream: stream.Name, Repository: member.Repository, Link: link})
			}
		}
	}
	return links, nil
}

// StreamLink is one live link qualified by the stream and repository holding
// it, so a refusal can name both without a second lookup.
type StreamLink struct {
	Stream     string `json:"stream"`
	Repository string `json:"repository"`
	Link       Link   `json:"link"`
}

func normalizePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(trimmed); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(trimmed)
}
