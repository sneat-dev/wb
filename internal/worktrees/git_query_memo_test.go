package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMemoizableGitQueryAcceptsOnlyStableReadOnlyVerbs(t *testing.T) {
	accepted := [][]string{
		{"rev-parse", "HEAD"},
		{"rev-parse", "--path-format=absolute", "--git-common-dir"},
		{"show", "-s", "--format=%cI", "HEAD"},
		{"merge-base", "--is-ancestor", "a", "b"},
		{"branch", "--show-current"},
		{"worktree", "list", "--porcelain"},
	}
	for _, args := range accepted {
		if !memoizableGitQuery(args) {
			t.Errorf("expected %v to be memoizable", args)
		}
	}
	rejected := [][]string{
		{},
		{"status", "--porcelain=v1"},
		{"diff"},
		{"checkout", "main"},
		{"branch", "-D", "feature"},
		{"branch"},
		{"worktree", "add", "x"},
		{"worktree", "list"},
		{"fetch"},
		{"push"},
	}
	for _, args := range rejected {
		if memoizableGitQuery(args) {
			t.Errorf("expected %v NOT to be memoizable", args)
		}
	}
}

func TestGitQueryMemoServesRepeatedReadOnlyQueryOnce(t *testing.T) {
	repo := newGitFixture(t).canonical
	ctx := withGitQueryMemo(context.Background())

	first, err := git(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	memo := gitQueryMemoFrom(ctx)
	if memo == nil {
		t.Fatal("memo was not installed on the context")
	}
	cached, ok := memo.get(gitQueryMemoKey(repo, []string{"rev-parse", "HEAD"}))
	if !ok || cached != first {
		t.Fatalf("memo did not record the answer: ok=%v cached=%q want %q", ok, cached, first)
	}

	// Poison the memo so a served answer is distinguishable from a re-run.
	memo.put(gitQueryMemoKey(repo, []string{"rev-parse", "HEAD"}), "served-from-memo")
	second, err := git(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if second != "served-from-memo" {
		t.Fatalf("repeat query re-ran git instead of using the memo: %q", second)
	}
}

func TestGitQueryMemoIsInertWithoutInstallation(t *testing.T) {
	repo := newGitFixture(t).canonical
	ctx := context.Background()
	if gitQueryMemoFrom(ctx) != nil {
		t.Fatal("a bare context must carry no memo")
	}
	head, err := git(ctx, repo, "rev-parse", "HEAD")
	if err != nil || head == "" {
		t.Fatalf("plain query failed: head=%q err=%v", head, err)
	}
}

func TestGitQueryMemoNeverCachesWorkingTreeState(t *testing.T) {
	repo := newGitFixture(t).canonical
	ctx := withGitQueryMemo(context.Background())
	if _, err := git(ctx, repo, "status", "--porcelain=v1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := gitQueryMemoFrom(ctx).get(gitQueryMemoKey(repo, []string{"status", "--porcelain=v1"})); ok {
		t.Fatal("status must never be memoized: the working tree can change mid-command")
	}
}

func TestWithGitQueryMemoIsIdempotent(t *testing.T) {
	ctx := withGitQueryMemo(context.Background())
	again := withGitQueryMemo(ctx)
	if gitQueryMemoFrom(ctx) != gitQueryMemoFrom(again) {
		t.Fatal("nested installation must share the outer memo, not replace it")
	}
}

func TestIsAncestorShortCircuitsSelfComparison(t *testing.T) {
	// A nonexistent repository proves no git process ran: a real spawn would
	// fail on the missing directory rather than answer true.
	ok, err := isAncestor(context.Background(), filepath.Join(t.TempDir(), "missing"), "abc123", "abc123")
	if err != nil || !ok {
		t.Fatalf("self comparison = (%v, %v), want (true, nil) without spawning git", ok, err)
	}
}

func TestIsAncestorMemoizesBothVerdicts(t *testing.T) {
	repo := newGitFixture(t).canonical
	head, err := git(context.Background(), repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "second.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "add", "second.txt")
	gitTest(t, repo, "commit", "-m", "second")
	second, err := git(context.Background(), repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	ctx := withGitQueryMemo(context.Background())
	if ok, err := isAncestor(ctx, repo, head, second); err != nil || !ok {
		t.Fatalf("first is ancestor of second: (%v, %v)", ok, err)
	}
	if ok, err := isAncestor(ctx, repo, second, head); err != nil || ok {
		t.Fatalf("second is not ancestor of first: (%v, %v)", ok, err)
	}
	memo := gitQueryMemoFrom(ctx)
	if v, ok := memo.get(gitQueryMemoKey(repo, []string{"merge-base", "--is-ancestor", head, second})); !ok || v != "true" {
		t.Fatalf("true verdict not memoized: ok=%v v=%q", ok, v)
	}
	if v, ok := memo.get(gitQueryMemoKey(repo, []string{"merge-base", "--is-ancestor", second, head})); !ok || v != "false" {
		t.Fatalf("false verdict not memoized: ok=%v v=%q", ok, v)
	}
	// Flip the memo to prove the second call is served from it.
	memo.put(gitQueryMemoKey(repo, []string{"merge-base", "--is-ancestor", second, head}), "true")
	if ok, _ := isAncestor(ctx, repo, second, head); !ok {
		t.Fatal("repeat isAncestor re-ran git instead of using the memo")
	}
}

func TestValidBranchMemoizesVerdictPerName(t *testing.T) {
	name := "memo-probe-" + t.Name()
	validBranchMemo.Delete(name)
	t.Cleanup(func() { validBranchMemo.Delete(name) })

	if !validBranch(context.Background(), name) {
		t.Fatalf("%q should be a valid branch name", name)
	}
	cached, ok := validBranchMemo.Load(name)
	if !ok || cached != true {
		t.Fatalf("verdict not memoized: ok=%v cached=%v", ok, cached)
	}
	// Flip the memo: a repeat must be served from it, not re-asked of git.
	validBranchMemo.Store(name, false)
	if validBranch(context.Background(), name) {
		t.Fatal("repeat validBranch re-ran git instead of using the memo")
	}
}

func TestValidBranchDoesNotMemoizeACancelledContext(t *testing.T) {
	name := "cancel-probe-" + t.Name()
	validBranchMemo.Delete(name)
	t.Cleanup(func() { validBranchMemo.Delete(name) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = validBranch(ctx, name)
	if _, ok := validBranchMemo.Load(name); ok {
		t.Fatal("a verdict produced under a cancelled context must not be remembered")
	}
}

func TestValidBranchDoesNotMemoizeFailure(t *testing.T) {
	name := "invalid..branch"
	validBranchMemo.Delete(name)
	t.Cleanup(func() { validBranchMemo.Delete(name) })

	if validBranch(context.Background(), name) {
		t.Fatalf("%q should be rejected", name)
	}
	if _, ok := validBranchMemo.Load(name); ok {
		t.Fatal("a failed branch validation must remain retryable, not poison the process memo")
	}
}
func TestValidBranchGitLookupRecoversAfterTemporaryPATHRestriction(t *testing.T) {
	originalPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", originalPath) })
	if err := os.Setenv("PATH", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	_ = validBranchGit()
	if err := os.Setenv("PATH", originalPath); err != nil {
		t.Fatal(err)
	}
	if gitPath := validBranchGit(); gitPath == "" {
		t.Fatal("a temporary PATH restriction permanently poisoned Git discovery")
	}
}
