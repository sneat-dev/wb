package streams

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A projects root whose ancestor is a regular file cannot be resolved, so the
// store must fail rather than guess a location.
func TestStoreOpenReportsAnUnresolvableProjectsRoot(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(blocker, "projects")); err == nil {
		t.Fatal("Open resolved a projects root whose ancestor is a regular file")
	}
}

func TestStoreWithoutAClockUsesTheWallClock(t *testing.T) {
	store := &Store{Root: filepath.Join(t.TempDir(), "streams")}
	before := time.Now().UTC().Add(-time.Minute)
	stream, err := store.Create(Stream{Name: "clocked"})
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC().Add(time.Minute)
	if stream.CreatedAt.Before(before) || stream.CreatedAt.After(after) {
		t.Fatalf("CreatedAt = %s, want a wall-clock time in [%s, %s]", stream.CreatedAt, before, after)
	}
	if stream.UpdatedAt.Before(before) || stream.UpdatedAt.After(after) {
		t.Fatalf("UpdatedAt = %s, want a wall-clock time in [%s, %s]", stream.UpdatedAt, before, after)
	}
}

func TestStoreLoadReportsAnUnreadableStatePath(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if err := os.MkdirAll(store.statePath("blocked"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := store.Load("blocked")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Load of a state path that is a directory = %v, want an unreadable-state error", err)
	}
}

func TestStoreListReportsAnUnreadableStoreAndSkipsNonDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "streams")
	store := OpenAt(root)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("not a stream\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A directory with no record at all is not a stream and must be skipped.
	if err := os.MkdirAll(filepath.Join(root, "empty-directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Stream{Name: "real"}); err != nil {
		t.Fatal(err)
	}
	streams, unreadable, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(streams) != 1 || streams[0].Name != "real" || len(unreadable) != 0 {
		t.Fatalf("List = %#v, %#v; want only the real stream", streams, unreadable)
	}

	// A store root that is a regular file cannot be listed at all.
	blocked := OpenAt(filepath.Join(t.TempDir(), "store-file"))
	if _, err := os.Create(blocked.Root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := blocked.List(); err == nil {
		t.Fatal("List of a store root that is a regular file reported no error")
	}
}

func TestStoreListSortsReadableNewestFirstAndUnreadableByName(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	if _, err := store.Create(Stream{Name: "older", CreatedAt: older}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Stream{Name: "newer", CreatedAt: newer}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"z-broken", "a-broken"} {
		if err := os.MkdirAll(store.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.statePath(name), []byte("{truncated"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	streams, unreadable, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(streams) != 2 || streams[0].Name != "newer" || streams[1].Name != "older" {
		t.Fatalf("readable streams = %#v, want newest first", streams)
	}
	if len(unreadable) != 2 || unreadable[0].Name != "a-broken" || unreadable[1].Name != "z-broken" {
		t.Fatalf("unreadable streams = %#v, want name order", unreadable)
	}
}

func TestStoreCreateValidatesTheNameAndReportsALockFailure(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if _, err := store.Create(Stream{Name: "has space"}); err == nil {
		t.Fatal("Create accepted an invalid stream name")
	}
	if _, err := store.CreateLocked(Stream{Name: "has space"}); err == nil {
		t.Fatal("CreateLocked accepted an invalid stream name")
	}

	blocked := OpenAt(filepath.Join(t.TempDir(), "store-file"))
	if _, err := os.Create(blocked.Root); err != nil {
		t.Fatal(err)
	}
	if _, err := blocked.Create(Stream{Name: "any"}); err == nil {
		t.Fatal("Create reported success when the store lock could not be created")
	}
	if err := blocked.WithStoreLock(func() error { return nil }); err == nil {
		t.Fatal("WithStoreLock reported success when the store lock could not be created")
	}

	// A store lock path occupied by a directory cannot be opened.
	unopenable := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if err := os.MkdirAll(filepath.Join(unopenable.Root, ".store.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := unopenable.Create(Stream{Name: "any"}); err == nil {
		t.Fatal("Create reported success when the store lock file could not be opened")
	}
}

func TestStoreWithStoreLockRunsTheBodyAndReportsItsError(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	ran := false
	if err := store.WithStoreLock(func() error { ran = true; return nil }); err != nil {
		t.Fatalf("WithStoreLock: %v", err)
	}
	if !ran {
		t.Fatal("WithStoreLock did not run its body")
	}
	sentinel := errors.New("body refused")
	if err := store.WithStoreLock(func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("WithStoreLock error = %v, want the body's error", err)
	}
}

func TestStoreCreateReportsUninspectableAndUnwritableState(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if err := os.MkdirAll(store.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	// A state path below a regular file cannot be inspected; the failure is
	// not "absent" and must be reported.
	blocked := filepath.Join(store.Root, "blocked")
	if err := os.WriteFile(blocked, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Stream{Name: "blocked"}); err == nil {
		t.Fatal("Create reported success for a state path below a regular file")
	}

	// A dangling symlink is absent to Stat but cannot be created over, so the
	// atomic write must report the failure instead of claiming the stream.
	if err := os.Symlink(filepath.Join(store.Root, "missing-target"), store.Dir("dangling")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Stream{Name: "dangling"}); err == nil {
		t.Fatal("Create reported success although the stream directory could not be created")
	}
}

func TestStoreArchiveReusesAnEndedStreamNameAndKeepsTheEvidence(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	fixed := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	store.Now = func() time.Time { return fixed }
	ended := fixed
	if _, err := store.Create(Stream{
		Name: "reusable", Phase: PhaseEnded, EndedAt: &ended,
		Members: []Member{{Repository: "acme/app", Role: RoleLibrary, Worktree: "/wt/app"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.EventLog("reusable").Append(Event{Verb: "stream end", Outcome: "pass", Detail: "kept"}); err != nil {
		t.Fatal(err)
	}

	archived, err := store.Archive("reusable")
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	want := "reusable.ended-20260203T040506Z"
	if archived != want {
		t.Fatalf("archived name = %q, want %q", archived, want)
	}
	if _, err := os.Stat(store.Dir("reusable")); !os.IsNotExist(err) {
		t.Fatalf("the original stream directory survived archiving (stat err %v)", err)
	}
	record, err := store.Load(archived)
	if err != nil {
		t.Fatalf("Load(archived): %v", err)
	}
	if record.Name != archived || record.ArchivedFrom != "reusable" {
		t.Fatalf("archived record = name %q, archived_from %q", record.Name, record.ArchivedFrom)
	}
	// The event log moved with the directory, so the evidence survives.
	events, err := ReadEvents(store.EventLog(archived).Path)
	if err != nil || len(events) != 1 || events[0].Detail != "kept" {
		t.Fatalf("archived events = %#v (err %v), want the preserved event", events, err)
	}

	// Freeing the name means a new stream can claim it; archiving that one in
	// the same second must not overwrite the first archive.
	if _, err := store.Create(Stream{Name: "reusable", Phase: PhaseEnded, EndedAt: &ended}); err != nil {
		t.Fatal(err)
	}
	second, err := store.Archive("reusable")
	if err != nil {
		t.Fatalf("second Archive: %v", err)
	}
	if second != want+".1" {
		t.Fatalf("second archived name = %q, want the suffixed %q", second, want+".1")
	}
	if _, err := store.Load(want); err != nil {
		t.Fatalf("the first archive was disturbed: %v", err)
	}
}

func TestStoreArchiveRefusesOpenAndMissingStreams(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if _, err := store.Create(Stream{Name: "live", Phase: PhaseOpen}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Archive("live"); err == nil {
		t.Fatal("Archive freed the name of a still-open stream")
	}
	if _, err := store.Archive("absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Archive of a missing stream = %v, want ErrNotFound", err)
	}

	blocked := OpenAt(filepath.Join(t.TempDir(), "store-file"))
	if _, err := os.Create(blocked.Root); err != nil {
		t.Fatal(err)
	}
	if _, err := blocked.Archive("any"); err == nil {
		t.Fatal("Archive reported success when the store lock could not be created")
	}
	if _, err := blocked.ArchiveLocked("any"); err == nil {
		t.Fatal("ArchiveLocked reported success for a missing stream")
	}
}

func TestStoreDeleteRemovesOnlyEndedStreams(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	ended := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if _, err := store.Create(Stream{Name: "finished", Phase: PhaseEnded, EndedAt: &ended}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("finished"); err != nil {
		t.Fatalf("Delete of an ended stream: %v", err)
	}
	if _, err := store.Load("finished"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load after Delete = %v, want ErrNotFound", err)
	}

	if _, err := store.Create(Stream{Name: "live", Phase: PhaseOpen}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("live"); err == nil {
		t.Fatal("Delete removed a still-open stream")
	}
	if err := store.Delete("absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete of a missing stream = %v, want ErrNotFound", err)
	}

	blocked := OpenAt(filepath.Join(t.TempDir(), "store-file"))
	if _, err := os.Create(blocked.Root); err != nil {
		t.Fatal(err)
	}
	if err := blocked.Delete("any"); err == nil {
		t.Fatal("Delete reported success when the store lock could not be created")
	}
}

func TestStoreUpdateReportsUnknownNamesAndUncreatableLocks(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if _, err := store.Update("has space", func(*Stream) error { return nil }); err == nil {
		t.Fatal("Update accepted an invalid stream name")
	}
	if _, err := store.Update("absent", func(*Stream) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update of a missing stream = %v, want ErrNotFound", err)
	}

	// A stream directory path occupied by a regular file cannot be created.
	if err := os.MkdirAll(store.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Dir("blocked"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update("blocked", func(*Stream) error { return nil }); err == nil {
		t.Fatal("Update reported success when the stream directory could not be created")
	}

	// A lock path occupied by a directory cannot be opened.
	if err := os.MkdirAll(store.Dir("unopenable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.lockPath("unopenable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update("unopenable", func(*Stream) error { return nil }); err == nil {
		t.Fatal("Update reported success when the stream lock could not be opened")
	}
}

func TestStoreRepositoryStreamReportsAnUnreadableStore(t *testing.T) {
	blocked := OpenAt(filepath.Join(t.TempDir(), "store-file"))
	if _, err := os.Create(blocked.Root); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := blocked.RepositoryStream("acme/app"); err == nil {
		t.Fatal("RepositoryStream reported a decision from a store it could not read")
	}
	if _, err := blocked.LiveLinksForWorktree("/wt/app"); err == nil {
		t.Fatal("LiveLinksForWorktree reported no links from a store it could not read")
	}
	if _, err := blocked.LinkSourcesForWorktree("/wt/lib"); err == nil {
		t.Fatal("LinkSourcesForWorktree reported no sources from a store it could not read")
	}
}

// A schema-2 record that this binary cannot decode is still indexed well
// enough to prove it does not mention an unrelated repository, so it must not
// refuse that repository's stream. A record that does name the repository, or
// whose index is incomplete, stays fail-closed.
func TestRepositoryStreamExcludesUnreadableRecordsByTheirStableIndex(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	writeState := func(name, contents string) {
		t.Helper()
		if err := os.MkdirAll(store.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.statePath(name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Decoding fails because "name" is a number, but the schema-2 index of
	// members and linked consumers still reads.
	writeState("other-repo", `{"schema_version":2,"name":123,"members":[{"repository":"acme/other"}],"linked_consumers":[{"repository":"acme/fourth"}]}`)
	if _, held, unreadable, err := store.RepositoryStream("acme/app"); err != nil || held || len(unreadable) != 0 {
		t.Fatalf("unrelated repository: held=%t unreadable=%#v err=%v; want the record excluded", held, unreadable, err)
	}

	// The same shape naming this repository is not excludable.
	writeState("same-repo", `{"schema_version":2,"name":456,"members":[{"repository":"acme/app"}]}`)
	if _, held, unreadable, err := store.RepositoryStream("acme/app"); err != nil || held || len(unreadable) != 1 {
		t.Fatalf("named repository: held=%t unreadable=%#v err=%v; want the record to stay unreadable", held, unreadable, err)
	}

	// An empty repository in the index makes the index incomplete.
	writeState("empty-repo", `{"schema_version":2,"name":789,"members":[{"repository":""}]}`)
	if _, _, unreadable, err := store.RepositoryStream("acme/app"); err != nil || len(unreadable) != 2 {
		t.Fatalf("incomplete index = unreadable %#v (err %v); want both records kept", unreadable, err)
	}
}

// A record whose index cannot be reread is unknown, never absent: it is handed
// to the caller so the one-open-stream guard can fail closed.
func TestRepositoryStreamKeepsRecordsWhoseIndexCannotBeRead(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if err := os.MkdirAll(filepath.Join(store.Dir("unreadable-index"), "stream.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, held, unreadable, err := store.RepositoryStream("acme/app")
	if err != nil || held {
		t.Fatalf("held=%t err=%v; want no holder and no error", held, err)
	}
	if len(unreadable) != 1 || unreadable[0].Name != "unreadable-index" {
		t.Fatalf("unreadable = %#v, want the record whose index could not be read", unreadable)
	}
}

// LiveLinksForWorktree is the state half of the merge refusal: it must find
// links held by a linked consumer as well as by a member, and must never
// resurrect a link recorded by an ended stream.
func TestLiveLinksForWorktreeFindsMemberAndLinkedConsumerLinks(t *testing.T) {
	base := t.TempDir()
	store := OpenAt(filepath.Join(base, "streams"))
	worktree := filepath.Join(base, "consumer")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Stream{
		Name: "open-stream",
		Members: []Member{{
			Repository: "acme/member", Worktree: worktree,
			Links: []Link{{Library: "/lib/one", Mechanism: MechanismGoWork, Identity: "acme.test/one"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Stream{
		Name: "linked-consumer",
		LinkedConsumers: []LinkedConsumerBinding{
			{
				Repository: "acme/alternate", Worktree: worktree,
				Links: []Link{{Library: "/lib/two", Mechanism: MechanismPnpmLink, Identity: "@acme/two"}},
			},
			{
				Repository: "acme/elsewhere", Worktree: filepath.Join(base, "elsewhere"),
				Links: []Link{{Library: "/lib/four", Mechanism: MechanismGoWork, Identity: "acme.test/four"}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	ended := time.Now().UTC()
	if _, err := store.Create(Stream{
		Name: "ended-stream", Phase: PhaseEnded, EndedAt: &ended,
		Members: []Member{{
			Repository: "acme/gone", Worktree: worktree,
			Links: []Link{{Library: "/lib/three", Mechanism: MechanismGoWork, Identity: "acme.test/three"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	links, err := store.LiveLinksForWorktree(worktree)
	if err != nil {
		t.Fatal(err)
	}
	byStream := map[string]StreamLink{}
	for _, link := range links {
		byStream[link.Stream] = link
	}
	if len(links) != 2 {
		t.Fatalf("live links = %#v, want the member and linked-consumer links only", links)
	}
	if member, ok := byStream["open-stream"]; !ok || member.Repository != "acme/member" || member.Link.Identity != "acme.test/one" {
		t.Fatalf("member link = %#v", member)
	}
	if consumer, ok := byStream["linked-consumer"]; !ok || consumer.Repository != "acme/alternate" || consumer.Link.Identity != "@acme/two" {
		t.Fatalf("linked-consumer link = %#v", consumer)
	}
	if _, ok := byStream["ended-stream"]; ok {
		t.Fatal("an ended stream's link was reported live")
	}
}

func TestNormalizePathHandlesEmptyMissingAndSymlinkedPaths(t *testing.T) {
	if got := normalizePath("   "); got != "" {
		t.Fatalf("normalizePath of a blank path = %q, want empty", got)
	}
	missing := filepath.Join(t.TempDir(), "missing", "..", "missing")
	if got := normalizePath(missing); got != filepath.Clean(missing) {
		t.Fatalf("normalizePath of a missing path = %q, want its clean spelling %q", got, filepath.Clean(missing))
	}
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := normalizePath(link); got != filepath.Clean(resolved) {
		t.Fatalf("normalizePath through a symlink = %q, want %q", got, filepath.Clean(resolved))
	}
}
