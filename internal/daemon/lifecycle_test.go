package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQueueOwnershipSurvivesVersionHandoff(t *testing.T) {
	now := time.Date(2026, 9, 5, 6, 0, 0, 0, time.UTC)
	old := NewStarting(nil, "127.0.0.1:8766", Provenance{Executable: "/wb-old", SHA256: "old", Version: "0.96.6"}, "old-owner", now)
	old.MarkReady(101, now)
	next := NewStarting(&old, old.Listen, Provenance{Executable: "/wb-new", SHA256: "new", Version: "0.96.7"}, "new-owner", now.Add(time.Minute))
	if next.Queue.Generation != 2 {
		t.Fatalf("generation = %d, want 2", next.Queue.Generation)
	}
	if next.Queue.HandoffFrom == nil || next.Queue.HandoffFrom.SHA256 != "old" {
		t.Fatalf("handoff source = %#v", next.Queue.HandoffFrom)
	}
	if next.Queue.OwnerToken != "new-owner" || next.Queue.Owner.SHA256 != "new" {
		t.Fatalf("new owner = %#v", next.Queue)
	}
}

func TestStoreRoundTripIsPrivateAndAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", "daemon-state.json")
	state := NewStarting(nil, "127.0.0.1:8766", Provenance{Executable: "/wb", SHA256: "digest", Version: "0.96.6"}, "owner", time.Now())
	store := Store{Path: path}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := store.Load()
	if err != nil || !ok {
		t.Fatalf("Load() = %#v, %t, %v", loaded, ok, err)
	}
	if loaded.Queue.Generation != 1 || loaded.OwnerToken != "owner" {
		t.Fatalf("loaded state = %#v", loaded)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %o, want 600", info.Mode().Perm())
	}
}
