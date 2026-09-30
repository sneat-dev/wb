package worktreejournal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

func secureTmpDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func journalTestStore(t *testing.T) (Store, string) {
	t.Helper()
	root := secureTmpDir(t)
	store := Store{
		OpenDirectory: func(worktree string, create bool) (*os.File, error) {
			return OpenJournalSubdirectory(worktree, "worklog", create)
		},
		Project: func(_ string, events []LocalWorkLogEvent) (LocalWorkLogProjection, error) {
			return Store{}.RebuildLocalProjection(events)
		},
	}
	return store, root
}
func event(seq int, id string) LocalWorkLogEvent {
	return LocalWorkLogEvent{Version: 1, Seq: seq, ID: id, Type: LocalEventSteer, At: time.Unix(int64(seq+1), 0).UTC(), Message: id}
}
func testDir(t *testing.T, store Store, root string) *os.File {
	t.Helper()
	d, e := store.OpenDirectory(root, true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}
func mustJSON(t *testing.T, e LocalWorkLogEvent) []byte {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func TestJournalStoreAppendReplayRepairAndConcurrency(t *testing.T) {
	store, root := journalTestStore(t)
	d := testDir(t, store, root)
	first := event(0, "first")
	got, projection, err := store.AppendLocalEventUnderLock(root, d, first)
	if err != nil || got.ID != "first" || projection.LastSeq != 0 {
		t.Fatalf("append = %#v, %#v, %v", got, projection, err)
	}
	// A crash after journal publication leaves both derivatives recoverable.
	if err := os.Remove(filepath.Join(root, JournalRootDirectory, JournalLocalDirectory, "worklog", OutboxName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, JournalRootDirectory, JournalLocalDirectory, "worklog", ProjectionName)); err != nil {
		t.Fatal(err)
	}
	replay := first
	replay.At = time.Time{}
	got, projection, err = store.AppendLocalEventUnderLock(root, d, replay)
	if err != nil || !store.SameLocalEvent(got, first) || projection.LastEventID != "first" {
		t.Fatalf("replay = %#v, %#v, %v", got, projection, err)
	}
	conflict := first
	conflict.Message = "forged"
	if _, _, err := store.AppendLocalEventUnderLock(root, d, conflict); err == nil || !strings.Contains(err.Error(), "different immutable") {
		t.Fatalf("conflict = %v", err)
	}
	// The lock must serialize real concurrent read/modify/atomic-replace transactions.
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dir, err := store.OpenDirectory(root, true)
			if err != nil {
				errs <- err
				return
			}
			defer dir.Close()
			unlock, err := store.LockLocalWorkLog(dir)
			if err != nil {
				errs <- err
				return
			}
			defer unlock()
			_, _, err = store.AppendLocalEventUnderLock(root, dir, LocalWorkLogEvent{Version: 1, Type: LocalEventSteer, ID: fmt.Sprintf("worker-%d", i)})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.ReadLocalEvents(root)
	if err != nil || len(events) != workers+1 {
		t.Fatalf("events = %d, %v", len(events), err)
	}
	for i, e := range events {
		if e.Seq != i {
			t.Fatalf("sequence[%d]=%d", i, e.Seq)
		}
	}
	if n, err := store.CountLocalOutbox(root); err != nil || n != len(events) {
		t.Fatalf("outbox = %d/%v", n, err)
	}
	read, err := store.ReadLocalProjection(root)
	if err != nil || read.LastSeq != workers {
		t.Fatalf("projection = %#v/%v", read, err)
	}
}

func TestJournalStoreTornFinalRecordAndInteriorCorruption(t *testing.T) {
	store, root := journalTestStore(t)
	d := testDir(t, store, root)
	first := event(0, "stable")
	if err := os.WriteFile(filepath.Join(root, JournalRootDirectory, JournalLocalDirectory, "worklog", EventsName), append(mustJSON(t, first), []byte(`{"version":1,"seq":1,"id":"torn`)...), 0600); err != nil {
		t.Fatal(err)
	}
	_, projection, err := store.AppendLocalEventUnderLock(root, d, event(1, "next"))
	if err != nil || projection.LastSeq != 1 {
		t.Fatalf("repair append = %#v/%v", projection, err)
	}
	raw, err := os.ReadFile(filepath.Join(root, JournalRootDirectory, JournalLocalDirectory, "worklog", EventsName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("torn")) {
		t.Fatalf("torn suffix survived: %q", raw)
	}
	if err := os.WriteFile(filepath.Join(root, JournalRootDirectory, JournalLocalDirectory, "worklog", EventsName), append(append(mustJSON(t, first), []byte("{invalid}\n")...), mustJSON(t, event(2, "later"))...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AppendLocalEventUnderLock(root, d, event(1, "refused")); err == nil || !strings.Contains(err.Error(), "parse local") {
		t.Fatalf("interior corruption = %v", err)
	}
	if _, err := store.RepairCurrentLocalProjection(root); err == nil {
		t.Fatal("repaired interior corruption")
	}
}

func TestJournalStoreOutboxContradictionsAndFaults(t *testing.T) {
	store, root := journalTestStore(t)
	d := testDir(t, store, root)
	one := event(0, "one")
	cases := []struct {
		name    string
		journal []LocalWorkLogEvent
		outbox  []byte
		want    string
	}{
		{"orphan", []LocalWorkLogEvent{one}, mustJSON(t, event(0, "orphan")), "no journal authority"},
		{"conflict", []LocalWorkLogEvent{one}, mustJSON(t, func() LocalWorkLogEvent { x := one; x.Message = "forged"; return x }()), "different immutable"},
		{"duplicate-journal", []LocalWorkLogEvent{one, one}, nil, "occurs more than once"},
		{"invalid-outbox", []LocalWorkLogEvent{one}, []byte("{bad}\n"), "parse local work-log outbox"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.outbox == nil {
				_ = os.Remove(filepath.Join(root, JournalRootDirectory, JournalLocalDirectory, "worklog", OutboxName))
			} else if err := os.WriteFile(filepath.Join(root, JournalRootDirectory, JournalLocalDirectory, "worklog", OutboxName), tc.outbox, 0600); err != nil {
				t.Fatal(err)
			}
			if err := store.RepairLocalOutbox(d, tc.journal); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if err := os.WriteFile(filepath.Join(root, JournalRootDirectory, JournalLocalDirectory, "worklog", OutboxName), mustJSON(t, one), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.RepairLocalOutbox(d, []LocalWorkLogEvent{one}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, JournalRootDirectory, JournalLocalDirectory, "worklog", OutboxName)); err != nil {
		t.Fatal(err)
	}
	broken := one
	broken.Extra = map[string]any{"channel": make(chan int)}
	if err := store.RepairLocalOutbox(d, []LocalWorkLogEvent{broken}); err == nil || !strings.Contains(err.Error(), "encode local work-log outbox") {
		t.Fatalf("encode error=%v", err)
	}
	store.Project = func(string, []LocalWorkLogEvent) (LocalWorkLogProjection, error) {
		return LocalWorkLogProjection{}, errors.New("project failed")
	}
	if _, err := store.RepairLocalEventDerivatives(root, d, []LocalWorkLogEvent{one}); err == nil || !strings.Contains(err.Error(), "project failed") {
		t.Fatalf("project error=%v", err)
	}
	store.WriteJournal = func(*os.File, string, []byte, os.FileMode) error { return errors.New("replace failed") }
	if err := store.RewriteLocalEventJournal(d, []LocalWorkLogEvent{one}); err == nil || !strings.Contains(err.Error(), "repair torn") {
		t.Fatalf("replace error=%v", err)
	}
	if err := store.RewriteLocalEventJournal(d, []LocalWorkLogEvent{broken}); err == nil || !strings.Contains(err.Error(), "encode local") {
		t.Fatalf("encode error=%v", err)
	}
}

func TestJournalStoreReadParseAndSequenceFailures(t *testing.T) {
	store, root := journalTestStore(t)
	if events, err := store.ReadLocalEvents(root); err != nil || events != nil {
		t.Fatalf("missing events=%v/%v", events, err)
	}
	if bytes, err := store.ReadLocalWorkLogBytes(root, OutboxName); err != nil || bytes != nil {
		t.Fatalf("missing bytes=%q/%v", bytes, err)
	}
	if _, err := store.ReadLocalProjection(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing projection=%v", err)
	}
	d := testDir(t, store, root)
	if _, err := store.ReadLocalProjection(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing projection file=%v", err)
	}
	if err := os.WriteFile(filepath.Join(root, JournalRootDirectory, JournalLocalDirectory, "worklog", EventsName), []byte("{bad}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadLocalEvents(root); err == nil {
		t.Fatal("accepted bad JSON")
	}
	if _, _, err := store.ReadLocalEventsForAppend(d); err == nil {
		t.Fatal("accepted bad completed record")
	}
	if _, err := store.ParseLocalEvents(mustJSON(t, event(1, "gap"))); err == nil || !strings.Contains(err.Error(), "start at seq") {
		t.Fatalf("gap=%v", err)
	}
	if _, _, err := store.ParseLocalEventsForRepair(append(mustJSON(t, event(0, "one")), mustJSON(t, event(2, "gap"))...)); err == nil || !strings.Contains(err.Error(), "sequence gap") {
		t.Fatalf("gap=%v", err)
	}
	if err := store.ValidateLocalEventForSequence(LocalWorkLogEvent{}, nil); err == nil {
		t.Fatal("accepted empty event")
	}
	if got, err := store.ParseLocalEvents(append([]byte(" \n"), mustJSON(t, event(0, "one"))...)); err != nil || len(got) != 1 {
		t.Fatalf("parsed=%v/%v", got, err)
	}
	if events, repair, err := store.ParseLocalEventsForRepair([]byte(`{"version":1`)); err != nil || !repair || len(events) != 0 {
		t.Fatalf("torn=%v/%v/%v", events, repair, err)
	}
	if events, repair, err := store.ParseLocalEventsForRepair(mustJSON(t, event(0, "one"))); err != nil || repair || len(events) != 1 {
		t.Fatalf("valid=%v/%v/%v", events, repair, err)
	}
	closed, err := os.Open(secureTmpDir(t))
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	store.OpenDirectory = func(string, bool) (*os.File, error) { return nil, errors.New("open failed") }
	if _, err := store.ReadLocalEvents(root); err == nil {
		t.Fatal("read ignored open error")
	}
	if _, err := store.ReadLocalProjection(root); err == nil {
		t.Fatal("projection ignored open error")
	}
	if _, err := store.RepairCurrentLocalProjection(root); err == nil {
		t.Fatal("repair ignored open error")
	}
	store.OpenDirectory = func(string, bool) (*os.File, error) { return closed, nil }
	if _, err := store.ReadLocalWorkLogBytes(root, EventsName); err == nil {
		t.Fatal("read ignored closed directory")
	}
	if _, err := store.ReadLocalProjection(root); err == nil {
		t.Fatal("projection ignored closed directory")
	}
}

func TestJournalProjectionIDAndJSON(t *testing.T) {
	s := Store{}
	now := time.Unix(100, 0).UTC()
	first := event(0, "one")
	first.At = now
	first.Type = LocalEventCheckpoint
	first.Git = &LocalGitEvidence{Head: "abc"}
	second := event(1, "two")
	second.Type = LocalEventRefreshNeed
	second.Conflict = "fetch_failed"
	third := event(2, "three")
	third.Type = LocalEventRefresh
	third.Target = &LocalTargetEvidence{SHA: "def"}
	fourth := event(3, "four")
	fourth.Type = LocalEventHandoff
	fourth.Result = "offered"
	fourth.Conflict = "integration"
	p, err := s.RebuildLocalProjection([]LocalWorkLogEvent{first, second, third, fourth})
	if err != nil || p.Lifecycle != "handoff" || p.LastCheckpoint.Head != "abc" || p.LastTarget.SHA != "def" || p.Conflict != "integration" {
		t.Fatalf("projection=%#v/%v", p, err)
	}
	terminal := event(4, "five")
	terminal.Type = LocalEventArchive
	p, err = s.RebuildLocalProjection([]LocalWorkLogEvent{terminal})
	if err != nil || p.Lifecycle != "terminal" {
		t.Fatalf("terminal=%#v/%v", p, err)
	}
	empty, err := s.RebuildLocalProjection(nil)
	if err != nil || empty.Lifecycle != "active" {
		t.Fatalf("empty=%#v/%v", empty, err)
	}
	if s.LocalEventID(nil, first) != s.LocalEventID(nil, first) || s.LocalEventID([]LocalWorkLogEvent{first}, first) == s.LocalEventID(nil, first) {
		t.Fatal("event ID not deterministic and sequence-sensitive")
	}
	if s.SameLocalEvent(first, second) || !s.SameLocalEvent(first, first) {
		t.Fatal("same-event comparison")
	}
	bad := first
	bad.Extra = map[string]any{"bad": make(chan int)}
	if _, err := s.EncodeLocalEvents([]LocalWorkLogEvent{bad}); err == nil {
		t.Fatal("encoded channel")
	}
	if s.SameLocalEvent(bad, bad) {
		t.Fatal("compared unencodable evidence")
	}
	raw, err := s.EncodeLocalEvents([]LocalWorkLogEvent{first})
	if err != nil {
		t.Fatal(err)
	}
	var decoded LocalWorkLogEvent
	if err := json.Unmarshal(bytes.TrimSpace(raw), &decoded); err != nil || decoded.ID != "one" {
		t.Fatalf("JSON=%#v/%v", decoded, err)
	}
}

func TestJournalDirectoryNoFollowAndCleanup(t *testing.T) {
	root := secureTmpDir(t)
	if _, err := OpenJournalDirectory(root, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing=%v", err)
	}
	dir, err := OpenJournalSubdirectory(root, "worklog", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := dir.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, ".wb", "local", "worklog"), filepath.Join(root, ".wb", "local")} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			t.Fatalf("directory=%s/%v", path, err)
		}
	}
	if _, err := OpenJournalSubdirectory(root, "missing", false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing subdir=%v", err)
	}
	symlinkRoot := secureTmpDir(t)
	if err := os.Symlink(filepath.Join(root, ".wb"), filepath.Join(symlinkRoot, ".wb")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournalDirectory(symlinkRoot, false); err == nil {
		t.Fatal("followed symlink")
	}
	fileRoot := secureTmpDir(t)
	if err := os.WriteFile(filepath.Join(fileRoot, ".wb"), []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournalSubdirectory(fileRoot, "worklog", true); err == nil {
		t.Fatal("opened regular-file component")
	}
	parent, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if _, err := OpenJournalComponent(int(parent.Fd()), "missing", false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing component=%v", err)
	}
	if fd, err := OpenJournalComponent(int(parent.Fd()), ".wb", false); err != nil {
		t.Fatal(err)
	} else {
		_ = unix.Close(fd)
	}
}

func TestJournalStoreInjectedStorageFailures(t *testing.T) {
	base, root := journalTestStore(t)
	d := testDir(t, base, root)
	one := event(0, "one")
	// The facade assigns version and type before this method; direct callers supply those fields.
	auto := one
	auto.ID = ""
	if got, _, err := base.AppendLocalEventUnderLock(root, d, auto); err != nil || got.ID == "" {
		t.Fatalf("automatic ID=%#v/%v", got, err)
	}
	clearJournal := func() {
		t.Helper()
		for _, name := range []string{EventsName, OutboxName, ProjectionName} {
			_ = os.Remove(filepath.Join(root, JournalRootDirectory, JournalLocalDirectory, "worklog", name))
		}
	}
	clearJournal()
	for _, tc := range []struct {
		name  string
		store Store
		want  string
	}{
		{"journal read", func() Store {
			s := base
			s.ReadFile = func(*os.File, string) ([]byte, error) { return nil, errors.New("read failed") }
			return s
		}(), "read failed"},
		{"journal write", func() Store {
			s := base
			s.WriteFile = func(*os.File, string, []byte, os.FileMode) error { return errors.New("write failed") }
			return s
		}(), "write failed"},
		{"outbox write", func() Store {
			s := base
			s.WriteFile = func(_ *os.File, name string, _ []byte, _ os.FileMode) error {
				if name == OutboxName {
					return errors.New("outbox write failed")
				}
				return nil
			}
			return s
		}(), "outbox write failed"},
		{"projection", func() Store {
			s := base
			s.Project = func(string, []LocalWorkLogEvent) (LocalWorkLogProjection, error) {
				return LocalWorkLogProjection{}, errors.New("project failed")
			}
			return s
		}(), "project failed"},
		{"projection write", func() Store {
			s := base
			s.WriteProjection = func(*os.File, string, any, os.FileMode) error { return errors.New("projection write failed") }
			return s
		}(), "projection write failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearJournal()
			if tc.name == "outbox write" || tc.name == "projection" || tc.name == "projection write" {
				if err := os.WriteFile(filepath.Join(root, JournalRootDirectory, JournalLocalDirectory, "worklog", EventsName), mustJSON(t, one), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := tc.store.RepairLocalEventDerivatives(root, d, []LocalWorkLogEvent{one}); err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if _, _, err := tc.store.AppendLocalEventUnderLock(root, d, one); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	clearJournal()
	if err := os.WriteFile(filepath.Join(root, JournalRootDirectory, JournalLocalDirectory, "worklog", EventsName), []byte("{bad}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := base.AppendLocalEventUnderLock(root, d, one); err == nil {
		t.Fatal("append accepted bad journal")
	}
	clearJournal()
	if err := base.RepairLocalOutbox(d, []LocalWorkLogEvent{one}); err != nil {
		t.Fatal(err)
	}

}

func TestJournalStoreRepairAndLockFailures(t *testing.T) {
	base, root := journalTestStore(t)
	d := testDir(t, base, root)
	one := event(0, "one")
	journalPath := filepath.Join(root, JournalRootDirectory, JournalLocalDirectory, "worklog", EventsName)
	if err := os.WriteFile(journalPath, append(mustJSON(t, one), []byte("{torn")...), 0600); err != nil {
		t.Fatal(err)
	}
	repaired, err := base.RepairCurrentLocalProjection(root)
	if err != nil || repaired.LastEventID != "one" {
		t.Fatalf("repaired=%#v/%v", repaired, err)
	}
	for _, tc := range []struct {
		name    string
		store   Store
		prepare func()
		want    string
	}{
		{"open", func() Store {
			s := base
			s.OpenDirectory = func(string, bool) (*os.File, error) { return nil, errors.New("open failed") }
			return s
		}(), func() {}, "open failed"},
		{"lock", func() Store { s := base; s.Flock = func(int, int) error { return errors.New("lock failed") }; return s }(), func() {}, "lock failed"},
		{"read", func() Store {
			s := base
			s.ReadFile = func(*os.File, string) ([]byte, error) { return nil, errors.New("read failed") }
			return s
		}(), func() {}, "read failed"},
		{"rewrite", func() Store {
			s := base
			s.WriteJournal = func(*os.File, string, []byte, os.FileMode) error { return errors.New("rewrite failed") }
			return s
		}(), func() { _ = os.WriteFile(journalPath, append(mustJSON(t, one), []byte("{torn")...), 0600) }, "rewrite failed"},
		{"outbox", func() Store {
			s := base
			s.Project = func(string, []LocalWorkLogEvent) (LocalWorkLogProjection, error) {
				return LocalWorkLogProjection{}, errors.New("project failed")
			}
			return s
		}(), func() { _ = os.WriteFile(journalPath, mustJSON(t, one), 0600) }, "project failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.prepare()
			if _, err := tc.store.RepairCurrentLocalProjection(root); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	// A symlink at the lock name must be refused and never followed.
	lockPath := filepath.Join(root, JournalRootDirectory, JournalLocalDirectory, "worklog", LockName)
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(journalPath, lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := base.LockLocalWorkLog(d); err == nil {
		t.Fatal("followed lock symlink")
	}
}

func TestJournalStoreReadAndCountFailures(t *testing.T) {
	base, root := journalTestStore(t)
	d := testDir(t, base, root)
	// Directory exists, but the requested file does not.
	if content, err := base.ReadLocalWorkLogBytes(root, OutboxName); err != nil || content != nil {
		t.Fatalf("missing file=%q/%v", content, err)
	}
	store := base
	store.ReadFile = func(*os.File, string) ([]byte, error) { return nil, errors.New("read failed") }
	if _, _, err := store.ReadLocalEventsForAppend(d); err == nil {
		t.Fatal("append read ignored error")
	}
	if _, err := store.CountLocalOutbox(root); err == nil {
		t.Fatal("count ignored error")
	}
	// Scanner's bounded token size is a real error path for a corrupt oversized line.
	outboxPath := filepath.Join(root, JournalRootDirectory, JournalLocalDirectory, "worklog", OutboxName)
	if err := os.WriteFile(outboxPath, bytes.Repeat([]byte("x"), 100000), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := base.CountLocalOutbox(root); err == nil {
		t.Fatal("count accepted oversized line")
	}
}

func TestJournalDirectoryFailurePaths(t *testing.T) {
	root := secureTmpDir(t)
	badRoot := filepath.Join(root, "missing")
	if _, err := OpenJournalDirectory(badRoot, false); err == nil {
		t.Fatal("opened missing worktree")
	}
	// A regular local component refuses descriptor traversal.
	if err := os.Mkdir(filepath.Join(root, JournalRootDirectory), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, JournalRootDirectory, JournalLocalDirectory), []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournalDirectory(root, false); err == nil {
		t.Fatal("opened file local component")
	}
	if err := os.Remove(filepath.Join(root, JournalRootDirectory, JournalLocalDirectory)); err != nil {
		t.Fatal(err)
	}
	ops := DirectoryStore{Chmod: func(int, uint32) error { return errors.New("chmod failed") }}
	if _, err := ops.OpenJournalDirectory(root, true); err == nil || !strings.Contains(err.Error(), "chmod failed") {
		t.Fatalf("root chmod=%v", err)
	}
	ops = DirectoryStore{Matches: func(string, *os.File) bool { return false }}
	if _, err := ops.OpenJournalDirectory(root, false); err == nil || !strings.Contains(err.Error(), "path changed") {
		t.Fatalf("identity=%v", err)
	}
	ops = DirectoryStore{Chmod: func(fd int, mode uint32) error { _ = fd; _ = mode; return errors.New("subdir chmod failed") }}
	// Root read and root create both use chmod, so target the second call.
	count := 0
	ops.Chmod = func(int, uint32) error {
		count++
		if count == 2 {
			return errors.New("subdir chmod failed")
		}
		return nil
	}
	if _, err := ops.OpenJournalSubdirectory(root, "worklog", true); err == nil || !strings.Contains(err.Error(), "subdir chmod failed") {
		t.Fatalf("subdir chmod=%v", err)
	}
	ops = DirectoryStore{OpenComponent: func(parent int, name string, create bool) (int, error) {
		if name == "worklog" {
			return 0, errors.New("subdir open failed")
		}
		return OpenJournalComponent(parent, name, create)
	}}
	if _, err := ops.OpenJournalSubdirectory(root, "worklog", false); err == nil || !strings.Contains(err.Error(), "subdir open failed") {
		t.Fatalf("subdir=%v", err)
	}
	ops = DirectoryStore{OpenRoot: func(string, bool) (*os.File, error) { return nil, errors.New("root open failed") }}
	if _, err := ops.OpenJournalDirectory(root, false); err == nil || !strings.Contains(err.Error(), "root open failed") {
		t.Fatalf("root open=%v", err)
	}
}

func TestJournalStoreAppendFailureAfterPublicationAndRepairRefusal(t *testing.T) {
	base, root := journalTestStore(t)
	d := testDir(t, base, root)
	one := event(0, "one")
	path := filepath.Join(root, JournalRootDirectory, JournalLocalDirectory, "worklog", EventsName)
	if err := os.WriteFile(path, append(mustJSON(t, one), []byte("{torn")...), 0600); err != nil {
		t.Fatal(err)
	}
	store := base
	store.WriteJournal = func(*os.File, string, []byte, os.FileMode) error { return errors.New("rewrite failed") }
	if _, _, err := store.AppendLocalEventUnderLock(root, d, event(1, "two")); err == nil || !strings.Contains(err.Error(), "repair torn") {
		t.Fatalf("rewrite=%v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	bad := one
	bad.Extra = map[string]any{"bad": make(chan int)}
	if _, _, err := base.AppendLocalEventUnderLock(root, d, bad); err == nil || !strings.Contains(err.Error(), "encode local") {
		t.Fatalf("encode=%v", err)
	}
	store = base
	store.Project = func(string, []LocalWorkLogEvent) (LocalWorkLogProjection, error) {
		return LocalWorkLogProjection{}, errors.New("project failed")
	}
	if _, _, err := store.AppendLocalEventUnderLock(root, d, one); err == nil || !strings.Contains(err.Error(), "project failed") {
		t.Fatalf("post-publication=%v", err)
	}
	// The authoritative event remains after a derived projection fails.
	events, err := base.ReadLocalEvents(root)
	if err != nil || len(events) != 1 || events[0].ID != "one" {
		t.Fatalf("authority=%v/%v", events, err)
	}
	store = base
	store.ReadFile = func(_ *os.File, name string) ([]byte, error) {
		if name == OutboxName {
			return nil, errors.New("outbox read failed")
		}
		return nil, os.ErrNotExist
	}
	if err := store.RepairLocalOutbox(d, events); err == nil || !strings.Contains(err.Error(), "outbox read failed") {
		t.Fatalf("outbox read=%v", err)
	}
}
