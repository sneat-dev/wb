package gitrepo

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

// TestReadLocalReadsTheExistingCloneAndNothingElse proves the read-only
// reader: the origin has moved ahead (another machine published), the clone's
// origin URL would fail if contacted, and a read returns only what the clone
// already holds while changing nothing under it, lock files included.
func TestReadLocalReadsTheExistingCloneAndNothingElse(t *testing.T) {
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

// TestReadLocalWithNoCloneKnowsNothing and the refusals: a missing clone is no
// error, a clone of another repository is, as are an unreadable config and a
// cancelled read; a snapshot that is a link is not followed.
func TestReadLocalWithNoCloneKnowsNothing(t *testing.T) {
	missing := New(Options{ClonePath: filepath.Join(t.TempDir(), "absent"), CloneURL: "git@github.com:o/r.git"})
	if entries, err := missing.ReadLocal(context.Background()); err != nil || entries != nil {
		t.Fatalf("a missing clone = %v, %v, want nothing known and no error", entries, err)
	}
	if _, err := os.Stat(missing.opts.ClonePath); !os.IsNotExist(err) {
		t.Error("ReadLocal created the clone path")
	}

	origin := bareOrigin(t)
	mine := machine(t, origin)
	if _, err := mine.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	other := New(Options{ClonePath: mine.opts.ClonePath, CloneURL: "git@github.com:someone/else.git"})
	if _, err := other.ReadLocal(context.Background()); err == nil {
		t.Error("a clone of a different repository was read")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := mine.ReadLocal(cancelled); err == nil {
		t.Error("a cancelled read returned entries")
	}

	dir := filepath.Join(mine.opts.ClonePath, "machines", "carol", "desk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(mine.opts.ClonePath, "README.md"), filepath.Join(dir, "snapshot.yaml")); err != nil {
		t.Fatal(err)
	}
	entries, err := mine.ReadLocal(context.Background())
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries with a linked snapshot = %+v, %v, want the link skipped", entries, err)
	}

	config := filepath.Join(mine.opts.ClonePath, ".git", "config")
	if err := os.Chmod(config, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(config, 0o644) })
	if _, openErr := os.ReadFile(config); openErr == nil {
		t.Skip("running with privileges that read a mode 000 file")
	}
	if _, err := mine.ReadLocal(context.Background()); err == nil {
		t.Error("an unreadable config was read")
	}
}

// TestConfiguredOriginReadsTheOriginURLOnly parses a config text without Git.
func TestConfiguredOriginReadsTheOriginURLOnly(t *testing.T) {
	config := "[core]\n\turl = wrong\n[remote \"other\"]\n\turl = also-wrong\n[remote \"origin\"]\n\tfetch = +refs/heads/*\n\turl = git@github.com:o/r.git\n"
	if got := configuredOrigin(config); got != "git@github.com:o/r.git" {
		t.Errorf("origin = %q", got)
	}
	if got := configuredOrigin("[core]\n\tbare = false\n"); got != "" {
		t.Errorf("origin of a config with none = %q", got)
	}
}
