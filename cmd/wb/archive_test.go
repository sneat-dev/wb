package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestArchiveCleanCLI exercises the real built binary against two local
// clones: one archived and clean (deletable), one archived but holding an
// untracked file. It asserts that a dry run itemizes the untracked path,
// ordinary --apply preserves it, and the narrow explicit flag deletes only
// that planned path before pruning the clone.
func TestArchiveCleanCLI(t *testing.T) {
	// Not t.Parallel(): this test uses t.Setenv for PATH and WB_HOME, which
	// Go's testing package forbids combining with parallel execution.
	root := t.TempDir()
	remotesRoot := t.TempDir()

	clean := initArchivableClone(t, root, remotesRoot, "acme", "clean-repo")
	dirty := initArchivableClone(t, root, remotesRoot, "acme", "dirty-repo")
	if err := os.WriteFile(filepath.Join(dirty, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	installArchivedFakeGh(t)
	home := t.TempDir()
	t.Setenv("WB_HOME", home)

	plan := runWB(t, "archive", "clean", "--projects-root", root, "--filter", "acme")
	if plan.exitCode != exitOK {
		t.Fatalf("dry-run exit = %d stderr=%s stdout=%s", plan.exitCode, plan.stderr, plan.stdout)
	}
	if !strings.Contains(plan.stdout, "would delete acme/clean-repo") {
		t.Errorf("dry-run report missing deletable clone: %s", plan.stdout)
	}
	if !strings.Contains(plan.stdout, "skipped      acme/dirty-repo") || !strings.Contains(plan.stdout, "untracked") {
		t.Errorf("dry-run report missing refused clone with reason: %s", plan.stdout)
	}
	if !strings.Contains(plan.stdout, "untracked file untracked.txt (4 bytes)") {
		t.Errorf("dry-run did not itemize untracked path and size: %s", plan.stdout)
	}
	if _, err := os.Stat(clean); err != nil {
		t.Fatal("dry-run deleted the deletable clone")
	}
	if _, err := os.Stat(dirty); err != nil {
		t.Fatal("dry-run deleted the dirty clone")
	}

	applied := runWB(t, "archive", "clean", "--projects-root", root, "--filter", "acme", "--apply")
	if applied.exitCode != exitOK {
		t.Fatalf("apply exit = %d stderr=%s stdout=%s", applied.exitCode, applied.stderr, applied.stdout)
	}
	if !strings.Contains(applied.stdout, "deleted      acme/clean-repo") {
		t.Errorf("apply report missing deletion: %s", applied.stdout)
	}
	if !strings.Contains(applied.stdout, "skipped      acme/dirty-repo") {
		t.Errorf("apply report missing refusal: %s", applied.stdout)
	}
	if _, err := os.Stat(clean); !os.IsNotExist(err) {
		t.Fatal("apply did not delete the clean archived clone")
	}
	if _, err := os.Stat(dirty); err != nil {
		t.Fatal("apply deleted the dirty clone")
	}

	authorised := runWB(t, "archive", "clean", "--projects-root", root, "--filter", "dirty-repo", "--apply", "--delete-untracked")
	if authorised.exitCode != exitOK {
		t.Fatalf("authorised untracked deletion exit = %d stderr=%s stdout=%s", authorised.exitCode, authorised.stderr, authorised.stdout)
	}
	if !strings.Contains(authorised.stdout, "deleted      acme/dirty-repo") || !strings.Contains(authorised.stdout, "receipt ") {
		t.Errorf("authorised deletion report lacks deletion receipt: %s", authorised.stdout)
	}
	if _, err := os.Stat(dirty); !os.IsNotExist(err) {
		t.Fatal("--apply --delete-untracked did not prune the authorised archived clone")
	}
}

func initArchivableClone(t *testing.T, projectsRoot, remotesRoot, owner, name string) string {
	t.Helper()
	seed := filepath.Join(remotesRoot, owner, name+"-seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "init", "-b", "main")
	runGit(t, seed, "config", "user.email", "wb@example.test")
	runGit(t, seed, "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte(owner+"/"+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", ".")
	runGit(t, seed, "commit", "-m", "init")

	remote := filepath.Join(remotesRoot, owner, name+".git")
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Dir(remote), "clone", "--bare", seed, remote)

	canonical := filepath.Join(projectsRoot, owner, name)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Dir(canonical), "clone", remote, canonical)
	return canonical
}

// installArchivedFakeGh puts a fake `gh` on PATH that reports every
// repository as archived, so this test never depends on network access or
// real GitHub credentials.
func installArchivedFakeGh(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	content := "#!/bin/sh\nset -eu\nprintf 'true\\n'\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
