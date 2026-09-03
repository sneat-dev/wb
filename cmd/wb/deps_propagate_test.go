package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
)

// AC: merge-refuses-while-a-link-is-live — `wb worktree merge` refuses before
// any push, names the link and the command that clears it.
func TestWorktreeMergeRefusesAWorktreeWithARecordedLink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WB_HOME", home)
	worktree := t.TempDir()
	store := streams.OpenAt(filepath.Join(home, "streams"))
	if _, err := store.Create(streams.Stream{
		Name: "linked",
		Members: []streams.Member{{
			Repository: "acme/app", Role: streams.RoleConsumer, Worktree: worktree,
			Links: []streams.Link{{
				Library: "/work/library", LibraryRepository: "acme/library",
				Mechanism: streams.MechanismGoWork, Identity: "github.com/acme/library/backend",
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"worktree", "merge", worktree, "--route", "auto", "--non-interactive"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d (refusal); stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "github.com/acme/library/backend") {
		t.Errorf("refusal does not name the link: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "wb deps propagate local /work/library --to "+worktree+" --undo") {
		t.Errorf("refusal does not name the clearing command: %s", stderr.String())
	}
}

// The second, independent signal: a hand-written go.work with a use entry and
// no stream record still refuses.
func TestWorktreeMergeRefusesAHandWrittenGoWork(t *testing.T) {
	t.Setenv("WB_HOME", t.TempDir())
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "go.work"), []byte("go 1.27\n\nuse (\n\t./backend\n\t/elsewhere/library\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"worktree", "merge", "prepare", worktree, "--non-interactive"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "/elsewhere/library") {
		t.Errorf("refusal does not name the use entry: %s", stderr.String())
	}
}

// `wb deps propagate local` links a Go consumer to a library worktree without
// touching one tracked file, and its JSON report carries the content hash and
// the version the link replaced.
func TestDepsPropagateLocalLinksAGoConsumer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WB_HOME", home)
	base := t.TempDir()
	library := initGitRepository(t, filepath.Join(base, "library"), map[string]string{
		"backend/go.mod": "module github.com/acme/library/backend\n\ngo 1.27\n",
	})
	consumer := initGitRepository(t, filepath.Join(base, "app"), map[string]string{
		"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n",
	})
	store := streams.OpenAt(filepath.Join(home, "streams"))
	if _, err := store.Create(streams.Stream{
		Name: "cli",
		Members: []streams.Member{
			{Repository: "acme/library", Role: streams.RoleLibrary, Worktree: library},
			{Repository: "acme/app", Role: streams.RoleConsumer, Worktree: consumer},
		},
	}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"deps", "propagate", "local", library, "--to", consumer,
		"--format", "json", "--non-interactive",
	}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var result struct {
		ContentHash string `json:"content_hash"`
		Dirty       bool   `json:"dirty"`
		Consumers   []struct {
			Links []struct {
				Identity        string `json:"identity"`
				Mechanism       string `json:"mechanism"`
				PreviousVersion string `json:"previous_version"`
			} `json:"links"`
		} `json:"consumers"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("parse %q: %v", stdout.String(), err)
	}
	if result.ContentHash == "" {
		t.Fatal("no content hash reported")
	}
	if len(result.Consumers) != 1 || len(result.Consumers[0].Links) != 1 {
		t.Fatalf("result = %#v", result)
	}
	link := result.Consumers[0].Links[0]
	if link.Identity != "github.com/acme/library/backend" || link.Mechanism != "go.work" || link.PreviousVersion != "v0.4.0" {
		t.Fatalf("link = %#v", link)
	}
	if _, err := os.Stat(filepath.Join(consumer, "go.work")); err != nil {
		t.Fatalf("go.work was not written: %v", err)
	}
	if status := gitPorcelain(t, consumer); status != "" {
		t.Fatalf("the consumer is no longer clean:\n%s", status)
	}

	// The link now refuses a merge, and undo clears it.
	var mergeOut, mergeErr bytes.Buffer
	if code := run([]string{"worktree", "merge", consumer, "--route", "auto", "--non-interactive"}, &mergeOut, &mergeErr); code != exitUsage {
		t.Fatalf("merge exit code = %d, want a refusal; stderr=%s", code, mergeErr.String())
	}
	var undoOut, undoErr bytes.Buffer
	if code := run([]string{"deps", "propagate", "local", "--to", consumer, "--undo", "--non-interactive"}, &undoOut, &undoErr); code != exitOK {
		t.Fatalf("undo exit code = %d; stderr=%s", code, undoErr.String())
	}
	if _, err := os.Stat(filepath.Join(consumer, "go.work")); !os.IsNotExist(err) {
		t.Errorf("go.work survived undo: %v", err)
	}
}

func TestDepsPropagateLocalRequiresAConsumer(t *testing.T) {
	t.Setenv("WB_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"deps", "propagate", "local", t.TempDir(), "--non-interactive"}, &stdout, &stderr); code == exitOK {
		t.Fatal("propagating to nothing succeeded")
	}
	if !strings.Contains(stderr.String(), "--to") {
		t.Errorf("stderr = %q, want the missing flag named", stderr.String())
	}
}

// initGitRepository creates a committed repository at root with the given
// files, so the local-link path is exercised against real Git rather than a
// fake.
func initGitRepository(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, contents := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "--initial-branch=main", "."},
		{"add", "."},
		{"commit", "-m", "base"},
	} {
		command := exec.Command("git", args...)
		command.Dir = root
		command.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=wb", "GIT_AUTHOR_EMAIL=wb@example.test",
			"GIT_COMMITTER_NAME=wb", "GIT_COMMITTER_EMAIL=wb@example.test",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		if output, err := command.CombinedOutput(); err != nil {
			t.Skipf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
	}
	return root
}

func gitPorcelain(t *testing.T, root string) string {
	t.Helper()
	command := exec.Command("git", "status", "--porcelain")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v: %s", err, output)
	}
	return strings.TrimSpace(string(output))
}
