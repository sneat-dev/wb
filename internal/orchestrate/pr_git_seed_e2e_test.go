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

func TestE2EPullRequestGitSeedKeepsModuleRecipeAndCloneStateSeparate(t *testing.T) {
	t.Parallel()
	seed := pullRequestGitSeed(t)
	if seed == engineGitSeed(t, "main") {
		t.Fatal("module and dependency recipes shared a seed")
	}
	if filepath.Dir(seed) != engineGitSeeds.root {
		t.Fatalf("seed %q does not belong to TestMain root", seed)
	}
	if got := runEngineGit(t, seed, "show", "HEAD:go.mod"); got != "module example.test/app\n\ngo 1.24\n" {
		t.Fatalf("module bytes = %q", got)
	}
	if got := strings.TrimSpace(runEngineGit(t, seed, "symbolic-ref", "--short", "HEAD")); got != "main" {
		t.Fatalf("seed branch = %q", got)
	}
	const peers = 8
	var wg sync.WaitGroup
	paths := make([]string, peers)
	for i := range peers {
		wg.Add(1)
		go func() { defer wg.Done(); paths[i] = pullRequestGitSeed(t) }()
	}
	wg.Wait()
	for _, path := range paths {
		if path != seed {
			t.Fatalf("concurrent seed = %q, want %q", path, seed)
		}
	}
	root := t.TempDir()
	first, second := filepath.Join(root, "first.git"), filepath.Join(root, "second")
	runEngineGit(t, root, "clone", "--bare", "--no-hardlinks", seed, first)
	runEngineGit(t, root, "clone", "--no-hardlinks", first, second)
	head := strings.TrimSpace(runEngineGit(t, seed, "rev-parse", "HEAD"))
	object := filepath.Join("objects", head[:2], head[2:])
	a, ea := os.Stat(filepath.Join(seed, ".git", object))
	b, eb := os.Stat(filepath.Join(first, object))
	c, ec := os.Stat(filepath.Join(second, ".git", object))
	if ea != nil || eb != nil || ec != nil || os.SameFile(a, b) || os.SameFile(b, c) {
		t.Fatalf("private object storage: %v, %v, %v", ea, eb, ec)
	}
	runEngineGit(t, first, "branch", "first-only", "main")
	runEngineGit(t, first, "config", "user.name", "Private Bare")
	runEngineGit(t, second, "config", "user.name", "Private Checkout")
	for _, other := range []string{seed, second} {
		if got := strings.TrimSpace(runEngineGit(t, other, "branch", "--list", "first-only")); got != "" {
			t.Fatalf("private ref reached %s: %q", other, got)
		}
	}
	if got := strings.TrimSpace(runEngineGit(t, seed, "config", "user.name")); got != "WB Test" {
		t.Fatalf("seed identity = %q", got)
	}
	writeEngineFile(t, filepath.Join(second, "go.mod"), "private edit\n")
	if got := runEngineGit(t, seed, "show", "HEAD:go.mod"); got != "module example.test/app\n\ngo 1.24\n" {
		t.Fatalf("private content mutation reached seed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(second, ".git", "objects", "info", "alternates")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clone uses alternates: %v", err)
	}
}

func TestE2EPullRequestGitSeedFailedInitializationLeavesNoPartialRecipe(t *testing.T) {
	t.Parallel()
	if _, err := initializePullRequestGitSeed("", os.WriteFile); err == nil {
		t.Fatal("missing TestMain root accepted")
	}
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := initializePullRequestGitSeed(blockedRoot, os.WriteFile); err == nil {
		t.Fatal("regular-file root accepted")
	}
	failure := errors.New("test: module write failed")
	for _, row := range []struct {
		name  string
		write func(string, []byte, os.FileMode) error
		want  error
	}{
		{"write refusal", func(string, []byte, os.FileMode) error { return failure }, failure},
		{"native Git init refusal", func(path string, contents []byte, mode os.FileMode) error {
			if err := os.WriteFile(path, contents, mode); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(filepath.Dir(path), ".git"), []byte("not a repository\n"), 0o644)
		}, nil},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			seed, err := initializePullRequestGitSeed(root, row.write)
			if seed != "" || err == nil || (row.want != nil && !errors.Is(err, row.want)) {
				t.Fatalf("failed initialization = %q, %v", seed, err)
			}
			if row.want == nil && !strings.Contains(err.Error(), "git init -b main in engine seed:") {
				t.Fatalf("failure did not reach native Git init: %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed recipe leaked: %v, %v", entries, err)
			}
			seed, err = initializePullRequestGitSeed(root, os.WriteFile)
			if err != nil {
				t.Fatal(err)
			}
			if got := runEngineGit(t, seed, "show", "HEAD:go.mod"); got != "module example.test/app\n\ngo 1.24\n" {
				t.Fatalf("retried recipe = %q", got)
			}
		})
	}
}
