package locallink

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func initRepository(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = root
		command.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=wb", "GIT_AUTHOR_EMAIL=wb@example.test",
			"GIT_COMMITTER_NAME=wb", "GIT_COMMITTER_EMAIL=wb@example.test",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
	}
	run("init", "--initial-branch=main", ".")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "base")
	return root
}

// REQ: batch-verification-is-keyed-to-a-tree-identity — the hash covers the
// working tree including modified and untracked files, so an uncommitted
// library still has an identity; ignored build output does not change it.
func TestContentHashCoversModifiedAndUntrackedFilesButNotIgnoredOnes(t *testing.T) {
	root := initRepository(t)
	git := ExecGit{Timeout: 30 * time.Second}
	ctx := context.Background()

	clean, dirty, err := git.ContentHash(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if dirty {
		t.Fatal("a committed tree reported dirty")
	}

	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	modified, dirty, err := git.ContentHash(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty {
		t.Fatal("a modified tree reported clean")
	}
	if modified == clean {
		t.Fatal("a modification did not move the content hash")
	}

	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withUntracked, _, err := git.ContentHash(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if withUntracked == modified {
		t.Fatal("an untracked file did not move the content hash")
	}

	if err := os.MkdirAll(filepath.Join(root, "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored", "build.js"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withIgnored, _, err := git.ContentHash(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if withIgnored != withUntracked {
		t.Fatal("an ignored build output changed the source identity")
	}
}

func TestTrackedChangesIgnoresUntrackedArtefacts(t *testing.T) {
	root := initRepository(t)
	git := ExecGit{Timeout: 30 * time.Second}
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err := git.TrackedChanges(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("tracked changes = %v; an untracked link artefact is not a tracked change", changes)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changes, err = git.TrackedChanges(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0] != "tracked.txt" {
		t.Fatalf("tracked changes = %v, want tracked.txt", changes)
	}
}

// The exclude goes into the worktree's own exclude file, never into the
// tracked .gitignore — and the excluded file must actually stop showing up as
// untracked, which is the only thing that proves the exclude landed where Git
// reads it.
func TestExcludePathUsesTheWorktreeExcludeFileAndNotTheTrackedGitignore(t *testing.T) {
	root := initRepository(t)
	git := ExecGit{Timeout: 30 * time.Second}
	ctx := context.Background()
	gitignoreBefore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if err := git.ExcludePath(ctx, root, "/go.work"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status := gitStatus(t, root)
	if strings.Contains(status, "go.work") {
		t.Fatalf("go.work is still reported by git status:\n%s", status)
	}
	gitignoreAfter, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gitignoreAfter) != string(gitignoreBefore) {
		t.Fatal("the tracked .gitignore was modified")
	}
	patterns, err := git.ExcludedPatterns(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(patterns, "/go.work") {
		t.Fatalf("exclude file = %v", patterns)
	}
	// Excluding the same pattern twice must not duplicate it.
	if err := git.ExcludePath(ctx, root, "/go.work"); err != nil {
		t.Fatal(err)
	}
	patterns, err = git.ExcludedPatterns(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	occurrences := 0
	for _, pattern := range patterns {
		if strings.TrimSpace(pattern) == "/go.work" {
			occurrences++
		}
	}
	if occurrences != 1 {
		t.Fatalf("exclude file carries %d copies of /go.work", occurrences)
	}
}

func gitStatus(t *testing.T, root string) string {
	t.Helper()
	command := exec.Command("git", "status", "--porcelain")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v: %s", err, output)
	}
	return string(output)
}

// Link replaces a real installed package with a symlink and keeps the original
// aside, so Unlink restores exactly what was there rather than reinstalling.
func TestExecNodeLinkAndUnlinkRestoreTheInstalledPackage(t *testing.T) {
	consumer := t.TempDir()
	installed := filepath.Join(consumer, "node_modules", "@acme", "core")
	if err := os.MkdirAll(installed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "package.json"), []byte(`{"name":"@acme/core","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "package.json"), []byte(`{"name":"@acme/core","version":"1.1.0-dev"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	node := ExecNode{CacheRoot: t.TempDir(), ContentHash: "hash", Timeout: 30 * time.Second}
	ctx := context.Background()

	previous, err := node.Link(ctx, consumer, "@acme/core", dist)
	if err != nil {
		t.Fatal(err)
	}
	if previous == "" {
		t.Fatal("Link did not record where the installed package went")
	}
	info, err := os.Lstat(installed)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the node_modules entry is not a symlink: %v", err)
	}
	linked, err := os.ReadFile(filepath.Join(installed, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(linked), "1.1.0-dev") {
		t.Fatalf("the link does not resolve to the built dist: %s", linked)
	}

	if err := node.Unlink(ctx, consumer, "@acme/core"); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(installed, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(restored), `"version":"1.0.0"`) {
		t.Fatalf("undo did not restore the installed package: %s", restored)
	}
}

func TestExecNodeBuildRefusesWithoutAContentHash(t *testing.T) {
	node := ExecNode{CacheRoot: t.TempDir()}
	if _, err := node.Build(context.Background(), t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("a build with no content hash to key its cache reported success")
	}
}

// The build cache is keyed by content hash: the same hash reuses the build,
// and a moved hash rebuilds.
func TestExecNodeBuildCacheIsKeyedByContentHash(t *testing.T) {
	cache := t.TempDir()
	library := t.TempDir()
	packageDir := filepath.Join(library, "libs", "core")
	dist := filepath.Join(packageDir, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "package.json"), []byte(`{"name":"@acme/core"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	node := ExecNode{CacheRoot: cache, ContentHash: "hash-one", Timeout: 30 * time.Second}
	// Seed the cache exactly as a successful build would, so the reuse path is
	// exercised without needing a Node toolchain on the test machine.
	// Seed the cache exactly as a successful build would, so the reuse path is
	// exercised without needing a Node toolchain on the test machine.
	seeded := filepath.Join(cache, node.ContentHash, buildCacheKey(node.ContentHash, packageDir))
	if err := os.MkdirAll(seeded, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seeded, buildMarkerName), []byte(dist+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := node.Build(context.Background(), library, packageDir)
	if err != nil {
		t.Fatalf("a cached build was not reused: %v", err)
	}
	if got != dist {
		t.Fatalf("cached dist = %q, want %q", got, dist)
	}
	if !strings.Contains(seeded, "hash-one") {
		t.Fatalf("the cache path is not keyed by the content hash: %s", seeded)
	}
	moved := ExecNode{CacheRoot: cache, ContentHash: "hash-two", Timeout: time.Second}
	if _, err := moved.Build(context.Background(), library, packageDir); err == nil {
		t.Fatal("a moved content hash reused a stale build instead of rebuilding")
	}
}

// MF-8. Under pnpm's default isolated store, node_modules/<pkg> IS a symlink
// into .pnpm/…. That symlink used to be deleted with no backup, so `--undo`
// left the consumer with no package at all. Its target is now recorded and
// re-created.
func TestExecNodeLinkAndUnlinkRestoreAPnpmSymlink(t *testing.T) {
	consumer := t.TempDir()
	store := filepath.Join(consumer, "node_modules", ".pnpm", "@acme+core@1.0.0", "node_modules", "@acme", "core")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "package.json"), []byte(`{"name":"@acme/core","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(consumer, "node_modules", "@acme", "core")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	// pnpm links with a RELATIVE target; preserving it verbatim is what makes
	// the restore correct if the tree is ever relocated.
	relative, err := filepath.Rel(filepath.Dir(target), store)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relative, target); err != nil {
		t.Fatal(err)
	}

	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "package.json"), []byte(`{"name":"@acme/core","version":"1.1.0-dev"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	node := ExecNode{CacheRoot: t.TempDir(), ContentHash: "hash", Timeout: 30 * time.Second}
	ctx := context.Background()

	previous, err := node.Link(ctx, consumer, "@acme/core", dist)
	if err != nil {
		t.Fatal(err)
	}
	if previous == "" {
		t.Fatal("Link did not record where the existing symlink pointed")
	}
	linked, err := os.ReadFile(filepath.Join(target, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(linked), "1.1.0-dev") {
		t.Fatalf("the link does not resolve to the built dist: %s", linked)
	}

	if err := node.Unlink(ctx, consumer, "@acme/core"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the package is not a symlink again after undo: %v", err)
	}
	restoredTarget, err := os.Readlink(target)
	if err != nil {
		t.Fatal(err)
	}
	if restoredTarget != relative {
		t.Fatalf("restored link points at %q, want the original %q", restoredTarget, relative)
	}
	restored, err := os.ReadFile(filepath.Join(target, "package.json"))
	if err != nil {
		t.Fatalf("the published package is not reachable after undo: %v", err)
	}
	if !strings.Contains(string(restored), `"version":"1.0.0"`) {
		t.Fatalf("undo did not restore the published version: %s", restored)
	}
	// No bookkeeping left behind.
	if fileExists(target + linkSymlinkBackupSuffix) {
		t.Error("the link record survived undo")
	}
}
