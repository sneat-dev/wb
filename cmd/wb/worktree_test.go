package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestWorktreeHelpExplainsCanonicalAndCentralLayout(t *testing.T) {
	command := newWorktreeCreateCmd()
	for _, wanted := range []string{
		"canonical clone must be clean",
		"pulls",
		"<wb-home>/worktrees/<task>/<owner>/<repository>",
		"WB_HOME",
		"--resume",
	} {
		if !strings.Contains(command.Long, wanted) {
			t.Errorf("worktree create help does not mention %q", wanted)
		}
	}
}

func TestWorktreeCleanupDefaultsToSafeDryRun(t *testing.T) {
	command := newWorktreeCleanupCmd()
	olderThan := command.Flags().Lookup("older-than")
	if olderThan == nil || olderThan.DefValue != (24*time.Hour).String() {
		t.Fatalf("--older-than default = %#v, want %s", olderThan, 24*time.Hour)
	}
	apply := command.Flags().Lookup("apply")
	if apply == nil || apply.DefValue != "false" {
		t.Fatalf("--apply default = %#v, want false", apply)
	}
	if command.Flags().Lookup("report-dir") == nil {
		t.Fatal("cleanup command has no --report-dir")
	}
	if err := command.Args(command, nil); err == nil || !strings.Contains(err.Error(), "--all-merged") {
		t.Fatalf("cleanup without selection error = %v", err)
	}
}

func TestWorktreeLifecycleHelpExplainsNetworkAndCleanupSafety(t *testing.T) {
	list := newWorktreeListCmd()
	for _, wanted := range []string{"only local Git data", "--github", "exact-head"} {
		if !strings.Contains(list.Long, wanted) {
			t.Errorf("worktree list help does not mention %q", wanted)
		}
	}
	cleanup := newWorktreeCleanupCmd()
	for _, wanted := range []string{"default is a dry-run", "recorded head match", "force-with-lease"} {
		if !strings.Contains(cleanup.Long, wanted) {
			t.Errorf("worktree cleanup help does not mention %q", wanted)
		}
	}
}

func TestWorktreeCreateRejectsTraversalBeforeRefreshingExternalHooks(t *testing.T) {
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	canonical := filepath.Join(projects, "acme", "app")
	external := filepath.Join(root, "evil")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(wbhome.EnvOverride, filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	prepareStaleManagedHook := func(repository string) (string, []byte) {
		t.Helper()
		if err := os.MkdirAll(repository, 0o755); err != nil {
			t.Fatal(err)
		}
		command := exec.Command("git", "-C", repository, "init", "-b", "main")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("init %s: %v\n%s", repository, err, output)
		}
		if _, err := hooks.Apply(hooks.ApplyOptions{
			RepoPath: repository, WBExecutable: hookExecutable(), ProjectsRoot: projects,
		}); err != nil {
			t.Fatal(err)
		}
		preCommit := filepath.Join(repository, ".git", "wb-hooks", "pre-commit")
		if err := os.Chmod(preCommit, 0o600); err != nil {
			t.Fatal(err)
		}
		contents, err := os.ReadFile(preCommit)
		if err != nil {
			t.Fatal(err)
		}
		return preCommit, contents
	}
	canonicalPreCommit, canonicalBefore := prepareStaleManagedHook(canonical)
	externalPreCommit, externalBefore := prepareStaleManagedHook(external)

	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--projects-root", projects, "worktree", "create", "traversal", "acme/app", "../evil"}, &stdout, &stderr); code == exitOK {
		t.Fatalf("traversal create unexpectedly succeeded: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "must be owner/name") {
		t.Fatalf("traversal rejection = %s", stderr.String())
	}
	for _, hook := range []struct {
		name   string
		path   string
		before []byte
	}{
		{name: "canonical", path: canonicalPreCommit, before: canonicalBefore},
		{name: "external", path: externalPreCommit, before: externalBefore},
	} {
		after, err := os.ReadFile(hook.path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(hook.before) {
			t.Fatalf("traversal input rewrote %s managed hook", hook.name)
		}
		info, err := os.Stat(hook.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("traversal input refreshed %s managed hook mode to %o", hook.name, info.Mode().Perm())
		}
	}
}
