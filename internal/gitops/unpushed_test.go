package gitops

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// pushedClone builds a clone with a real origin, so upstream tracking behaves
// exactly as it does in the fleet rather than being simulated.
func pushedClone(t testing.TB) (clone, origin string) {
	t.Helper()
	origin = t.TempDir()
	git(t, origin, "init", "-q", "--bare", "-b", "main", origin)
	testenv.ConfigureGitAutoMaintenanceOff(t, origin)

	seed := t.TempDir()
	git(t, seed, "init", "-q", "-b", "main", seed)
	git(t, seed, "commit", "-q", "--allow-empty", "-m", "seed")
	git(t, seed, "remote", "add", "origin", origin)
	git(t, seed, "push", "-q", "origin", "main")

	clone = t.TempDir()
	cloneCmd := exec.Command("git", "clone", "-q", origin, clone)
	cloneCmd.Env = testenv.GitAutoMaintenanceOffEnv(os.Environ())
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}
	return clone, origin
}

func BenchmarkUnpushedWorkManyBranches(b *testing.B) {
	clone, _ := pushedClone(b)
	for index := range 24 {
		branch := fmt.Sprintf("feature-%02d", index)
		git(b, clone, "checkout", "-q", "-b", branch, "main")
		git(b, clone, "commit", "-q", "--allow-empty", "-m", branch)
	}
	git(b, clone, "checkout", "-q", "main")

	b.ResetTimer()
	for range b.N {
		if _, _, err := UnpushedWork(clone); err != nil {
			b.Fatal(err)
		}
	}
}

func TestUnpushedWorkAttributesCommitToLinkedWorktree(t *testing.T) {
	t.Parallel()
	clone, _ := pushedClone(t)
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, clone, "worktree", "add", "-q", "-b", "linked-work", linked, "main")
	git(t, linked, "commit", "-q", "--allow-empty", "-m", "linked work")

	commits, branches, err := UnpushedWork(clone)
	if err != nil {
		t.Fatalf("UnpushedWork: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("commits = %v, want the linked-worktree commit", commits)
	}
	if len(branches) != 1 {
		t.Fatalf("branches = %+v, want one attributed branch", branches)
	}
	canonicalLinked, err := filepath.EvalSymlinks(linked)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", linked, err)
	}
	if got := branches[0]; got.Branch != "linked-work" || got.Worktree != canonicalLinked || len(got.Commits) != 1 || got.Commits[0] != commits[0] {
		t.Fatalf("branch attribution = %+v, want linked-work in %q with %q", got, linked, commits[0])
	}
}

func TestUnpushedWorkAttributesSharedHistoryToEveryBranch(t *testing.T) {
	t.Parallel()
	clone, _ := pushedClone(t)
	git(t, clone, "checkout", "-q", "-b", "alpha", "main")
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "shared work")
	git(t, clone, "checkout", "-q", "-b", "beta")
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "beta only")

	commits, branches, err := UnpushedWork(clone)
	if err != nil {
		t.Fatalf("UnpushedWork: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("commits = %v, want two unique commits", commits)
	}
	byBranch := make(map[string][]string)
	for _, branch := range branches {
		byBranch[branch.Branch] = branch.Commits
	}
	if got := byBranch["alpha"]; len(got) != 1 || !strings.Contains(got[0], "shared work") {
		t.Fatalf("alpha commits = %v, want shared work", got)
	}
	if got := byBranch["beta"]; len(got) != 2 || !strings.Contains(got[0], "beta only") || !strings.Contains(got[1], "shared work") {
		t.Fatalf("beta commits = %v, want beta-only then shared work", got)
	}
}

func TestParseUnpushedCommitPreservesTabsInSubject(t *testing.T) {
	t.Parallel()
	commit, err := parseUnpushedCommit("abcdef\t1234567 subject\twith tab\tparent1 parent2")
	if err != nil {
		t.Fatal(err)
	}
	if commit.sha != "abcdef" || commit.line != "1234567 subject\twith tab" || len(commit.parents) != 2 {
		t.Fatalf("commit = %+v", commit)
	}
}
