package fleetsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// installArchivedFakeGh puts a fake `gh` on PATH that reports every
// repository as archived, so tests exercising the pruneArchived=true path
// (internal/archiveprune.Evaluate's live GitHub check) never depend on
// network access or real GitHub credentials. It also isolates WB Work Log
// claim scanning from this machine's real fleet state.
func installArchivedFakeGh(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nset -eu\nprintf 'true\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WB_HOME", t.TempDir())
}

// newRemote creates a bare repo with one commit on main and returns its path.
func newRemote(t *testing.T) string {
	t.Helper()
	seed := t.TempDir()
	git(t, seed, "init", "-q", "-b", "main")
	write(t, seed, "f.txt", "v1\n")
	git(t, seed, "add", "-A")
	git(t, seed, "commit", "-qm", "v1")

	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare", "-b", "main")
	git(t, seed, "remote", "add", "origin", remote)
	git(t, seed, "push", "-q", "origin", "main")
	return remote
}

func TestSyncCloneMissing(t *testing.T) {
	remote := newRemote(t)
	root := t.TempDir()
	repo := discover.Repo{Org: "acme", Name: "widgets", CloneURL: remote, Remote: true}

	res := Sync(context.Background(), repo, root, false, false)

	if res.Status != Cloned {
		t.Fatalf("Status = %v, want Cloned (err=%v)", res.Status, res.Err)
	}
	dest := filepath.Join(root, "acme", "widgets")
	if _, err := os.Stat(filepath.Join(dest, "f.txt")); err != nil {
		t.Fatalf("clone did not land at %s: %v", dest, err)
	}
}

func TestSyncCloneMissingDryRun(t *testing.T) {
	remote := newRemote(t)
	root := t.TempDir()
	repo := discover.Repo{Org: "acme", Name: "widgets", CloneURL: remote, Remote: true}

	res := Sync(context.Background(), repo, root, true, false)

	if res.Status != Cloned {
		t.Fatalf("Status = %v, want Cloned", res.Status)
	}
	if _, err := os.Stat(filepath.Join(root, "acme", "widgets")); err == nil {
		t.Fatal("dry-run should not have cloned anything")
	}
}

func TestSyncPullClean(t *testing.T) {
	remote := newRemote(t)
	cloneDir := filepath.Join(t.TempDir(), "widgets")
	if out, err := exec.Command("git", "clone", "-q", remote, cloneDir).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}

	seed := t.TempDir()
	if out, err := exec.Command("git", "clone", "-q", remote, seed).CombinedOutput(); err != nil {
		t.Fatalf("seed clone: %v: %s", err, out)
	}
	write(t, seed, "f.txt", "v2\n")
	git(t, seed, "commit", "-qam", "v2")
	git(t, seed, "push", "-q", "origin", "main")

	repo := discover.Repo{Org: "acme", Name: "widgets", Path: cloneDir, Remote: true}
	res := Sync(context.Background(), repo, "", false, false)

	if res.Status != Pulled {
		t.Fatalf("Status = %v, want Pulled (err=%v)", res.Status, res.Err)
	}
	if !res.PullAttempted || !res.PullSucceeded || !res.Updated || res.PullSummary() != "updated from remote" {
		t.Fatalf("pull action = %+v, want successful remote update", res)
	}
	got, err := os.ReadFile(filepath.Join(cloneDir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v2\n" {
		t.Fatalf("f.txt = %q, want v2", got)
	}
}

func TestSyncSkipDirty(t *testing.T) {
	remote := newRemote(t)
	cloneDir := t.TempDir()
	if out, err := exec.Command("git", "clone", "-q", remote, cloneDir).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}
	write(t, cloneDir, "f.txt", "dirty\n")

	repo := discover.Repo{Org: "acme", Name: "widgets", Path: cloneDir, Remote: true}
	res := Sync(context.Background(), repo, "", false, false)

	if res.Status != SkippedDirty {
		t.Fatalf("Status = %v, want SkippedDirty (err=%v)", res.Status, res.Err)
	}
	if len(res.Detail.Modified) != 1 {
		t.Errorf("Detail.Modified = %v, want 1 entry", res.Detail.Modified)
	}
}

// TestSyncArchivedDefaultDoesNotPrune is the regression test for the
// founder's ruling: a plain `wb sync` (pruneArchived=false) must never delete
// an archived clone, however clean it is. Before this fix, syncArchived
// deleted on nothing more than gitops.RepoStatus.Dirty(), gated by no flag at
// all — this exact fixture would have been removed.
func TestSyncArchivedDefaultDoesNotPrune(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "f.txt", "v1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "v1")
	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare", "-b", "main")
	git(t, dir, "remote", "add", "origin", remote)
	// -u: the off-path now routes an archived clone through the ordinary
	// pull logic, which needs upstream tracking configured exactly like a
	// real clone would have it.
	git(t, dir, "push", "-q", "-u", "origin", "main")

	repo := discover.Repo{Org: "acme", Name: "widgets", Path: dir, Remote: true, Archived: true}
	res := Sync(context.Background(), repo, "", false, false)

	if res.Status == RemovedArchived {
		t.Fatalf("plain sync deleted an archived clone without --prune-archived: %+v", res)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("archived clone was removed by a plain sync (no --prune-archived): stat err=%v", err)
	}
	if !res.Archived {
		t.Error("Archived = false, want true so a report never hides that this repository is archived")
	}
	if !res.ArchivedNotPruned {
		t.Error("ArchivedNotPruned = false, want true: pruning was not requested")
	}
	// Treated exactly like an ordinary clean clone: pulled, not classified
	// into any of the archived-specific statuses.
	if res.Status != Pulled {
		t.Errorf("Status = %v, want Pulled (archived repos are synced like any other without the flag)", res.Status)
	}
}

