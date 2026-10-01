//go:build e2e

package gitrepo

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"time"
)

// TestE2EReadLocalReadsTheExistingCloneAndNothingElse proves the read-only
// reader: the origin has moved ahead (another machine published), the clone's
// origin URL would fail if contacted, and a read returns only what the clone
// already holds while changing nothing under it, lock files included.
//
//nolint:paralleltest // bareOrigin calls t.Setenv (git identity and maintenance settings), which Go's testing package forbids combined with t.Parallel
func TestE2EReadLocalReadsTheExistingCloneAndNothingElse(t *testing.T) {
	origin := bareOrigin(t)
	mine := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if _, err := mine.Publish(context.Background(), snap("alice", "laptop", at)); err != nil {
		t.Fatal(err)
	}
	// Another machine publishes: the origin is ahead of mine.
	if _, err := machine(t, origin).Publish(context.Background(), snap("bob", "desk", at)); err != nil {
		t.Fatal(err)
	}
	const unreachable = "ssh://127.0.0.1:1/never/contacted.git"
	gitIn(t, mine.opts.ClonePath, "remote", "set-url", "origin", unreachable)
	reader := New(Options{ClonePath: mine.opts.ClonePath, CloneURL: unreachable})
	before := treeStates(t, mine.opts.ClonePath)

	entries, err := reader.ReadLocal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Snapshot.Key() != "alice/laptop" || !entries[0].Snapshot.PublishedAt.Equal(at) {
		t.Fatalf("entries = %+v, want only alice/laptop: the origin's newer state must not be fetched", entries)
	}
	after := treeStates(t, mine.opts.ClonePath)
	for name, state := range before {
		if after[name] != state {
			t.Errorf("%s changed during a read", name)
		}
	}
	for name := range after {
		if _, existed := before[name]; !existed {
			t.Errorf("%s was created during a read", name)
		}
	}
}

// treeStates records every file under dir by modification time, mode and size.
func treeStates(t *testing.T, dir string) map[string]string {
	t.Helper()
	states := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(dir, path)
		states[relative] = info.ModTime().String() + info.Mode().String() + string(rune(info.Size()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return states
}
