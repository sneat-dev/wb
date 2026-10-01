//go:build e2e

package fleet

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// journalOf makes a real git worktree of a throwaway repository, with a WB
// manifest and a Work Log journal of events owner registrations, the way a
// long-lived task's journal grows: dead owner processes first and, when alive is
// set, this process last.
func journalOf(t *testing.T, events int, alive bool) string {
	t.Helper()
	root := realTempDir(t)
	clone := filepath.Join(root, "repo")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(clone, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "add", ".")
	gitIn(t, clone, "commit", "-m", "x")
	worktree := filepath.Join(root, "task")
	gitIn(t, clone, "worktree", "add", "-b", "task", worktree)
	if err := worktrees.WriteManifest(worktree, worktrees.Manifest{
		Version: 1, EffortID: "task", EffortKind: worktrees.EffortKindTask, Provenance: worktrees.ProvenanceCreated,
		Repository: "acme/widgets", Branch: "task", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	for index := range events {
		pid := 4_000_000 + index // no such process
		if alive && index == events-1 {
			pid = os.Getpid()
		}
		identity := worktrees.AgentIdentity{Runtime: "claude", AgentID: "agent", Model: "opus", PID: pid, WBSessionID: "wbs"}
		if err := worktrees.RecordCustody(worktree, "", "wb test", identity); err != nil {
			t.Fatal(err)
		}
	}
	return worktree
}

// TestE2ERecordReadsTheRealOwnerLivenessAndWritesNothing proves the production
// Record tells a live owner process from a gone one and from none, through the
// real Work Log journal of a real worktree, and never writes inside the
// repository while doing it.
func TestE2ERecordReadsTheRealOwnerLivenessAndWritesNothing(t *testing.T) {
	t.Parallel()
	collectors := LocalCollectors{}
	for name, test := range map[string]struct {
		worktree string
		want     string
	}{
		"newest owner is this process": {journalOf(t, 3, true), worktrees.OwnerLive},
		"every owner is dead":          {journalOf(t, 2, false), worktrees.OwnerGone},
		"no owner recorded":            {journalOf(t, 0, false), worktrees.OwnerUnstated},
	} {
		gitDir := filepath.Join(filepath.Dir(test.worktree), "repo", ".git")
		before := fileStates(t, gitDir)
		record, ok := collectors.Record(test.worktree)
		if !ok || record.Owner != test.want {
			t.Errorf("%s: record = %+v, %v; want owner %q", name, record, ok, test.want)
		}
		requireUnchanged(t, name+": .git", before, fileStates(t, gitDir))
	}
}

// TestE2EOwnerLivenessCost measures what reading the owner process liveness of
// every worktree costs on a snapshot pass (the Open Question of cockpit-views
// REQ:owner-state-vocabulary): one real journal of 40 owner registrations, as a
// long-lived task has, copied to 600 directories and read twice (the second pass
// with warm caches). It logs the figures for the report and fails only when a
// pass over 600 worktrees costs a second or more, the budget the read must stay
// inside to be taken on every pass.
func TestE2EOwnerLivenessCost(t *testing.T) { //nolint:paralleltest // a timing measurement: it must not compete with other tests for the disk and CPU
	const fleet = 600
	source := journalOf(t, 40, true)
	dirs := []string{source}
	for range fleet - 1 {
		dir := realTempDir(t)
		if output, err := exec.CommandContext(t.Context(), "cp", "-R", filepath.Join(source, ".wb"), dir).CombinedOutput(); err != nil {
			t.Fatalf("copy the journal: %v %s", err, output)
		}
		dirs = append(dirs, dir)
	}
	measure := func() time.Duration {
		start := time.Now()
		for _, dir := range dirs {
			if got := declaredOwner(dir); got != worktrees.OwnerLive {
				t.Fatalf("owner of %s = %q, want live", dir, got)
			}
		}
		return time.Since(start)
	}
	first, second := measure(), measure()
	t.Logf("owner liveness, %d worktrees with 40 owner registrations each: first pass %v (%v each), second pass %v (%v each)",
		fleet, first, first/fleet, second, second/fleet)
	if second >= time.Second {
		t.Errorf("a pass over %d worktrees costs %v, want under 1s", fleet, second)
	}
}
