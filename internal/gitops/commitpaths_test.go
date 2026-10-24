package gitops

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

func writeRepoFile(t *testing.T, repo, relative, content string) {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// CommitPaths records additions, modifications and deletions as one commit
// on the branch, leaves the shared index matching the new HEAD, and reports
// false for a second, identical call.
func TestCommitPathsCommitsWorkingTreeStateAndLeavesTheIndexClean(t *testing.T) {
	_, clone := seededOriginAndClone(t)
	writeRepoFile(t, clone, "claims/keep.yaml", "keep\n")
	writeRepoFile(t, clone, "claims/drop.yaml", "drop\n")
	if committed, err := CommitPaths(clone, "first", "claims"); err != nil || !committed {
		t.Fatalf("first CommitPaths = (%v, %v), want (true, nil)", committed, err)
	}
	writeRepoFile(t, clone, "claims/keep.yaml", "changed\n")
	writeRepoFile(t, clone, "claims/new.yaml", "new\n")
	if err := os.Remove(filepath.Join(clone, "claims", "drop.yaml")); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, clone, "unrelated.txt", "not committed\n")

	committed, err := CommitPaths(clone, "second", "claims")
	if err != nil || !committed {
		t.Fatalf("second CommitPaths = (%v, %v), want (true, nil)", committed, err)
	}
	if got := gitIn(t, clone, "ls-tree", "-r", "--name-only", "HEAD"); got != "claims/keep.yaml\nclaims/new.yaml" {
		t.Fatalf("HEAD tree = %q", got)
	}
	if got := gitIn(t, clone, "show", "HEAD:claims/keep.yaml"); got != "changed" {
		t.Fatalf("committed keep.yaml = %q", got)
	}
	if got := gitIn(t, clone, "log", "-1", "--format=%s"); got != "second" {
		t.Fatalf("commit message = %q", got)
	}
	if got := gitIn(t, clone, "status", "--porcelain"); got != "?? unrelated.txt" {
		t.Fatalf("status = %q, want only the untouched unrelated file", got)
	}
	if committed, err := CommitPaths(clone, "nothing", "claims"); err != nil || committed {
		t.Fatalf("unchanged CommitPaths = (%v, %v), want (false, nil)", committed, err)
	}
}

// The first commit into an unborn branch has no parent and creates it.
func TestCommitPathsCreatesTheFirstCommitOnAnUnbornBranch(t *testing.T) {
	setGitIdentity(t)
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	testenv.ConfigureGitAutoMaintenanceOff(t, repo)
	writeRepoFile(t, repo, "README.md", "hello\n")
	if committed, err := CommitPaths(repo, "seed", "README.md"); err != nil || !committed {
		t.Fatalf("CommitPaths = (%v, %v), want (true, nil)", committed, err)
	}
	if got := gitIn(t, repo, "rev-list", "--count", "main"); got != "1" {
		t.Fatalf("main has %s commits, want 1", got)
	}
	if got := gitIn(t, repo, "status", "--porcelain"); got != "" {
		t.Fatalf("status = %q, want clean", got)
	}
}

// CommitPaths runs the repository's pre-commit hook like any `git commit`
// (secret scanning and the rest keep working for every caller), and a hook
// that refuses — as WB's canonical-clean guard did in the remote-store clone —
// or a commit that fails for any other reason leaves nothing staged in the
// shared index (sneat-dev/wb#899).
func TestCommitPathsRunsHooksAndNeverLeavesTheSharedIndexStaged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook scripts rely on a POSIX shell")
	}
	_, clone := seededOriginAndClone(t)
	hooks := filepath.Join(clone, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	ran := filepath.Join(t.TempDir(), "pre-commit-ran")
	if err := testenv.WriteExecutableFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\ntouch '"+ran+"'\n[ ! -f refuse ]\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, clone, "claims/a.yaml", "a\n")
	if committed, err := CommitPaths(clone, "with hook", "claims"); err != nil || !committed {
		t.Fatalf("CommitPaths with a passing pre-commit hook = (%v, %v), want (true, nil)", committed, err)
	}
	if _, err := os.Stat(ran); err != nil {
		t.Fatalf("pre-commit hook did not run: %v", err)
	}

	writeRepoFile(t, clone, "refuse", "")
	writeRepoFile(t, clone, "claims/b.yaml", "b\n")
	if _, err := CommitPaths(clone, "refused", "claims"); err == nil {
		t.Fatal("CommitPaths with a refusing pre-commit hook succeeded, want an error")
	}
	if got := gitIn(t, clone, "diff", "--cached", "--name-only"); got != "" {
		t.Fatalf("staged after a refused commit = %q, want nothing", got)
	}
	if got := gitIn(t, clone, "log", "-1", "--format=%s"); got != "with hook" {
		t.Fatalf("HEAD = %q, want the refused commit absent", got)
	}
}

