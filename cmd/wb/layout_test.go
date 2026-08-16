package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLayoutAuditAndCleanCLI(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := filepath.Join(root, "acme", "app")
	initOriginRepository(t, canonical, "acme/app")
	top := filepath.Join(root, "app")
	clonePath(t, canonical, top)
	runGit(t, top, "remote", "set-url", "origin", "git@github.com:acme/app.git")

	audit := runWB(t, "layout", "audit", "--projects-root", root, "--format", "json")
	if audit.exitCode != exitFindings {
		t.Fatalf("audit exit = %d, want findings; stderr=%s stdout=%s", audit.exitCode, audit.stderr, audit.stdout)
	}
	if !strings.Contains(audit.stdout, "top_level") && !strings.Contains(audit.stdout, `"kind": "top_level"`) {
		t.Fatalf("audit missing top_level: %s", audit.stdout)
	}

	clean := runWB(t, "layout", "clean", "--projects-root", root, "--format", "json")
	if clean.exitCode != exitOK {
		t.Fatalf("clean dry-run exit = %d stderr=%s", clean.exitCode, clean.stderr)
	}
	var planned struct {
		Actions []struct {
			Status string `json:"status"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(clean.stdout), &planned); err != nil {
		t.Fatalf("decode clean: %v\n%s", err, clean.stdout)
	}
	if len(planned.Actions) != 1 || planned.Actions[0].Status != "planned" {
		t.Fatalf("planned actions = %+v", planned.Actions)
	}
	if _, err := os.Stat(top); err != nil {
		t.Fatal("dry-run must keep top-level clone")
	}

	applied := runWB(t, "layout", "clean", "--projects-root", root, "--apply", "--format", "json")
	if applied.exitCode != exitOK {
		t.Fatalf("clean apply exit = %d stderr=%s stdout=%s", applied.exitCode, applied.stderr, applied.stdout)
	}
	if _, err := os.Stat(top); !os.IsNotExist(err) {
		t.Fatal("apply must remove top-level clone")
	}
	if _, err := os.Stat(canonical); err != nil {
		t.Fatal("canonical clone must remain")
	}
}

func initOriginRepository(t *testing.T, path, slug string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, path, "init", "-b", "main")
	runGit(t, path, "config", "user.email", "wb@example.test")
	runGit(t, path, "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte(slug+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, path, "add", ".")
	runGit(t, path, "commit", "-m", "init")
	runGit(t, path, "remote", "add", "origin", "git@github.com:"+slug+".git")
}

func clonePath(t *testing.T, source, dest string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Dir(dest), "clone", source, dest)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
