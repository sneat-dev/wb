package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initGCFixture builds a real canonical clone with one WB worktree whose branch
// is dirty, so the sweep has exactly one thing to refuse and nothing to remove.
func initGCFixture(t *testing.T) (projectsRoot, home, worktree string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "wb-home")
	remote := filepath.Join(root, "remote.git")
	projectsRoot = filepath.Join(root, "projects")
	canonical := filepath.Join(projectsRoot, "acme", "app")
	gcGit(t, root, "init", "--bare", "--initial-branch=main", remote)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	gcGit(t, root, "clone", remote, canonical)
	gcGit(t, canonical, "config", "user.email", "wb-test@example.com")
	gcGit(t, canonical, "config", "user.name", "wb-test")
	if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("# app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gcGit(t, canonical, "add", "README.md")
	gcGit(t, canonical, "commit", "-m", "initial")
	gcGit(t, canonical, "push", "-u", "origin", "main")

	t.Setenv("WB_HOME", home)
	installGCFakeGh(t)
	prompt := writeOriginalPromptFixture(t, "gc cli fixture")
	created := runWB(t, "worktree", "create", "gc-cli", "acme/app",
		"--projects-root", projectsRoot, "--model", "unknown",
		"--mode", "manual", "--initiator", "wb-test",
		"--original-prompt-file", prompt)
	if created.exitCode != exitOK {
		t.Fatalf("create exit = %d stderr=%s stdout=%s", created.exitCode, created.stderr, created.stdout)
	}
	worktree = filepath.Join(home, "worktrees", "gc-cli", "acme", "app")
	if err := os.WriteFile(filepath.Join(worktree, "wip.txt"), []byte("in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return projectsRoot, home, worktree
}

func gcGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=wb-test", "GIT_AUTHOR_EMAIL=wb-test@example.com",
		"GIT_COMMITTER_NAME=wb-test", "GIT_COMMITTER_EMAIL=wb-test@example.com")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

// installGCFakeGh answers GitHub's commit index with "no pull request", which
// is the honest answer for a local-only fixture.
func installGCFakeGh(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\nprintf '[]'\n"
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestWorktreeGCCLIPlansAndRefuses(t *testing.T) {
	// Not t.Parallel(): t.Setenv for PATH and WB_HOME forbids it.
	projectsRoot, home, worktree := initGCFixture(t)

	// A stale terminal artefact under the task must be purged on the read path
	// itself, silently, and counted in the footer.
	stage := filepath.Join(home, "worktrees", "gc-cli", ".wb-retired-stage-6b0995eef65f84dace22d24df2644b32")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}

	plan := runWB(t, "worktree", "gc", "--projects-root", projectsRoot, "--skip-sizes")
	if plan.exitCode != exitFindings {
		t.Fatalf("a refused checkout must exit 1: exit=%d stdout=%s stderr=%s", plan.exitCode, plan.stdout, plan.stderr)
	}
	if !strings.Contains(plan.stdout, "dirty") || !strings.Contains(plan.stdout, "worktree has local changes") {
		t.Errorf("plan does not report the dirty checkout with its reason: %s", plan.stdout)
	}
	if !strings.Contains(plan.stdout, "resolve with: wb worktree abort gc-cli --apply") {
		t.Errorf("every refusal must name the sanctioned command: %s", plan.stdout)
	}
	if !strings.Contains(plan.stdout, "terminal artefacts purged") {
		t.Errorf("plan footer does not report purged artefacts: %s", plan.stdout)
	}
	if strings.Contains(plan.stderr, "info: inventory classified") {
		t.Errorf("a terminal artefact must never become a per-invocation log line: %s", plan.stderr)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Errorf("empty retired stage survived a gc read path: %v", err)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("a dry run removed the worktree: %v", err)
	}

	// Nothing changes with --apply: a dirty checkout is refused, not forced.
	applied := runWB(t, "worktree", "gc", "--projects-root", projectsRoot, "--skip-sizes", "--apply")
	if applied.exitCode != exitFindings {
		t.Fatalf("apply exit = %d stdout=%s stderr=%s", applied.exitCode, applied.stdout, applied.stderr)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("--apply removed a dirty checkout: %v", err)
	}
}

func TestWorktreeGCCLIEmitsAStableJSONPlan(t *testing.T) {
	projectsRoot, _, _ := initGCFixture(t)

	result := runWB(t, "worktree", "gc", "--projects-root", projectsRoot, "--skip-sizes", "--format", "json")
	if result.exitCode != exitFindings {
		t.Fatalf("exit = %d stderr=%s", result.exitCode, result.stderr)
	}
	var outcome struct {
		SchemaVersion int  `json:"schema_version"`
		Apply         bool `json:"apply"`
		Entries       []struct {
			Task              string   `json:"task"`
			Class             string   `json:"class"`
			Eligible          bool     `json:"eligible"`
			Reason            string   `json:"reason"`
			SanctionedCommand string   `json:"sanctioned_command"`
			Evidence          []string `json:"evidence"`
		} `json:"entries"`
		Reclaimable struct {
			ApparentBytes int64 `json:"apparent_bytes"`
			UnsharedBytes int64 `json:"unshared_bytes"`
		} `json:"reclaimable"`
		Totals map[string]int `json:"totals"`
	}
	if err := json.Unmarshal([]byte(result.stdout), &outcome); err != nil {
		t.Fatalf("decode gc JSON: %v\n%s", err, result.stdout)
	}
	if outcome.SchemaVersion != 1 || outcome.Apply {
		t.Fatalf("envelope identity = %#v", outcome)
	}
	if len(outcome.Entries) != 1 || outcome.Entries[0].Class != "dirty" || outcome.Entries[0].Eligible {
		t.Fatalf("entries = %#v", outcome.Entries)
	}
	if outcome.Entries[0].SanctionedCommand == "" || len(outcome.Entries[0].Evidence) == 0 {
		t.Fatalf("a refusal carries its sanctioned command and its evidence: %#v", outcome.Entries[0])
	}
	if outcome.Totals["refused"] != 1 {
		t.Fatalf("totals = %#v", outcome.Totals)
	}
}

// gc's help is the contract an agent reads before it reaches for raw Git.
func TestWorktreeGCHelpStatesItsEvidenceAndItsRefusals(t *testing.T) {
	command := newWorktreeGCCmd()
	for _, wanted := range []string{
		"--allow-residue",
		"--apply",
		"landed + residue",
		"commit identity",
		"detached-review",
		"no force flag",
		"apparent and unshared",
		"Work Logs and event logs are never touched",
	} {
		if !strings.Contains(command.Long, wanted) {
			t.Errorf("wb worktree gc help does not mention %q", wanted)
		}
	}
	if command.Flags().Lookup("force") != nil {
		t.Fatal("gc must expose no force flag")
	}
}
