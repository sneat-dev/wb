package fleetsync

import (
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

	res := Sync(repo, root, false)

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

	res := Sync(repo, root, true)

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
	res := Sync(repo, "", false)

	if res.Status != Pulled {
		t.Fatalf("Status = %v, want Pulled (err=%v)", res.Status, res.Err)
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
	res := Sync(repo, "", false)

	if res.Status != SkippedDirty {
		t.Fatalf("Status = %v, want SkippedDirty (err=%v)", res.Status, res.Err)
	}
	if len(res.Detail.Modified) != 1 {
		t.Errorf("Detail.Modified = %v, want 1 entry", res.Detail.Modified)
	}
}

func TestSyncArchivedRemoveSafe(t *testing.T) {
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
	res := Sync(repo, "", false)

	if res.Status != RemovedArchived {
		t.Fatalf("Status = %v, want RemovedArchived (err=%v)", res.Status, res.Err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("archived dir should have been removed, stat err=%v", err)
	}
}

func TestSyncArchivedRemoveSafeDryRun(t *testing.T) {
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
	res := Sync(repo, "", true)

	if res.Status != RemovedArchived {
		t.Fatalf("Status = %v, want RemovedArchived", res.Status)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("dry-run should not have removed the dir: %v", err)
	}
}

func TestSyncArchivedKeepUnsafe(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "f.txt", "v1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "v1")
	write(t, dir, "uncommitted.txt", "oops\n")

	repo := discover.Repo{Org: "acme", Name: "widgets", Path: dir, Remote: true, Archived: true}
	res := Sync(repo, "", false)

	if res.Status != KeptArchived {
		t.Fatalf("Status = %v, want KeptArchived (err=%v)", res.Status, res.Err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("unsafe archived dir should be kept: %v", err)
	}
	if len(res.Detail.Untracked) != 1 {
		t.Errorf("Detail.Untracked = %v, want 1 entry", res.Detail.Untracked)
	}
}

func TestSyncArchivedAbsent(t *testing.T) {
	repo := discover.Repo{Org: "acme", Name: "widgets", Remote: true, Archived: true}
	res := Sync(repo, "", false)
	if res.Status != AbsentArchived {
		t.Fatalf("Status = %v, want AbsentArchived", res.Status)
	}
}

func TestSyncForkNoOp(t *testing.T) {
	repo := discover.Repo{Org: "acme", Name: "widgets", Remote: true, IsFork: true}
	res := Sync(repo, "", false)
	if res.Status != NoOp {
		t.Fatalf("Status = %v, want NoOp", res.Status)
	}
}

func TestSyncLocalOnlyNoOp(t *testing.T) {
	repo := discover.Repo{Org: "acme", Name: "widgets", Remote: false}
	res := Sync(repo, "", false)
	if res.Status != NoOp {
		t.Fatalf("Status = %v, want NoOp", res.Status)
	}
}
