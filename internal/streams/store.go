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

// Unreadable names one stream whose record could not be parsed.
//
// It is reported rather than returned as an error for the whole store: a
// single truncated file must not refuse every `wb stream start` on the
// machine. It is also never silently dropped — a stream WB cannot read may
// hold live links, and pretending it is absent is the failure mode
// `status` exists to prevent.
type Unreadable struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// List returns every readable stream in the store, newest first, alongside
// every stream whose record could not be parsed.
func (store *Store) List() ([]Stream, []Unreadable, error) {
	entries, err := os.ReadDir(store.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read stream store %s: %w", store.Root, err)
	}
	streams := make([]Stream, 0, len(entries))
	var unreadable []Unreadable
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		stream, err := store.Load(entry.Name())
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			unreadable = append(unreadable, Unreadable{
				Name: entry.Name(), Path: store.statePath(entry.Name()), Reason: RedactString(err.Error()),
			})
			continue
		}
		streams = append(streams, stream)
	}
	sort.SliceStable(streams, func(i, j int) bool {
		return streams[i].CreatedAt.After(streams[j].CreatedAt)
	})
	sort.Slice(unreadable, func(i, j int) bool { return unreadable[i].Name < unreadable[j].Name })
	return streams, unreadable, nil
}

// Create reserves a stream name and writes its record.
//
// It is the arbiter for two concurrent `stream start` calls on one name: the
// exclusive create happens under the store-wide lock and BEFORE either call
// publishes anything, so the loser refuses with no side effects on the remote.
// The earlier design let both calls create worktrees, push branches and open
// pull requests and only then arbitrated the file — which left the loser's
// effects stranded.
//
// The record is written through the same temp-file-and-rename path Update
// uses, so an interrupted create can never leave a truncated file that would
// then refuse every later `stream start` on the machine.
func (store *Store) Create(stream Stream) (Stream, error) {
	if err := ValidateName(stream.Name); err != nil {
		return Stream{}, err
	}
	release, err := store.lockStore()
	if err != nil {
		return Stream{}, err
	}
	defer release()
	return store.createLocked(stream)
}

func (store *Store) createLocked(stream Stream) (Stream, error) {
	if _, err := os.Stat(store.statePath(stream.Name)); err == nil {
		return Stream{}, fmt.Errorf("stream %q already exists at %s", stream.Name, store.statePath(stream.Name))
	} else if !os.IsNotExist(err) {
		return Stream{}, fmt.Errorf("inspect stream state: %w", err)
	}
	stream.SchemaVersion = SchemaVersion
	now := store.now()
	if stream.CreatedAt.IsZero() {
		stream.CreatedAt = now
	}
	stream.UpdatedAt = now
	if stream.Phase == "" {
		// A record handed in already ended keeps that meaning; only a genuinely
		// new stream starts in the creating phase.
		stream.Phase = PhaseCreating
		if stream.EndedAt != nil {
			stream.Phase = PhaseEnded
		}
	}
	if err := store.writeAtomically(stream.Name, stream); err != nil {
		return Stream{}, err
	}
	return stream, nil
}

// WithStoreLock runs body while holding the store-wide lock, so a caller can
// make "no repository is in another open stream" and "reserve this name" one
// indivisible decision. Without it the guard is a check with a gap after it.
func (store *Store) WithStoreLock(body func() error) error {
	release, err := store.lockStore()
	if err != nil {
		return err
	}
	defer release()
	return body()
}

// CreateLocked is Create for a caller already holding the store lock through
// WithStoreLock. Calling it without that lock is a defect, not a deadlock:
// the lock is not reentrant.
func (store *Store) CreateLocked(stream Stream) (Stream, error) {
	if err := ValidateName(stream.Name); err != nil {
		return Stream{}, err
	}
	return store.createLocked(stream)
}

