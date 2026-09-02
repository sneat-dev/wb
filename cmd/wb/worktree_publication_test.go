package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// The exit code is what an agent branches on. An unpublished HEAD is exactly
// the state the operator already believed was fine, so it must change the exit
// code — printing a warning above a cheerful "ok:" line would repeat the
// original failure, where a success message covered orphaned work.
func TestWorktreeGuardPublishedExitsWithFindingsAndNamesTheRemedy(t *testing.T) {
	worktree, root := newPublicationWorktree(t, "guard-published-findings")
	publicationGit(t, worktree, "push", "-u", "origin", "guard-published-findings")
	if err := os.WriteFile(filepath.Join(worktree, "unpushed.txt"), []byte("not on origin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	publicationGit(t, worktree, "add", "unpushed.txt")
	publicationGit(t, worktree, "commit", "-m", "work that must not be lost")

	previous := projectsRoot
	projectsRoot = root
	t.Cleanup(func() { projectsRoot = previous })

	var stdout, stderr bytes.Buffer
	command := newWorktreeGuardCmd()
	command.SetArgs([]string{worktree, "--published"})
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SilenceUsage = true

	err := command.Execute()
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitFindings {
		t.Fatalf("error = %v, want an exitFindings error", err)
	}
	if !strings.Contains(stderr.String(), "unpublished:") || !strings.Contains(stderr.String(), "git push origin HEAD:guard-published-findings") {
		t.Fatalf("stderr = %q, want the finding and its remedy", stderr.String())
	}
}

// The complement: a pushed worktree exits 0 and says so, with the remote SHA
// it verified against. Without this, the finding path could pass because the
// check was broken for everyone.
func TestWorktreeGuardPublishedConfirmsAPushedWorktree(t *testing.T) {
	worktree, root := newPublicationWorktree(t, "guard-published-ok")
	publicationGit(t, worktree, "push", "-u", "origin", "guard-published-ok")

	previous := projectsRoot
	projectsRoot = root
	t.Cleanup(func() { projectsRoot = previous })

	var stdout, stderr bytes.Buffer
	command := newWorktreeGuardCmd()
	command.SetArgs([]string{worktree, "--published"})
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SilenceUsage = true

	if err := command.Execute(); err != nil {
		t.Fatalf("guard --published on a pushed worktree = %v (stderr %q)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "published at origin/guard-published-ok") {
		t.Fatalf("stdout = %q, want the verified remote ref", stdout.String())
	}
}

// newPublicationWorktree builds a real remote, canonical clone, and managed
// worktree, and returns the worktree path plus the projects root.
func newPublicationWorktree(t *testing.T, task string) (string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv(wbhome.EnvOverride, filepath.Join(root, "wb-home"))
	t.Setenv(wbhome.EnvMigrationCompat, "")

	remote := filepath.Join(root, "remote.git")
	publicationGit(t, root, "init", "--bare", "--initial-branch=main", remote)
	projects := filepath.Join(root, "projects")
	canonical := filepath.Join(projects, "acme", "app")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	publicationGit(t, root, "clone", remote, canonical)
	publicationGit(t, canonical, "config", "user.name", "WB Test")
	publicationGit(t, canonical, "config", "user.email", "wb@example.test")
	if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("# app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	publicationGit(t, canonical, "add", "README.md")
	publicationGit(t, canonical, "commit", "-m", "initial")
	publicationGit(t, canonical, "push", "-u", "origin", "main")

	resolved, err := filepath.EvalSymlinks(projects)
	if err != nil {
		t.Fatal(err)
	}
	created, err := worktrees.Create(context.Background(), []string{"acme/app"}, worktrees.CreateOptions{
		ProjectsRoot: resolved, Operation: task,
		WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return created[0].WorktreeDir, resolved
}

func publicationGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