func TestSyncArchivedWithFlagRemovesQualifying(t *testing.T) {
	installArchivedFakeGh(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "f.txt", "v1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "v1")
	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare", "-b", "main")
	git(t, dir, "remote", "add", "origin", remote)
	git(t, dir, "push", "-q", "origin", "main")

	repo := discover.Repo{Org: "acme", Name: "widgets", Path: dir, Remote: true, Archived: true}
	res := Sync(context.Background(), repo, "", false, true)

	if res.Status != RemovedArchived {
		t.Fatalf("Status = %v, want RemovedArchived (err=%v)", res.Status, res.Err)
	}
	if res.Reason == "" {
		t.Error("Reason is empty, want the archiveprune explanation")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("archived dir should have been removed, stat err=%v", err)
	}
}

func TestSyncArchivedWithFlagRemovesQualifyingDryRun(t *testing.T) {
	installArchivedFakeGh(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "f.txt", "v1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "v1")
	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare", "-b", "main")
	git(t, dir, "remote", "add", "origin", remote)
	git(t, dir, "push", "-q", "origin", "main")

	repo := discover.Repo{Org: "acme", Name: "widgets", Path: dir, Remote: true, Archived: true}
	res := Sync(context.Background(), repo, "", true, true)

	if res.Status != RemovedArchived {
		t.Fatalf("Status = %v, want RemovedArchived", res.Status)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("dry-run should not have removed the dir: %v", err)
	}
}

// TestSyncArchivedWithFlagRefusesUnsafe reuses the same dangerous-case
// fixture shape internal/archiveprune already covers exhaustively (untracked
// files) to prove the wiring — not to re-litigate every case, which stays
// archiveprune's job — while confirming Evaluate's decision, not a
// reimplementation of it, is what gates deletion here.
func TestSyncArchivedWithFlagRefusesUnsafe(t *testing.T) {
	installArchivedFakeGh(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "f.txt", "v1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "v1")
	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare", "-b", "main")
	git(t, dir, "remote", "add", "origin", remote)
	git(t, dir, "push", "-q", "origin", "main")
	write(t, dir, "uncommitted.txt", "oops\n")

	repo := discover.Repo{Org: "acme", Name: "widgets", Path: dir, Remote: true, Archived: true}
	res := Sync(context.Background(), repo, "", false, true)

	if res.Status != KeptArchived {
		t.Fatalf("Status = %v, want KeptArchived (err=%v)", res.Status, res.Err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("unsafe archived dir should be kept: %v", err)
	}
	if len(res.Detail.Untracked) != 1 {
		t.Errorf("Detail.Untracked = %v, want 1 entry", res.Detail.Untracked)
	}
	if res.Reason == "" {
		t.Error("Reason is empty, want the archiveprune refusal explanation")
	}
}

// TestSyncArchivedWithFlagRefusesLinkedWorktree is the named incident this
// whole safety predicate exists for: a plain Dirty() check (working tree,
// stash, unpushed commits) cannot see a linked worktree at all, so it would
// let sync call os.RemoveAll on a canonical clone that another checkout's
// Git metadata still depends on, breaking or orphaning it.
func TestSyncArchivedWithFlagRefusesLinkedWorktree(t *testing.T) {
	installArchivedFakeGh(t)
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "f.txt", "v1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "v1")
	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare", "-b", "main")
	git(t, dir, "remote", "add", "origin", remote)
	git(t, dir, "push", "-q", "origin", "main")

	worktreePath := filepath.Join(t.TempDir(), "linked")
	git(t, dir, "worktree", "add", "-q", "-b", "task", worktreePath, "main")

	repo := discover.Repo{Org: "acme", Name: "widgets", Path: dir, Remote: true, Archived: true}
	res := Sync(context.Background(), repo, "", false, true)

	if res.Status == RemovedArchived {
		t.Fatalf("archived clone with a linked worktree was deleted: %+v", res)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("canonical clone was deleted while a linked worktree still referenced it: %v", err)
	}
}

func TestSyncArchivedAbsent(t *testing.T) {
	repo := discover.Repo{Org: "acme", Name: "widgets", Remote: true, Archived: true}
	// AbsentArchived does not depend on pruneArchived: there is nothing to
	// prune or pull either way.
	res := Sync(context.Background(), repo, "", false, false)
	if res.Status != AbsentArchived {
		t.Fatalf("Status = %v, want AbsentArchived", res.Status)
	}
}

func TestSyncForkNoOp(t *testing.T) {
	repo := discover.Repo{Org: "acme", Name: "widgets", Remote: true, IsFork: true}
	res := Sync(context.Background(), repo, "", false, false)
	if res.Status != NoOp {
		t.Fatalf("Status = %v, want NoOp", res.Status)
	}
}

func TestSyncLocalOnlyNoOp(t *testing.T) {
	repo := discover.Repo{Org: "acme", Name: "widgets", Remote: false}
	res := Sync(context.Background(), repo, "", false, false)
	if res.Status != NoOp {
		t.Fatalf("Status = %v, want NoOp", res.Status)
	}
}
