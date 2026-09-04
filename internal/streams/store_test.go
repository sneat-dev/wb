package streams

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreCreateRefusesASecondStreamOfTheSameName(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if _, err := store.Create(Stream{Name: "once"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Stream{Name: "once"}); err == nil {
		t.Fatal("a second create of the same name succeeded")
	}
}

func TestStoreLoadDistinguishesMissingFromUnreadable(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if _, err := store.Load("absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	if err := os.MkdirAll(store.Dir("broken"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir("broken"), "stream.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := store.Load("broken")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want a parse failure distinct from ErrNotFound", err)
	}
}

// .fleet is the reserved event-only directory for landings outside a stream.
// It has no stream.json, so inventory must not mistake it for corrupt stream
// state. A state file there, however, is malformed state and must still be
// surfaced rather than hidden by the reservation.
func TestStoreListIgnoresOnlyTheEventOnlyFleetDirectory(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if err := os.MkdirAll(store.Dir(".fleet"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir(".fleet"), "events.jsonl"), []byte("event\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	streams, unreadable, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(streams) != 0 || len(unreadable) != 0 {
		t.Fatalf("event-only .fleet inventory = streams %#v, unreadable %#v; want neither", streams, unreadable)
	}

	if err := os.WriteFile(filepath.Join(store.Dir(".fleet"), "stream.json"), []byte("{truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, unreadable, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(unreadable) != 1 || unreadable[0].Name != ".fleet" {
		t.Fatalf("inventory with .fleet/stream.json = %#v, want the malformed state fail-closed", unreadable)
	}
	if err := os.Remove(filepath.Join(store.Dir(".fleet"), "stream.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-stream.json", filepath.Join(store.Dir(".fleet"), "stream.json")); err != nil {
		t.Fatal(err)
	}
	_, unreadable, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(unreadable) != 1 || unreadable[0].Name != ".fleet" {
		t.Fatalf("inventory with dangling .fleet/stream.json = %#v, want the unsafe state surfaced", unreadable)
	}
}

// A newer schema is refused rather than partially interpreted: stream state
// carries the only record of the versions a link replaced.
func TestStoreRefusesANewerSchema(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if err := os.MkdirAll(store.Dir("future"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir("future"), "stream.json"), []byte(`{"schema_version":99,"name":"future"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := store.Load("future")
	if err == nil || !strings.Contains(err.Error(), "wb self-update") {
		t.Fatalf("error = %v, want a refusal naming the update command", err)
	}
}

// Update re-reads under an exclusive lock, so concurrent mutations compose
// instead of each writing back its own stale copy of the whole stream.
func TestUpdateSerializesConcurrentMutations(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if _, err := store.Create(Stream{Name: "busy"}); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	const writers = 8
	group.Add(writers)
	for index := 0; index < writers; index++ {
		go func(index int) {
			defer group.Done()
			_, _ = store.Update("busy", func(stream *Stream) error {
				stream.Members = append(stream.Members, Member{
					Repository: "acme/repo", Role: RoleConsumer, JoinedAt: time.Now().UTC(),
				})
				return nil
			})
		}(index)
	}
	group.Wait()
	stream, err := store.Load("busy")
	if err != nil {
		t.Fatal(err)
	}
	if len(stream.Members) != writers {
		t.Fatalf("members = %d, want %d — a concurrent update overwrote another", len(stream.Members), writers)
	}
}

func TestUpdateLeavesTheStoredStreamUntouchedWhenTheMutationFails(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if _, err := store.Create(Stream{Name: "unchanged", Members: []Member{{Repository: "acme/one"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update("unchanged", func(stream *Stream) error {
		stream.Members = nil
		return errors.New("refused")
	}); err == nil {
		t.Fatal("a failing mutation reported success")
	}
	stream, err := store.Load("unchanged")
	if err != nil {
		t.Fatal(err)
	}
	if len(stream.Members) != 1 {
		t.Fatalf("members = %d, want the original 1", len(stream.Members))
	}
}

func TestRepositoryStreamIgnoresEndedStreams(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	ended := time.Now().UTC()
	if _, err := store.Create(Stream{
		Name: "over", EndedAt: &ended, Members: []Member{{Repository: "acme/app"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, held, _, err := store.RepositoryStream("acme/app"); err != nil || held {
		t.Fatalf("held = %t (err %v), want an ended stream to release its repositories", held, err)
	}
}

// The link inventory is the state half of the merge refusal, so it must
// resolve a worktree path through symlinks and ignore ended streams.
func TestLiveLinksForWorktreeFindsLinksAcrossOpenStreams(t *testing.T) {
	base := t.TempDir()
	store := OpenAt(filepath.Join(base, "streams"))
	worktree := filepath.Join(base, "app")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Stream{
		Name: "live",
		Members: []Member{{
			Repository: "acme/app", Worktree: worktree,
			Links: []Link{{Library: "/elsewhere/library", LibraryRepository: "acme/library", Mechanism: MechanismPnpmLink, Identity: "@acme/core"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	links, err := store.LiveLinksForWorktree(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Stream != "live" || links[0].Link.Identity != "@acme/core" {
		t.Fatalf("links = %#v, want the one recorded link", links)
	}
	if none, err := store.LiveLinksForWorktree(filepath.Join(base, "other")); err != nil || len(none) != 0 {
		t.Fatalf("links for an unrelated worktree = %#v (err %v)", none, err)
	}
}

func TestValidateNameRejectsAnythingThatCouldNotBeATaskName(t *testing.T) {
	for _, name := range []string{"", "-leading", "has space", "has/slash", ".."} {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) accepted a name that is not a valid worktree task", name)
		}
	}
	if err := ValidateName("checkout-rewrite.2"); err != nil {
		t.Errorf("ValidateName rejected a valid task name: %v", err)
	}
}

func TestIsStreamBranchRecognizesBothSpellings(t *testing.T) {
	if !IsStreamBranch("stream/x") || !IsStreamBranch("refs/heads/stream/x") {
		t.Error("stream branch not recognized")
	}
	if IsStreamBranch("feature/stream-thing") {
		t.Error("a branch merely mentioning stream was recognized")
	}
}

// The one-open-stream guard must hand back the records it could not read: "no
// stream holds this repository" is only as good as the records WB could read,
// and a truncated file could be the very stream that holds it.
func TestRepositoryStreamSurfacesUnreadableRecords(t *testing.T) {
	store := OpenAt(filepath.Join(t.TempDir(), "streams"))
	if _, err := store.Create(Stream{Name: "healthy", Phase: PhaseOpen}); err != nil {
		t.Fatal(err)
	}
	if err := store.EventLog(reservedFleetMetadataDirectory).Append(Event{Verb: "pr land", Outcome: "findings"}); err != nil {
		t.Fatalf("append fleet event: %v", err)
	}
	if err := os.MkdirAll(store.Dir("broken"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir("broken"), "stream.json"), []byte("{truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, held, unreadable, err := store.RepositoryStream("acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if held {
		t.Fatal("no readable stream holds acme/app")
	}
	if len(unreadable) != 1 || unreadable[0].Name != "broken" {
		t.Fatalf("unreadable = %#v, want only the truncated stream record surfaced to the caller", unreadable)
	}
}
