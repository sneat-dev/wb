package taskoffload

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	store := NewStore(t.TempDir())
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	record := Record{
		SchemaVersion: 1, TaskID: id, Task: "review-auth", WorktreeDir: "/tmp/review",
		Repository: "acme/app", Harness: "claude-code", Model: "opus", Status: StatusParked,
		CreatedAt: time.Unix(10, 0).UTC(),
	}
	if err := store.Save(record, "Review the auth package."); err != nil {
		t.Fatal(err)
	}
	loaded, body, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Task != record.Task || body != "Review the auth package." || loaded.Status != StatusParked {
		t.Fatalf("loaded = %#v body=%q", loaded, body)
	}
}

func TestStoreRejectsEmptyContext(t *testing.T) {
	store := NewStore(t.TempDir())
	err := store.Save(Record{SchemaVersion: 1, TaskID: "task-abc", Task: "x", WorktreeDir: "/tmp"}, "")
	if err == nil {
		t.Fatal("expected empty context refusal")
	}
	if _, err := os.Stat(filepath.Join(store.Root, "task-abc")); !os.IsNotExist(err) {
		t.Fatalf("empty context must not persist a directory: %v", err)
	}
}
