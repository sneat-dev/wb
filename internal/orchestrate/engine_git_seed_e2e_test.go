//go:build e2e

package orchestrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestE2EEngineGitSeedKeepsBranchTemplatesAndClonesSeparate(t *testing.T) {
	t.Parallel()
	main := engineGitSeed(t, "main")
	master := engineGitSeed(t, "master")
	if main == master {
		t.Fatal("distinct default branches shared one seed directory")
	}
	for branch, seed := range map[string]string{"main": main, "master": master} {
		if got := strings.TrimSpace(runEngineGit(t, seed, "symbolic-ref", "--short", "HEAD")); got != branch {
			t.Fatalf("seed branch = %q, want %q", got, branch)
		}
		if got := strings.TrimSpace(runEngineGit(t, seed, "show", "HEAD:dependency.txt")); got != "old" {
			t.Fatalf("seed dependency = %q", got)
		}
		if got := engineGitSeed(t, branch); got != seed {
			t.Fatalf("repeated seed path = %q, want %q", got, seed)
		}
	}
	root := t.TempDir()
	first, second := filepath.Join(root, "first.git"), filepath.Join(root, "second.git")
	runEngineGit(t, root, "clone", "--bare", "--no-hardlinks", main, first)
	runEngineGit(t, root, "clone", "--bare", "--no-hardlinks", main, second)
	runEngineGit(t, first, "branch", "first-only", "main")
	if got := strings.TrimSpace(runEngineGit(t, first, "branch", "--list", "first-only")); got != "first-only" {
		t.Fatalf("first clone lost its private ref: %q", got)
	}
	for _, other := range []string{main, second} {
		if got := strings.TrimSpace(runEngineGit(t, other, "branch", "--list", "first-only")); got != "" {
			t.Fatalf("clone mutation reached %s: %q", other, got)
		}
	}
	seedObject := strings.TrimSpace(runEngineGit(t, main, "rev-parse", "HEAD"))
	object := filepath.Join("objects", seedObject[:2], seedObject[2:])
	seedInfo, seedErr := os.Stat(filepath.Join(main, ".git", object))
	cloneInfo, cloneErr := os.Stat(filepath.Join(first, object))
	if seedErr != nil || cloneErr != nil || os.SameFile(seedInfo, cloneInfo) {
		t.Fatalf("bare clone shared seed object inode: seed=%v clone=%v", seedErr, cloneErr)
	}
}

func TestE2EEngineGitSeedConcurrentFirstUseAfterInvalidBranch(t *testing.T) {
	t.Parallel()
	if _, err := engineGitSeedForBranch("invalid..branch"); err == nil {
		t.Fatal("invalid Git branch unexpectedly seeded")
	}
	const retriedBranch = "retry-after-write-failure"
	if _, err := engineGitSeedForBranchWithWrite(retriedBranch, func(string, []byte, os.FileMode) error {
		return errors.New("test: seed write failed")
	}); err == nil || err.Error() != "test: seed write failed" {
		t.Fatalf("failed seed recipe = %v", err)
	}
	retried, err := engineGitSeedForBranch(retriedBranch)
	if err != nil {
		t.Fatalf("retry after failed seed write: %v", err)
	}
	if got := strings.TrimSpace(runEngineGit(t, retried, "show", "HEAD:dependency.txt")); got != "old" {
		t.Fatalf("retried seed dependency = %q", got)
	}
	const peers = 8
	var wg sync.WaitGroup
	paths := make([]string, peers)
	errs := make([]error, peers)
	for i := range peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths[i], errs[i] = engineGitSeedForBranch("concurrent-seed")
		}()
	}
	wg.Wait()
	for i := range peers {
		if errs[i] != nil || paths[i] == "" || paths[i] != paths[0] {
			t.Fatalf("concurrent seed %d = %q, %v; first = %q", i, paths[i], errs[i], paths[0])
		}
	}
	if got := strings.TrimSpace(runEngineGit(t, paths[0], "symbolic-ref", "--short", "HEAD")); got != "concurrent-seed" {
		t.Fatalf("concurrent seed branch = %q", got)
	}
}
