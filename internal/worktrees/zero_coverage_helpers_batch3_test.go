package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestZeroCoverageHelpersBatch3ErrorMessages(t *testing.T) {
	t.Parallel()

	if got := (&pullRequestHeadMismatchError{message: "exact head changed"}).Error(); got != "exact head changed" {
		t.Fatalf("pull-request mismatch error = %q", got)
	}
	rename := (&RepositoryRenameMismatchError{
		Worktree: "/worktree", PathRepository: "old-name", CanonicalRepository: "new-name",
	}).Error()
	for _, detail := range []string{"/worktree", `"old-name"`, `"new-name"`} {
		if !strings.Contains(rename, detail) {
			t.Fatalf("repository rename mismatch error %q lacks %q", rename, detail)
		}
	}
}

func TestZeroCoverageHelpersBatch3CleanupWrappers(t *testing.T) {
	t.Parallel()

	missingRoot := filepath.Join(t.TempDir(), "missing-worktrees")
	if handle, err := acquireCleanupTaskAt(missingRoot, "coverage-task"); err == nil || handle != nil {
		if handle != nil {
			handle.close()
		}
		t.Fatalf("missing cleanup task = %#v, %v", handle, err)
	}

	reportDir := t.TempDir()
	path, err := writeCleanupReportInjected(CleanupOptions{
		Task: "coverage-task", Tasks: []string{"coverage-task", "second-task"}, ReportDir: reportDir,
	}, time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC), "plan", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("write multi-task cleanup report: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"tasks": [`) || !strings.Contains(string(raw), `"second-task"`) {
		t.Fatalf("multi-task cleanup report = %s", raw)
	}
}

func TestZeroCoverageHelpersBatch3GuardAndOwnerWrappers(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if _, err := locateGuardedAdoptedWorktree(context.Background(), root, wbhome.Layout{Home: t.TempDir()}, root); err == nil {
		t.Fatal("guarded adopted worktree without an active claim was accepted")
	}

	projectsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectsRoot, "github.com", "acme", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	owners, diagnostics := canonicalOwnerDirectories(projectsRoot)
	if len(diagnostics) != 0 || len(owners) != 1 || owners[0].Host != "github.com" || owners[0].Name != "acme" {
		t.Fatalf("canonical owners = %#v, diagnostics = %#v", owners, diagnostics)
	}
}

func TestZeroCoverageHelpersBatch3CleanWorktree(t *testing.T) {
	t.Parallel()

	const worktree = "/fixture/worktree"
	cleanRunner := runnertest.New(t)
	cleanRunner.ExpectArgv(
		[]string{"git", "-C", worktree, "status", "--porcelain=v1"},
		runner.Result{CombinedOutput: "\n"}, nil,
	)
	clean, err := cleanWorktree(withGitRunner(context.Background(), cleanRunner), worktree)
	if err != nil || !clean {
		t.Fatalf("clean worktree = %t, %v", clean, err)
	}

	dirtyRunner := runnertest.New(t)
	dirtyRunner.ExpectArgv(
		[]string{"git", "-C", worktree, "status", "--porcelain=v1"},
		runner.Result{CombinedOutput: " M changed.go\n"}, nil,
	)
	clean, err = cleanWorktree(withGitRunner(context.Background(), dirtyRunner), worktree)
	if err != nil || clean {
		t.Fatalf("dirty worktree = %t, %v", clean, err)
	}

	failedRunner := runnertest.New(t)
	failedRunner.ExpectArgv(
		[]string{"git", "-C", worktree, "status", "--porcelain=v1"},
		runner.Result{}, errors.New("injected status failure"),
	)
	if clean, err = cleanWorktree(withGitRunner(context.Background(), failedRunner), worktree); err == nil || clean {
		t.Fatalf("failed worktree status = %t, %v", clean, err)
	}
}
