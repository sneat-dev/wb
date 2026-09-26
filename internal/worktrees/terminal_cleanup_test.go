package worktrees

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // t.Setenv isolates WB home and cannot run with t.Parallel.
func TestFindTerminalCleanupProofUsesLatestExactAppliedReceipt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	const repository, task, target, branch = "acme/app", "retired-task", "cov/integration", "retired-task"
	worktree := filepath.Join(root, ".worktrees", task, "github.com", "acme", "app")
	canonical, err := CanonicalRepositoryPath(root, repository)
	if err != nil {
		t.Fatal(err)
	}
	result := CleanupResult{ListResult: ListResult{
		Task: task, Repository: repository, WorktreeDir: worktree, CanonicalDir: canonical,
		Branch: branch, Base: target, HeadSHA: "head", RemoteTargetSHA: "target",
		Clean: true, IntegratedAtOrigin: true,
	}, Eligible: true, Applied: true, WorktreeGone: true, BranchDeleted: true}
	start := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	write := func(at time.Time, report cleanupReport) string {
		t.Helper()
		dir := filepath.Join(root, ".wb", "reports", "worktree-cleanup", at.Format("20060102T150405.000000000Z"))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "cleanup.json")
		contents, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	lookup := func() (*TerminalCleanupProof, error) {
		t.Helper()
		return FindTerminalCleanupProof(root, repository, target, task, worktree, branch)
	}

	// A foreign task and an incomplete JSON receipt cannot authorize cleanup.
	write(start, cleanupReport{GeneratedAt: start, Phase: "applied", Apply: true, Task: "other", Results: []CleanupResult{result}})
	truncated := write(start.Add(time.Second), cleanupReport{GeneratedAt: start.Add(time.Second), Phase: "applied", Apply: true, Task: task, Results: []CleanupResult{result}})
	if err := os.WriteFile(truncated, []byte(`{"generated_at":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lookup(); err == nil || !strings.Contains(err.Error(), "no terminal cleanup receipt") {
		t.Fatalf("invalid evidence authorized cleanup: %v", err)
	}

	valid := write(start.Add(2*time.Second), cleanupReport{GeneratedAt: start.Add(2 * time.Second), Phase: "applied", Apply: true, Task: task, Results: []CleanupResult{result}})
	proof, err := lookup()
	if err != nil || proof.ReportPath != valid || proof.Result.HeadSHA != "head" {
		t.Fatalf("exact terminal proof = %+v, %v", proof, err)
	}
	extra := write(start.Add(2500*time.Millisecond), cleanupReport{GeneratedAt: start.Add(2500 * time.Millisecond), Phase: "applied", Apply: true, Task: task, Results: []CleanupResult{result}})
	contents, err := os.ReadFile(extra)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extra, append(contents, []byte(`{}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if proof, err := lookup(); err == nil || proof != nil || !strings.Contains(err.Error(), "later cleanup receipt") {
		t.Fatalf("unverifiable later receipt left stale proof: %+v, %v", proof, err)
	}

	// An exact later receipt supersedes earlier success. It must fail closed
	// when the later operation did not actually finish removing the checkout.
	later := result
	later.Applied = false
	later.WorktreeGone = false
	write(start.Add(3*time.Second), cleanupReport{GeneratedAt: start.Add(3 * time.Second), Phase: "applied", Apply: true, Tasks: []string{task}, Results: []CleanupResult{later}})
	if _, err := lookup(); err == nil || !strings.Contains(err.Error(), "does not prove") {
		t.Fatalf("stale success overrode later failure: %v", err)
	}
}

//nolint:paralleltest // t.Setenv isolates WB home and cannot run with t.Parallel.
func TestFindTerminalCleanupProofRejectsAmbiguousIdentityAndSymlink(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	worktree := filepath.Join(root, ".worktrees", "task", "github.com", "acme", "app")
	canonical, err := CanonicalRepositoryPath(root, "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	result := CleanupResult{ListResult: ListResult{Task: "task", Repository: "acme/app", WorktreeDir: worktree, CanonicalDir: canonical,
		Branch: "task", Base: "cov/integration", HeadSHA: "head", RemoteTargetSHA: "target", Clean: true, IntegratedAtOrigin: true},
		Eligible: true, Applied: true, WorktreeGone: true, BranchDeleted: true}
	at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	dir := filepath.Join(root, ".wb", "reports", "worktree-cleanup", at.Format("20060102T150405.000000000Z"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cleanup.json")
	write := func(report cleanupReport) {
		t.Helper()
		contents, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lookup := func() error {
		t.Helper()
		_, err := FindTerminalCleanupProof(root, "acme/app", "cov/integration", "task", worktree, "task")
		return err
	}
	write(cleanupReport{GeneratedAt: at, Phase: "applied", Apply: true, Task: "task", Tasks: []string{"task"}, Results: []CleanupResult{result}})
	if err := lookup(); err == nil || !strings.Contains(err.Error(), "no terminal cleanup receipt") {
		t.Fatalf("ambiguous selected task authorized cleanup: %v", err)
	}
	write(cleanupReport{GeneratedAt: at, Phase: "applied", Apply: true, Task: "task", Results: []CleanupResult{result, result}})
	if err := lookup(); err == nil || !strings.Contains(err.Error(), "no terminal cleanup receipt") {
		t.Fatalf("duplicate result authorized cleanup: %v", err)
	}
	write(cleanupReport{GeneratedAt: at, Phase: "applied", Apply: true, Task: "task", Results: []CleanupResult{result}})
	if err := os.Rename(path, path+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".real", path); err != nil {
		t.Fatal(err)
	}
	if err := lookup(); err == nil || !strings.Contains(err.Error(), "no terminal cleanup receipt") {
		t.Fatalf("symlink report authorized cleanup: %v", err)
	}
}
