package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestReadLocalWithNoCloneKnowsNothing and the refusals: a missing clone is no
// error, a clone of another repository is, as are an unreadable config and a
// cancelled read; a snapshot that is a link is not followed.
//
//nolint:paralleltest // bareOrigin calls t.Setenv (git identity and maintenance settings), which Go's testing package forbids combined with t.Parallel
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
	t.Parallel()
	config := "[core]\n\turl = wrong\n[remote \"other\"]\n\turl = also-wrong\n[remote \"origin\"]\n\tfetch = +refs/heads/*\n\turl = git@github.com:o/r.git\n"
	if got := configuredOrigin(config); got != "git@github.com:o/r.git" {
		t.Errorf("origin = %q", got)
	}
	if got := configuredOrigin("[core]\n\tbare = false\n"); got != "" {
		t.Errorf("origin of a config with none = %q", got)
	}
}
