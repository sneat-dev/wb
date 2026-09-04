package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOriginSlugUsesHostedRemotePath(t *testing.T) {
	repo := initRepo(t)
	git(t, repo, "remote", "add", "origin", "git@github.com:acme/app.git")
	if got := originSlug(repo); got != "acme/app" {
		t.Fatalf("originSlug = %q, want acme/app from the hosted remote", got)
	}
}

func TestOriginSlugDoesNotReadALocalPathRemoteAsASlug(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	bare := filepath.Join(root, "hr2", "origin.git")
	canonical := filepath.Join(root, "projects", "acme", "app")
	if err := os.MkdirAll(filepath.Dir(bare), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "--bare", "--initial-branch=main", bare)
	git(t, root, "clone", bare, canonical)
	git(t, canonical, "config", "user.name", "WB Tests")
	git(t, canonical, "config", "user.email", "wb-tests@example.invalid")
	mustWrite(t, filepath.Join(canonical, "README.md"), "test\n")
	git(t, canonical, "add", "README.md")
	git(t, canonical, "commit", "-m", "initial")
	if got := originSlug(canonical); got != "acme/app" {
		t.Fatalf("originSlug = %q, want acme/app from the checkout layout, not hr2/origin from the bare path", got)
	}
}

func TestOriginSlugOfAStagingWorktreeMatchesTheCanonicalClone(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	bare := filepath.Join(root, "remote.git")
	canonical := filepath.Join(root, "projects", "acme", "app")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "--bare", "--initial-branch=main", bare)
	git(t, root, "clone", bare, canonical)
	git(t, canonical, "config", "user.name", "WB Tests")
	git(t, canonical, "config", "user.email", "wb-tests@example.invalid")
	mustWrite(t, filepath.Join(canonical, "README.md"), "test\n")
	git(t, canonical, "add", "README.md")
	git(t, canonical, "commit", "-m", "initial")
	git(t, canonical, "push", "-u", "origin", "main")

	stage := filepath.Join(root, "home", ".wb", "worktrees", "upgrade", ".wb-stage-deadbeefdeadbeefdeadbeefdeadbeef", "checkout")
	if err := os.MkdirAll(filepath.Dir(stage), 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, canonical, "worktree", "add", "-b", "feature/upgrade", stage, "main")

	canonicalSlug := originSlug(canonical)
	stageSlug := originSlug(stage)
	if canonicalSlug != "acme/app" {
		t.Fatalf("canonical originSlug = %q, want acme/app", canonicalSlug)
	}
	if stageSlug != canonicalSlug {
		t.Fatalf("staging originSlug = %q, want the canonical slug %q so Landlock write roots still match during worktree create", stageSlug, canonicalSlug)
	}
	if strings.Contains(stageSlug, ".wb-stage-") {
		t.Fatalf("staging originSlug leaked the stage directory: %q", stageSlug)
	}

	t.Setenv("PATH", "/nonexistent")
	if got := originSlug(stage); got != "acme/app" {
		t.Fatalf("originSlug on a staging worktree spawned Git or missed the gitfile: %q", got)
	}
	if got := canonicalCheckoutSlug(stage); got != "acme/app" {
		t.Fatalf("canonicalCheckoutSlug without git = %q, want acme/app from the gitfile (hooks must not spawn git during worktree add)", got)
	}
}

func TestExecutionLayoutOfStagingWorktreeUsesCanonicalCheckoutIdentity(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	bare := filepath.Join(root, "remote.git")
	canonical := filepath.Join(root, "projects", "strongo", "selfupdate")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "--bare", "--initial-branch=main", bare)
	git(t, root, "clone", bare, canonical)
	git(t, canonical, "config", "user.name", "WB Tests")
	git(t, canonical, "config", "user.email", "wb-tests@example.invalid")
	mustWrite(t, filepath.Join(canonical, "README.md"), "test\n")
	git(t, canonical, "add", "README.md")
	git(t, canonical, "commit", "-m", "initial")
	git(t, canonical, "push", "-u", "origin", "main")
	git(t, canonical, "remote", "set-url", "origin", "git@github.com:strongo/cli-helpers.git")

	stage := filepath.Join(root, "home", ".wb", "worktrees", "upgrade", ".wb-stage-deadbeefdeadbeefdeadbeefdeadbeef", "checkout")
	if err := os.MkdirAll(filepath.Dir(stage), 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, canonical, "worktree", "add", "-b", "feature/upgrade", stage, "main")

	canonicalLayout, err := ResolveExecutionLayout(canonical, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(canonicalLayout.Root, filepath.Join("hook-runtime", "strongo", "selfupdate")) {
		t.Fatalf("canonical runtime root = %q, want canonical checkout identity", canonicalLayout.Root)
	}

	t.Setenv("PATH", "/nonexistent")
	stageLayout, err := ResolveExecutionLayout(stage, "")
	if err != nil {
		t.Fatalf("resolve staging execution layout without Git: %v", err)
	}
	if stageLayout.Root != canonicalLayout.Root {
		t.Fatalf("staging runtime root = %q, want canonical root %q", stageLayout.Root, canonicalLayout.Root)
	}
}

func TestFileURLRemoteIsNotAHostedSlug(t *testing.T) {
	if hostedRemote("file:///tmp/hr2/origin.git") {
		t.Fatal("file:// is a local path remote, not a hosted owner/repository")
	}
	if !hostedRemote("https://github.com/acme/app.git") {
		t.Fatal("https GitHub URL should be hosted")
	}
	if hostedRemote("/tmp/hr2/origin.git") {
		t.Fatal("absolute filesystem path should not be hosted")
	}
}
