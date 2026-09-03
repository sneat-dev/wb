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
	"github.com/spf13/cobra"
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

// MF-4. `wb worktree merge land` and `merge resume` take a RECEIPT, not a
// worktree path, and used to skip the live-link guard entirely — so preparing
// before linking and then landing the receipt pushed a linked worktree.
func TestWorktreeMergeLandRefusesALinkedWorktreeFromItsReceipt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WB_HOME", home)
	worktree := t.TempDir()
	store := streams.OpenAt(filepath.Join(home, "streams"))
	if _, err := store.Create(streams.Stream{
		Name: "linked", Phase: streams.PhaseOpen,
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
	receipt := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(receipt, []byte(`{"sources":[{"worktree":"`+worktree+`"}],"candidate":{"worktree":"`+worktree+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, verb := range []string{"land", "resume"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{"worktree", "merge", verb, receipt, "--non-interactive"}, &stdout, &stderr)
		if code != exitUsage {
			t.Fatalf("%s exit code = %d, want %d (refusal); stderr=%s", verb, code, exitUsage, stderr.String())
		}
		if !strings.Contains(stderr.String(), "github.com/acme/library/backend") {
			t.Errorf("%s refusal does not name the link: %s", verb, stderr.String())
		}
		if !strings.Contains(stderr.String(), "--undo") {
			t.Errorf("%s refusal does not name the clearing command: %s", verb, stderr.String())
		}
	}
}

// Every verb that pushes, lands or absorbs work must refuse a worktree holding
// a live local link — and must be discoverable as such.
//
// This walks the command tree rather than naming call sites, so a landing verb
// added later (wb stream absorb, in the local-integration rows) inherits the
// requirement the moment it declares the annotation, instead of relying on
// someone remembering to add a call. That is exactly how merge land/resume
// came to be on the landing surface without ever calling the guard.
func TestEveryLandingVerbRefusesALiveLink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WB_HOME", home)
	worktree := t.TempDir()
	store := streams.OpenAt(filepath.Join(home, "streams"))
	if _, err := store.Create(streams.Stream{
		Name: "linked", Phase: streams.PhaseOpen,
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
	receipt := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(receipt, []byte(`{"sources":[{"worktree":"`+worktree+`"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	guarded := landingGuardedCommands(newRootCmd())

	// The declared surface and the annotated surface must agree exactly. A
	// count-based backstop would pass an undeclared landing verb, which is the
	// hole `merge land`/`merge resume` fell through.
	for path, addressing := range landingSurface {
		declared, annotated := guarded[path]
		if !annotated {
			t.Errorf("%s is on the landing surface but does not declare %s, so it will never run the live-link guard", path, landingGuardAnnotation)
			continue
		}
		if declared != addressing {
			t.Errorf("%s declares addressing %q, want %q", path, declared, addressing)
		}
	}
	for path := range guarded {
		if _, listed := landingSurface[path]; !listed {
			t.Errorf("%s declares the landing guard but is not on the declared landing surface; add it to landingSurface", path)
		}
	}
	for path, addressing := range guarded {
		argument := worktree
		switch addressing {
		case landingGuardByReceipt:
			argument = receipt
		case landingGuardByPullRequest:
			// A pull-request landing names no worktree, so the guard resolves
			// them from the open stream that holds the repository. The stream
			// created above holds acme/app, so this refuses without any
			// network call — which is the point: a guard that has to reach
			// GitHub first fails differently when GitHub does.
			argument = "acme/app#1"
		}
		invocation := append(strings.Fields(strings.TrimPrefix(path, "wb ")), argument, "--non-interactive")
		var stdout, stderr bytes.Buffer
		code := run(invocation, &stdout, &stderr)
		if code != exitUsage {
			t.Errorf("%s exit code = %d, want %d (refusal); stderr=%s", path, code, exitUsage, stderr.String())
			continue
		}
		if !strings.Contains(stderr.String(), "live local link") {
			t.Errorf("%s did not refuse on the live link: %s", path, stderr.String())
		}
		if !strings.Contains(stderr.String(), "--undo") {
			t.Errorf("%s refusal does not name the clearing command: %s", path, stderr.String())
		}
	}
}

// landingGuardedCommands collects every command declaring the landing guard.
func landingGuardedCommands(root *cobra.Command) map[string]string {
	guarded := map[string]string{}
	var visit func(*cobra.Command)
	visit = func(parent *cobra.Command) {
		for _, command := range parent.Commands() {
			if addressing := command.Annotations[landingGuardAnnotation]; addressing != "" {
				guarded[command.CommandPath()] = addressing
			}
			visit(command)
		}
	}
	visit(root)
	return guarded
}