// Every git step's failure is reported, never read as "nothing to commit".
func TestCommitPathsReportsEachGitStepFailure(t *testing.T) {
	notAnIndex := t.TempDir() // a directory: reading it as the index fails
	for name, body := range map[string]string{
		"locate index": `case "$1" in rev-parse) exit 1;; esac; exit 0`,
		"read index":   `case "$1" in rev-parse) echo '` + notAnIndex + `';; esac; exit 0`,
		"add":          `case "$1" in rev-parse) echo /nonexistent/index;; add) exit 1;; esac; exit 0`,
		"inspect":      `case "$1" in rev-parse) echo /nonexistent/index;; diff) exit 128;; esac; exit 0`,
		"commit":       `case "$1" in rev-parse) echo /nonexistent/index;; diff) exit 1;; commit) exit 1;; esac; exit 0`,
	} {
		t.Run(name, func(t *testing.T) {
			lgCovFakeGit(t, body)
			if committed, err := CommitPaths(t.TempDir(), "msg", "claims"); err == nil || committed {
				t.Fatalf("CommitPaths = (%v, %v), want the %s failure", committed, err, name)
			}
		})
	}
	t.Run("scratch index directory", func(t *testing.T) {
		t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
		if committed, err := CommitPaths(t.TempDir(), "msg", "claims"); err == nil || committed {
			t.Fatalf("CommitPaths = (%v, %v), want the temp-dir failure", committed, err)
		}
	})
}

// ChangedPaths lists one entry per changed file, with untracked directories
// expanded, and reports a failing status.
func TestChangedPathsListsEveryChangedFile(t *testing.T) {
	_, clone := seededOriginAndClone(t)
	writeRepoFile(t, clone, "tracked.txt", "a\n")
	gitIn(t, clone, "add", "tracked.txt")
	gitIn(t, clone, "commit", "-q", "-m", "tracked")
	writeRepoFile(t, clone, "tracked.txt", "b\n")
	writeRepoFile(t, clone, "claims/new.yaml", "n\n")
	writeRepoFile(t, clone, "staged.txt", "s\n")
	gitIn(t, clone, "add", "staged.txt")

	changed, err := ChangedPaths(clone)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, entry := range changed {
		got[entry.Path] = entry.Code
	}
	if len(got) != 3 || got["tracked.txt"] != " M" || got["staged.txt"] != "A " || got["claims/new.yaml"] != "??" {
		t.Fatalf("ChangedPaths = %+v", changed)
	}
	for _, entry := range changed {
		if entry.Untracked() != (entry.Path == "claims/new.yaml") {
			t.Fatalf("Untracked() wrong for %+v", entry)
		}
	}
	if _, err := ChangedPaths(t.TempDir()); err == nil {
		t.Fatal("ChangedPaths outside a repository succeeded, want an error")
	}
}

// DiscardPaths undoes staged and unstaged changes, restores a deleted tracked
// file, removes a staged new file, and tolerates a path already gone.
func TestDiscardPathsReturnsPathsToHead(t *testing.T) {
	_, clone := seededOriginAndClone(t)
	writeRepoFile(t, clone, "machines/me/snapshot.yaml", "committed\n")
	writeRepoFile(t, clone, "claims/old.yaml", "old\n")
	gitIn(t, clone, "add", ".")
	gitIn(t, clone, "commit", "-q", "-m", "state")

	writeRepoFile(t, clone, "machines/me/snapshot.yaml", "staged\n")
	writeRepoFile(t, clone, "claims/new.yaml", "new\n")
	gitIn(t, clone, "add", ".")
	if err := os.Remove(filepath.Join(clone, "claims", "old.yaml")); err != nil {
		t.Fatal(err)
	}

	if err := DiscardPaths(clone, "machines/me/snapshot.yaml", "claims/new.yaml", "claims/old.yaml", "claims/never.yaml"); err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, clone, "status", "--porcelain"); got != "" {
		t.Fatalf("status after discard = %q, want clean", got)
	}
	if data, err := os.ReadFile(filepath.Join(clone, "machines", "me", "snapshot.yaml")); err != nil || string(data) != "committed\n" {
		t.Fatalf("snapshot = %q, %v; want the committed bytes", data, err)
	}
}

// Every DiscardPaths step's failure is reported.
func TestDiscardPathsReportsEachStepFailure(t *testing.T) {
	for name, body := range map[string]string{
		"reset":          `case "$1" in reset) exit 1;; esac; exit 0`,
		"ls-files":       `case "$1" in ls-files) exit 1;; esac; exit 0`,
		"checkout-index": `case "$1" in ls-files) printf 'claims/a.yaml\0';; checkout-index) exit 1;; esac; exit 0`,
	} {
		t.Run(name, func(t *testing.T) {
			lgCovFakeGit(t, body)
			if err := DiscardPaths(t.TempDir(), "claims/a.yaml"); err == nil {
				t.Fatalf("DiscardPaths succeeded, want the %s failure", name)
			}
		})
	}
	t.Run("remove", func(t *testing.T) {
		lgCovFakeGit(t, `exit 0`)
		repo := t.TempDir()
		writeRepoFile(t, repo, "claims/a.yaml/inner", "x\n") // a non-empty directory cannot be removed
		err := DiscardPaths(repo, "claims/a.yaml")
		if err == nil || !strings.Contains(err.Error(), "a.yaml") {
			t.Fatalf("DiscardPaths = %v, want the remove failure", err)
		}
	})
}

// Warnings git prints on stderr (an unreadable directory, say) are never
// parsed as changed paths.
func TestChangedPathsIgnoresGitWarnings(t *testing.T) {
	lgCovFakeGit(t, `echo "warning: could not open directory 'machines/': Permission denied" >&2; printf ' M notes.md\0'; exit 0`)
	changed, err := ChangedPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].Path != "notes.md" {
		t.Fatalf("ChangedPaths = %+v, want only notes.md", changed)
	}
}