// Archive renames an ended stream so its name can be used again, keeping the
// record and its event log intact.
//
// `work-and-event-logs-are-never-pruned` forbids discarding the evidence, but
// keeping it must not burn the name forever: before this, `start` refused an
// existing name and `join` refused an ended stream, and the two refusals
// pointed at each other with no way out.
func (store *Store) Archive(name string) (string, error) {
	release, err := store.lockStore()
	if err != nil {
		return "", err
	}
	defer release()
	return store.archiveLocked(name)
}

// ArchiveLocked is Archive for a caller already holding the store lock.
func (store *Store) ArchiveLocked(name string) (string, error) { return store.archiveLocked(name) }

func (store *Store) archiveLocked(name string) (string, error) {
	stream, err := store.Load(name)
	if err != nil {
		return "", err
	}
	if stream.Open() {
		return "", fmt.Errorf("stream %q is still open; end it before its name can be reused", name)
	}
	archived := name + ".ended-" + store.now().Format("20060102T150405Z")
	for suffix := 1; ; suffix++ {
		if _, err := os.Stat(store.Dir(archived)); os.IsNotExist(err) {
			break
		} else if err != nil {
			return "", fmt.Errorf("inspect archive destination: %w", err)
		}
		archived = fmt.Sprintf("%s.ended-%s.%d", name, store.now().Format("20060102T150405Z"), suffix)
	}
	if err := os.Rename(store.Dir(name), store.Dir(archived)); err != nil {
		return "", fmt.Errorf("archive stream %q: %w", name, err)
	}
	// The record's own name follows the directory, so a later List does not
	// report a stream whose stored name disagrees with where it lives.
	stream.ArchivedFrom = name
	stream.Name = archived
	if err := store.writeAtomically(archived, stream); err != nil {
		return "", err
	}
	return archived, nil
}

// Delete removes an ended stream and its event log. It refuses an open stream:
// deleting one would strand its worktrees, branches and pull requests with no
// record any verb could reach.
func (store *Store) Delete(name string) error {
	release, err := store.lockStore()
	if err != nil {
		return err
	}
	defer release()
	stream, err := store.Load(name)
	if err != nil {
		return err
	}
	if stream.Open() {
		return fmt.Errorf("stream %q is still open", name)
	}
	if err := os.RemoveAll(store.Dir(name)); err != nil {
		return fmt.Errorf("delete stream %q: %w", name, err)
	}
	return nil
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

// lockStore takes the store-wide exclusive lock. It is held only across
// bookkeeping — never across a network call — so one stalled `gh` can never
// freeze every stream verb on the machine.
func (store *Store) lockStore() (func(), error) {
	if err := os.MkdirAll(store.Root, 0o700); err != nil {
		return nil, fmt.Errorf("create stream store: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(store.Root, ".store.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open stream store lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock stream store: %w", err)
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}

// RepositoryStream answers the one-open-stream-per-repository question:
// which open stream, if any, already holds this repository. It is what
// `stream start` refuses on, and what names the holder in that refusal.
func (store *Store) RepositoryStream(repository string) (Stream, bool, []Unreadable, error) {
	all, unreadable, err := store.List()
	if err != nil {
		return Stream{}, false, nil, err
	}
	for _, stream := range all {
		if !stream.Open() {
			continue
		}
		if _, ok := stream.Member(repository); ok {
			return stream, true, unreadable, nil
		}
	}
	// "No stream holds this repository" is only true of the streams WB could
	// read. An unreadable record may be the one that holds it, so the caller
	// is handed the list rather than a bare false — the guard decides what to
	// do about an answer it cannot fully stand behind.
	return Stream{}, false, unreadable, nil
}

// LiveLinksForWorktree returns every live link recorded against one consumer
// worktree path, across every open stream.
//
// This is the state half of `merge-refuses-a-linked-worktree`. It is exported
// because the refusal belongs to `wb worktree merge` and `wb pr land`, which
// must be able to ask the question without importing a stream verb.
func (store *Store) LiveLinksForWorktree(worktree string) ([]StreamLink, error) {
	resolved := normalizePath(worktree)
	all, _, err := store.List()
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
