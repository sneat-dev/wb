package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkLogRecordsOneImmutableClaimPerRepositoryInSharedRun(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".wb")
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	for _, worktree := range []string{first, second} {
		if err := os.MkdirAll(worktree, 0o755); err != nil {
			t.Fatal(err)
		}
		gitTest(t, worktree, "init")
	}
	options := WorkLogOptions{EffortID: "fair-split", RunID: "codex-run-1", AgentRuntime: "codex"}
	for _, result := range []CreateResult{
		{Repository: "acme/first", WorktreeDir: first, Branch: "codex/fair-split", Base: "main", BaseSHA: "aabbcc"},
		{Repository: "acme/second", WorktreeDir: second, Branch: "codex/fair-split", Base: "main", BaseSHA: "ddeeff"},
	} {
		if _, err := recordWorkLog(home, "fair-split", result, options); err != nil {
			t.Fatal(err)
		}
	}
	claims, err := os.ReadDir(filepath.Join(home, "worklogs", "fair-split", "runs", "codex-run-1", "claims"))
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 2 || claims[0].Name() != "acme-first.json" || claims[1].Name() != "acme-second.json" {
		t.Fatalf("claims = %#v, want one immutable entry per repository", claims)
	}
	for _, worktree := range []string{first, second} {
		if _, err := git(context.Background(), worktree, "check-ignore", ".wb-worklog.json"); err != nil {
			t.Fatalf("projection at %s is not locally ignored: %v", worktree, err)
		}
	}
}
