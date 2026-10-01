package worktrees

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreejournal"
)

// journalWith writes a Work Log journal of owner registrations for pids (an
// absent journal for none) straight into a fresh directory, with no Git, and
// returns the directory.
func journalWith(t *testing.T, pids ...int) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if pids == nil {
		return dir
	}
	var events []LocalWorkLogEvent
	for seq, pid := range pids {
		events = append(events, LocalWorkLogEvent{
			Version: 1, Seq: seq, ID: "evt-" + string(rune('a'+seq)), Type: LocalEventOwner, At: time.Now().UTC(),
			Owner: &OwnerRegistration{Agent: "claude", PID: pid, At: time.Now().UTC().Add(time.Duration(seq) * time.Second)},
		})
	}
	content, err := worktreejournal.Store{}.EncodeLocalEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	worklog := filepath.Join(dir, ".wb", "local", "worklog")
	if err := os.MkdirAll(worklog, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worklog, "events.jsonl"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// treeOf lists every path under dir with its mode, size and modification time.
func treeOf(t *testing.T, dir string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		tree[path] = info.Mode().String() + info.ModTime().String() + string(rune(info.Size()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// TestDeclaredOwnerReadOnlyTellsLivenessAndWritesNothing proves the read-only
// reader gives DeclaredOwner's answer for a live, a gone and an unstated owner,
// from a journal of files alone, and that it creates and changes nothing.
func TestDeclaredOwnerReadOnlyTellsLivenessAndWritesNothing(t *testing.T) {
	t.Parallel()
	live, dead := os.Getpid(), 424242
	for name, test := range map[string]struct {
		pids []int
		want string
	}{
		"no journal":     {nil, OwnerUnstated},
		"no process id":  {[]int{0}, OwnerUnstated},
		"a dead owner":   {[]int{dead}, OwnerGone},
		"a live owner":   {[]int{live}, OwnerLive},
		"live then dead": {[]int{live, dead}, OwnerLive},
		"dead then live": {[]int{dead, live}, OwnerLive},
	} {
		dir := journalWith(t, test.pids...)
		before := treeOf(t, dir)
		if got := DeclaredOwnerReadOnly(dir); got != test.want {
			t.Errorf("%s: owner = %q, want %q", name, got, test.want)
		}
		after := treeOf(t, dir)
		if len(after) != len(before) {
			t.Errorf("%s: the reader created %d files", name, len(after)-len(before))
		}
		for path, state := range before {
			if after[path] != state {
				t.Errorf("%s: %s changed", name, path)
			}
		}
	}
	if got := DeclaredOwnerReadOnly(filepath.Join(t.TempDir(), "absent")); got != OwnerUnstated {
		t.Errorf("a directory that is not there = %q", got)
	}
}

// TestDeclaredOwnerPIDReadOnlyGivesTheLiveOwnersProcessOnly proves the process
// id is the live owner's and is 0 for a gone or an unstated owner.
func TestDeclaredOwnerPIDReadOnlyGivesTheLiveOwnersProcessOnly(t *testing.T) {
	t.Parallel()
	live, dead := os.Getpid(), 424242
	for name, test := range map[string]struct {
		pids []int
		want int
	}{
		"no journal":     {nil, 0},
		"a dead owner":   {[]int{dead}, 0},
		"a live owner":   {[]int{live}, live},
		"dead then live": {[]int{dead, live}, live},
	} {
		if _, pid := DeclaredOwnerPIDReadOnly(journalWith(t, test.pids...)); pid != test.want {
			t.Errorf("%s: pid = %d, want %d", name, pid, test.want)
		}
	}
}
